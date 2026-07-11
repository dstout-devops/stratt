package authz

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/openfga/go-sdk/client"
)

// authzModelJSON is the ADR-0009 v1 authorization model. The DSL in the ADR is
// documentation; this JSON is the artifact (charter §1.5: schemas are data,
// authored and pinned, not generated from language classes). The in-process
// TupleAuthorizer implements the same semantics and its tests define the
// model's meaning — TestOpenFGAAgreement holds the two together.
//
//go:embed authzmodel.json
var authzModelJSON []byte

const storeName = "stratt"

// OpenFGAAuthorizer is the server-backed Authorizer (charter §3: OpenFGA is
// the authorization substrate; the tuple evaluator is the no-substrate dev
// path). The store and model are ensured idempotently at construction, and
// tuples are synced from the same CaC manifest the evaluator loads — the
// server is a rebuildable projection of Git, exactly like the graph (§1.2).
type OpenFGAAuthorizer struct {
	fga *client.OpenFgaClient
}

// NewOpenFGAAuthorizer connects to the server at apiURL, finds or creates the
// store, and writes the embedded authorization model iff the latest stored
// model differs.
func NewOpenFGAAuthorizer(ctx context.Context, apiURL string) (*OpenFGAAuthorizer, error) {
	fga, err := client.NewSdkClient(&client.ClientConfiguration{ApiUrl: apiURL})
	if err != nil {
		return nil, fmt.Errorf("authz: openfga client: %w", err)
	}
	storeID, err := ensureStore(ctx, fga)
	if err != nil {
		return nil, err
	}
	if err := fga.SetStoreId(storeID); err != nil {
		return nil, fmt.Errorf("authz: openfga store id: %w", err)
	}
	modelID, err := ensureModel(ctx, fga)
	if err != nil {
		return nil, err
	}
	if err := fga.SetAuthorizationModelId(modelID); err != nil {
		return nil, fmt.Errorf("authz: openfga model id: %w", err)
	}
	return &OpenFGAAuthorizer{fga: fga}, nil
}

func ensureStore(ctx context.Context, fga *client.OpenFgaClient) (string, error) {
	token := ""
	for {
		opts := client.ClientListStoresOptions{}
		if token != "" {
			opts.ContinuationToken = &token
		}
		resp, err := fga.ListStores(ctx).Options(opts).Execute()
		if err != nil {
			return "", fmt.Errorf("authz: openfga list stores: %w", err)
		}
		for _, s := range resp.GetStores() {
			if s.GetName() == storeName {
				return s.GetId(), nil
			}
		}
		token = resp.GetContinuationToken()
		if token == "" {
			break
		}
	}
	created, err := fga.CreateStore(ctx).Body(client.ClientCreateStoreRequest{Name: storeName}).Execute()
	if err != nil {
		return "", fmt.Errorf("authz: openfga create store: %w", err)
	}
	return created.GetId(), nil
}

// ensureModel writes the embedded model only when the latest stored model
// differs semantically (models are immutable in OpenFGA; an unconditional
// write would mint a new version every startup).
func ensureModel(ctx context.Context, fga *client.OpenFgaClient) (string, error) {
	var desired client.ClientWriteAuthorizationModelRequest
	if err := json.Unmarshal(authzModelJSON, &desired); err != nil {
		return "", fmt.Errorf("authz: embedded model: %w", err)
	}
	latest, err := fga.ReadLatestAuthorizationModel(ctx).Execute()
	if err == nil && latest.AuthorizationModel != nil {
		current := latest.GetAuthorizationModel()
		curJSON, _ := json.Marshal(current.GetTypeDefinitions())
		wantJSON, _ := json.Marshal(desired.GetTypeDefinitions())
		if current.GetSchemaVersion() == desired.SchemaVersion && string(curJSON) == string(wantJSON) {
			return current.GetId(), nil
		}
	}
	written, err := fga.WriteAuthorizationModel(ctx).Body(desired).Execute()
	if err != nil {
		return "", fmt.Errorf("authz: openfga write model: %w", err)
	}
	return written.GetAuthorizationModelId(), nil
}

// Check implements Authorizer.
func (a *OpenFGAAuthorizer) Check(ctx context.Context, principalID, relation, object string) (bool, error) {
	resp, err := a.fga.Check(ctx).Body(client.ClientCheckRequest{
		User:     "principal:" + principalID,
		Relation: relation,
		Object:   object,
	}).Execute()
	if err != nil {
		// Fail closed at the seam; callers already treat (false, err) as deny.
		return false, fmt.Errorf("authz: openfga check: %w", err)
	}
	return resp.GetAllowed(), nil
}

// writeChunk keeps each transactional Write under the server's per-request
// tuple limit (100).
const writeChunk = 100

// SyncTuples reconciles the server's tuple set to exactly `desired` — the CaC
// manifest is the single source (§1.2); grants added out-of-band on the server
// are removed, not merged (no implicit precedence, §2.4).
func (a *OpenFGAAuthorizer) SyncTuples(ctx context.Context, desired []Tuple) error {
	current := map[Tuple]bool{}
	token := ""
	for {
		opts := client.ClientReadOptions{}
		if token != "" {
			opts.ContinuationToken = &token
		}
		resp, err := a.fga.Read(ctx).Body(client.ClientReadRequest{}).Options(opts).Execute()
		if err != nil {
			return fmt.Errorf("authz: openfga read tuples: %w", err)
		}
		for _, t := range resp.GetTuples() {
			k := t.GetKey()
			current[Tuple{User: k.GetUser(), Relation: k.GetRelation(), Object: k.GetObject()}] = true
		}
		token = resp.GetContinuationToken()
		if token == "" {
			break
		}
	}

	want := make(map[Tuple]bool, len(desired))
	for _, t := range desired {
		want[t] = true
	}
	var writes []client.ClientTupleKey
	var deletes []client.ClientTupleKeyWithoutCondition
	for _, t := range desired { // slice order: deterministic batches
		if !current[t] {
			writes = append(writes, client.ClientTupleKey{User: t.User, Relation: t.Relation, Object: t.Object})
		}
	}
	for t := range current {
		if !want[t] {
			deletes = append(deletes, client.ClientTupleKeyWithoutCondition{User: t.User, Relation: t.Relation, Object: t.Object})
		}
	}
	for len(writes) > 0 || len(deletes) > 0 {
		req := client.ClientWriteRequest{}
		n := 0
		for len(writes) > 0 && n < writeChunk {
			req.Writes = append(req.Writes, writes[0])
			writes, n = writes[1:], n+1
		}
		for len(deletes) > 0 && n < writeChunk {
			req.Deletes = append(req.Deletes, deletes[0])
			deletes, n = deletes[1:], n+1
		}
		if _, err := a.fga.Write(ctx).Body(req).Execute(); err != nil {
			return fmt.Errorf("authz: openfga write tuples: %w", err)
		}
	}
	return nil
}
