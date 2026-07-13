package awximport

import (
	"github.com/dstout-devops/stratt/core/internal/connectors/awx"
	"github.com/dstout-devops/stratt/types"
)

// injectionFor gives a sensible injection policy per AWX credential kind. The
// key names the secret field the operator will populate at the backend; As/Name
// shape how the EE pod receives it. This is policy only — never material (§2.5).
var injectionFor = map[string]yInjection{
	"ssh":     {Key: "ssh_private_key", As: types.InjectFile, Name: "id_ssh"},
	"scm":     {Key: "ssh_private_key", As: types.InjectFile, Name: "id_scm"},
	"vault":   {Key: "vault_password", As: types.InjectFile, Name: "vault-password"},
	"aws":     {Key: "access_key", As: types.InjectEnv, Name: "AWS_ACCESS_KEY_ID"},
	"vmware":  {Key: "password", As: types.InjectEnv, Name: "VMWARE_PASSWORD"},
	"azuread": {Key: "client_secret", As: types.InjectEnv, Name: "AZURE_CLIENT_SECRET"},
}

// mapCredential transforms an AWX credential into a CredentialRef declaration:
// a pointer + injection policy. Material is NEVER imported (§2.5) — AWX returns
// $encrypted$ placeholders regardless. The locator and ownerTeam are REVIEW-ME
// placeholders the operator must set when re-brokering the secret.
func mapCredential(cr awx.Credential, r *report) (string, string, error) {
	name := "awx/" + slug(cr.Name)

	inj, ok := injectionFor[cr.Kind]
	if !ok {
		inj = yInjection{Key: "secret", As: types.InjectEnv, Name: "SECRET"}
		r.note("CredentialRef %q (was: %q credential %q): unmapped credential kind — defaulted the injection policy; adjust key/as/name.", name, cr.Kind, cr.Name)
	}

	doc := yCredRef{
		Name:      name,
		OwnerTeam: "REVIEW-ME",
		Backend:   types.BackendK8sSecret,
		Locator:   map[string]any{"namespace": "stratt-system", "name": "REVIEW-ME"},
		Injection: []yInjection{inj},
	}

	r.block("CredentialRef %q (was: %q credential %q): set ownerTeam and re-broker the secret into its backend (locator points at where). AWX credential material is never imported (§2.5). Review the corresponding Source trust settings.", name, cr.Kind, cr.Name)

	out, err := marshalYAML(doc)
	if err != nil {
		return "", "", mapErr("credential", cr.Name, err)
	}
	return name, out, nil
}
