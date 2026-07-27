package dns

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"

	pluginv1 "github.com/dstout-devops/stratt/sdk/stratt/plugin/v1"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeZone is an in-memory zone. The plugin's whole content-expertise — canonicalizing
// names, choosing A vs CNAME from a coordinate, projecting records onto Entities — is
// exercised with NO DNS server anywhere (the ADR-0046 module-isolation property).
type fakeZone struct {
	mu      sync.Mutex
	records map[string][]Record // zone → records
	failOn  string              // a record name whose Update fails, for the error path
}

func newFakeZone(seed ...Record) *fakeZone {
	f := &fakeZone{records: map[string][]Record{}}
	for _, r := range seed {
		f.records["estate.example"] = append(f.records["estate.example"], r)
	}
	return f
}

func (f *fakeZone) Transfer(_ context.Context, zone string) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Record(nil), f.records[zone]...), nil
}

func (f *fakeZone) Update(_ context.Context, zone string, rec Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOn != "" && strings.HasPrefix(rec.Name, f.failOn) {
		return io.ErrUnexpectedEOF
	}
	out := f.records[zone][:0:0]
	for _, r := range f.records[zone] {
		if r.Name != rec.Name || r.Type != rec.Type {
			out = append(out, r)
		}
	}
	f.records[zone] = append(out, rec)
	return nil
}

func (f *fakeZone) Remove(_ context.Context, zone, name, rtype string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.records[zone][:0:0]
	for _, r := range f.records[zone] {
		if r.Name != name || r.Type != rtype {
			out = append(out, r)
		}
	}
	f.records[zone] = out
	return nil
}

type applyCapture struct {
	grpc.ServerStream
	ctx  context.Context
	msgs []*pluginv1.ApplyResponse
}

func (c *applyCapture) Send(m *pluginv1.ApplyResponse) error { c.msgs = append(c.msgs, m); return nil }
func (c *applyCapture) Context() context.Context             { return c.ctx }

type invokeCapture struct {
	grpc.ServerStream
	ctx  context.Context
	msgs []*pluginv1.InvokeResponse
}

func (c *invokeCapture) Send(m *pluginv1.InvokeResponse) error {
	c.msgs = append(c.msgs, m)
	return nil
}
func (c *invokeCapture) Context() context.Context { return c.ctx }

type observeCapture struct {
	grpc.ServerStream
	ctx  context.Context
	msgs []*pluginv1.ObserveResponse
}

func (c *observeCapture) Send(m *pluginv1.ObserveResponse) error {
	c.msgs = append(c.msgs, m)
	return nil
}
func (c *observeCapture) Context() context.Context { return c.ctx }

func newServer(cfg Config, z zoneClient) *Server {
	return NewServer(cfg, discard()).WithZoneClient(z)
}

// ── The declared record: what the estate says, and what it may not say ──────────────

func TestNewRecordValidatesTheDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name, zone, rname, rtype, data string
		want                           string // "" == must succeed, with this canonical name
		wantErr                        string
	}{
		{name: "bare name is qualified into the zone", zone: "estate.example", rname: "web-01", rtype: "A", data: "10.0.0.5", want: "web-01.estate.example"},
		{name: "qualified name in zone passes through", zone: "estate.example", rname: "web-01.estate.example", rtype: "A", data: "10.0.0.5", want: "web-01.estate.example"},
		{name: "case is normalized once, here", zone: "Estate.Example", rname: "WEB-01", rtype: "a", data: "10.0.0.5", want: "web-01.estate.example"},
		{name: "trailing dots are not identity", zone: "estate.example.", rname: "web-01.estate.example.", rtype: "A", data: "10.0.0.5", want: "web-01.estate.example"},
		// The refusals. Each is a real defect the seam catches instead of the server.
		{name: "a name from another zone is refused, never re-suffixed", zone: "estate.example", rname: "www.other.example", rtype: "A", data: "10.0.0.5", wantErr: "not in zone"},
		{name: "an A whose data is a name", zone: "estate.example", rname: "web-01", rtype: "A", data: "some.host", wantErr: "not an IP address"},
		{name: "an A whose data is IPv6", zone: "estate.example", rname: "web-01", rtype: "A", data: "2001:db8::1", wantErr: "use AAAA"},
		{name: "an AAAA whose data is IPv4", zone: "estate.example", rname: "web-01", rtype: "AAAA", data: "10.0.0.5", wantErr: "use A"},
		{name: "a CNAME whose data is an address", zone: "estate.example", rname: "web", rtype: "CNAME", data: "10.0.0.5", wantErr: "an address, not a name"},
		{name: "a CNAME pointing at itself is a resolver loop", zone: "estate.example", rname: "web-01", rtype: "CNAME", data: "web-01.estate.example", wantErr: "points at itself"},
		{name: "an unsupported type is refused at the seam", zone: "estate.example", rname: "estate.example", rtype: "MX", data: "10 mail.estate.example", wantErr: "not supported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, err := newRecord(tc.zone, tc.rname, tc.rtype, tc.data, 0)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want an error containing %q, got record %s", tc.wantErr, rec)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if rec.Name != tc.want {
				t.Errorf("name = %q, want %q", rec.Name, tc.want)
			}
			if rec.TTL != defaultTTL {
				t.Errorf("ttl = %d, want the %d default", rec.TTL, defaultTTL)
			}
		})
	}
}

// TestRecordForTargetChoosesTypeFromTheCoordinate pins the branch that keeps IPs out of
// Git: the record's data is whatever the graph observed, and its TYPE follows from the
// shape of that value rather than from anything anyone declared.
func TestRecordForTargetChoosesTypeFromTheCoordinate(t *testing.T) {
	for _, tc := range []struct {
		coordinate, wantType, wantData string
	}{
		{"10.0.0.5", "A", "10.0.0.5"},
		{"2001:db8::1", "AAAA", "2001:db8::1"},
		{"web-01.dev.stratt.test", "CNAME", "web-01.dev.stratt.test"},
		{"WEB-01.DEV.stratt.test.", "CNAME", "web-01.dev.stratt.test"},
	} {
		rec, err := recordForTarget("estate.example", "web-01", tc.coordinate, 0)
		if err != nil {
			t.Fatalf("%s: %v", tc.coordinate, err)
		}
		if rec.Type != tc.wantType || rec.Data != tc.wantData {
			t.Errorf("coordinate %q → %s %s, want %s %s", tc.coordinate, rec.Type, rec.Data, tc.wantType, tc.wantData)
		}
	}
}

// ── The projection: ADR-0144 D3's table, which is the whole design ──────────────────

func TestNormalizeZoneProjectsTheEntityARecordNames(t *testing.T) {
	ents, skipped := normalizeZone("host", []Record{
		{Name: "web-01.estate.example", Type: "A", Data: "10.0.0.5"},
		{Name: "estate.example", Type: "-"}, // an SOA/NS/MX — not a coordinate
	})
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 — a projection that drops input should be able to say how much", skipped)
	}
	if len(ents) != 1 {
		t.Fatalf("projected %d entities, want 1", len(ents))
	}
	e := ents[0]
	if got := e.GetIdentityKeys()["dns.fqdn"]; got != "web-01.estate.example" {
		t.Errorf("identity = %q, want the record's own name (an A record IS canonical)", got)
	}
	if got := addressOf(t, e); got != "web-01.estate.example" {
		t.Errorf("mgmt.address = %q, want the NAME — the coordinate is the record, never its data", got)
	}
}

// TestNormalizeZoneProjectsACnameOntoItsCanonicalTarget is the load-bearing row. A CNAME
// says "this name is an alias for that canonical name", so the Entity is the CANONICAL
// one and the alias is an additional coordinate for it — which is how the estate's
// stable name lands on the machine the substrate named something else.
func TestNormalizeZoneProjectsACnameOntoItsCanonicalTarget(t *testing.T) {
	ents, _ := normalizeZone("vm", []Record{
		{Name: "web.estate.example", Type: "CNAME", Data: "web-01.dev.stratt.test"},
	})
	if len(ents) != 1 {
		t.Fatalf("projected %d entities, want 1", len(ents))
	}
	if got := ents[0].GetIdentityKeys()["dns.fqdn"]; got != "web-01.dev.stratt.test" {
		t.Errorf("identity = %q, want the CANONICAL target — correlating onto the machine is the entire mechanism", got)
	}
	if got := addressOf(t, ents[0]); got != "web.estate.example" {
		t.Errorf("mgmt.address = %q, want the ALIAS — the estate's name is what everything should dial", got)
	}
	if got := ents[0].GetKind(); got != "vm" {
		t.Errorf("kind = %q, want the operator's declared kind: a correlated projection SETS kind, so guessing here retypes another source's Entity", got)
	}
}

func TestNormalizeZoneIsDeterministicWhenTwoNamesClaimOneEntity(t *testing.T) {
	records := []Record{
		{Name: "zeta.estate.example", Type: "CNAME", Data: "web-01.dev.stratt.test"},
		{Name: "alpha.estate.example", Type: "CNAME", Data: "web-01.dev.stratt.test"},
	}
	first, _ := normalizeZone("host", records)
	// Reversed input, same answer: a coordinate that flipped between two equally valid
	// aliases on alternating syncs would make a Run's target depend on when it ran.
	reversed, _ := normalizeZone("host", []Record{records[1], records[0]})
	if len(first) != 1 || len(reversed) != 1 {
		t.Fatalf("want one entity per pass, got %d and %d", len(first), len(reversed))
	}
	if a, b := addressOf(t, first[0]), addressOf(t, reversed[0]); a != b || a != "alpha.estate.example" {
		t.Errorf("coordinate depends on record order: %q vs %q", a, b)
	}
}

func TestNormalizeZoneProjectsNothingForRecordsThatNameNoHost(t *testing.T) {
	ents, skipped := normalizeZone("host", []Record{
		{Name: "estate.example", Type: "-"},
		{Name: "_dmarc.estate.example", Type: "-"},
	})
	if len(ents) != 0 {
		t.Errorf("projected %d entities from non-coordinate records — a zone read-model is a second DNS, not a projection", len(ents))
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
}

func addressOf(t *testing.T, e *pluginv1.ObservedEntity) string {
	t.Helper()
	raw, ok := e.GetFacets()["mgmt.address"]
	if !ok {
		t.Fatal("entity carries no mgmt.address facet")
	}
	var f struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("mgmt.address is not the closed {address, port?} shape: %v (%s)", err, raw)
	}
	return f.Address
}

// ── Observe ────────────────────────────────────────────────────────────────────────

func TestObserveRefusesWithNoDeclaredZones(t *testing.T) {
	s := newServer(Config{}, newFakeZone())
	err := s.Observe(&pluginv1.ObserveRequest{}, &observeCapture{ctx: context.Background()})
	if err == nil {
		t.Fatal("a Syncer with no zones must fail loudly, not report an empty estate — an empty full sync is a claim that nothing exists")
	}
}

func TestObserveProjectsEveryDeclaredZone(t *testing.T) {
	z := newFakeZone(Record{Name: "web-01.estate.example", Type: "A", Data: "10.0.0.5"})
	cap := &observeCapture{ctx: context.Background()}
	s := newServer(Config{Zones: []string{"estate.example"}}, z)
	if err := s.Observe(&pluginv1.ObserveRequest{}, cap); err != nil {
		t.Fatal(err)
	}
	if len(cap.msgs) != 1 || !cap.msgs[0].GetFullSyncComplete() {
		t.Fatalf("want one full-sync window, got %d", len(cap.msgs))
	}
	if len(cap.msgs[0].GetEntities()) != 1 {
		t.Fatalf("want 1 entity, got %d", len(cap.msgs[0].GetEntities()))
	}
}

// ── The fleet Apply: the half where no address is ever declared ─────────────────────

func TestApplyRegistersEachTargetAtItsOwnCoordinate(t *testing.T) {
	z := newFakeZone()
	cap := &applyCapture{ctx: context.Background()}
	s := newServer(Config{}, z)
	err := s.Apply(&pluginv1.ApplyRequest{
		Desired: &pluginv1.Payload{Bytes: []byte(`{"zone":"estate.example"}`)},
		Targets: []*pluginv1.ApplyTarget{
			{Name: "web-01", Address: "10.0.0.5"},
			{Name: "web-02", Address: "web-02.dev.stratt.test"},
		},
	}, cap)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := z.Transfer(context.Background(), "estate.example")
	if len(got) != 2 {
		t.Fatalf("wrote %d records, want 2: %v", len(got), got)
	}
	byName := map[string]Record{}
	for _, r := range got {
		byName[r.Name] = r
	}
	if r := byName["web-01.estate.example"]; r.Type != "A" || r.Data != "10.0.0.5" {
		t.Errorf("web-01 → %s %s, want A 10.0.0.5", r.Type, r.Data)
	}
	if r := byName["web-02.estate.example"]; r.Type != "CNAME" || r.Data != "web-02.dev.stratt.test" {
		t.Errorf("web-02 → %s %s, want CNAME to its observed name", r.Type, r.Data)
	}
}

// TestApplySkipsATargetWithNoCoordinate is ADR-0144 D1's limit, made executable:
// registration binds a name to a coordinate the substrate already produced. It cannot
// conjure reachability, and a name that resolves nowhere is worse than an absent one.
func TestApplySkipsATargetWithNoCoordinate(t *testing.T) {
	z := newFakeZone()
	cap := &applyCapture{ctx: context.Background()}
	s := newServer(Config{}, z)
	if err := s.Apply(&pluginv1.ApplyRequest{
		Desired: &pluginv1.Payload{Bytes: []byte(`{"zone":"estate.example"}`)},
		Targets: []*pluginv1.ApplyTarget{{Name: "not-yet-booted"}},
	}, cap); err != nil {
		t.Fatal(err)
	}
	got, _ := z.Transfer(context.Background(), "estate.example")
	if len(got) != 0 {
		t.Fatalf("wrote %v for a target with no coordinate — that name would resolve nowhere", got)
	}
	// The skip must be VISIBLE. A silent OK is the §1.8 defect this whole path is about.
	var warned bool
	for _, m := range cap.msgs {
		if m.GetEvent().GetLevel() == pluginv1.TaskEvent_LEVEL_WARN &&
			strings.Contains(m.GetEvent().GetMessage(), "no reach coordinate") {
			warned = true
		}
	}
	if !warned {
		t.Error("no WARN event named the skipped target — a skip nobody can see is a silent success")
	}
	terminal := cap.msgs[len(cap.msgs)-1].GetEvent()
	if !strings.Contains(terminal.GetMessage(), "1 skipped") {
		t.Errorf("terminal summary %q does not count the skip", terminal.GetMessage())
	}
}

func TestApplyDryRunWritesNothing(t *testing.T) {
	z := newFakeZone()
	s := newServer(Config{}, z)
	if err := s.Apply(&pluginv1.ApplyRequest{
		Desired: &pluginv1.Payload{Bytes: []byte(`{"zone":"estate.example"}`)},
		Targets: []*pluginv1.ApplyTarget{{Name: "web-01", Address: "10.0.0.5"}},
		DryRun:  true,
	}, &applyCapture{ctx: context.Background()}); err != nil {
		t.Fatal(err)
	}
	if got, _ := z.Transfer(context.Background(), "estate.example"); len(got) != 0 {
		t.Fatalf("a dry run wrote %v", got)
	}
}

func TestApplyFailsTheStepWhenAWriteFails(t *testing.T) {
	z := newFakeZone()
	z.failOn = "web-01"
	cap := &applyCapture{ctx: context.Background()}
	s := newServer(Config{}, z)
	if err := s.Apply(&pluginv1.ApplyRequest{
		Desired: &pluginv1.Payload{Bytes: []byte(`{"zone":"estate.example"}`)},
		Targets: []*pluginv1.ApplyTarget{{Name: "web-01", Address: "10.0.0.5"}},
	}, cap); err != nil {
		t.Fatal(err)
	}
	terminal := cap.msgs[len(cap.msgs)-1].GetEvent()
	if terminal.GetOk() {
		t.Error("a failed write reported a green Run — partial success is never green (§1.8)")
	}
}

// ── The singleton Action ───────────────────────────────────────────────────────────

func TestInvokeRegisterWritesAndProjectsBack(t *testing.T) {
	z := newFakeZone()
	cap := &invokeCapture{ctx: context.Background()}
	s := newServer(Config{}, z)
	args := `{"zone":"estate.example","name":"www","type":"CNAME","data":"web-01.estate.example",
	          "projectKind":"dns-record","labels":{"stratt.intent/singleton":"Intent/DnsRecord/www"}}`
	if err := s.Invoke(&pluginv1.InvokeRequest{
		Action: actionRegister,
		Args:   &pluginv1.Payload{Bytes: []byte(args)},
	}, cap); err != nil {
		t.Fatal(err)
	}
	final := cap.msgs[len(cap.msgs)-1]
	if !final.GetEvent().GetOk() {
		t.Fatalf("register failed: %s", final.GetEvent().GetMessage())
	}
	got, _ := z.Transfer(context.Background(), "estate.example")
	if len(got) != 1 || got[0].Name != "www.estate.example" {
		t.Fatalf("zone holds %v", got)
	}
	ents := final.GetResult().GetEntities()
	if len(ents) != 1 {
		t.Fatalf("want one projected entity, got %d", len(ents))
	}
	if ents[0].GetLabels()["stratt.intent/singleton"] == "" {
		t.Error("the project-back dropped the singleton correlation label — its own provisioning Finding would never resolve")
	}
	if len(ents[0].GetFacets()) != 0 {
		t.Error("the Action projected a Facet — mgmt.address is the Syncer's alone (ADR-0144 D5): a build asserts what it INTENDED, a zone read reports what is TRUE")
	}
}

func TestInvokeRejectsAnUnknownAction(t *testing.T) {
	s := newServer(Config{}, newFakeZone())
	err := s.Invoke(&pluginv1.InvokeRequest{Action: "dns/delete-everything"}, &invokeCapture{ctx: context.Background()})
	if err == nil {
		t.Fatal("an unadvertised action must be refused")
	}
}

func TestInvokeDeregisterRemovesTheRRset(t *testing.T) {
	z := newFakeZone(Record{Name: "www.estate.example", Type: "CNAME", Data: "web-01.estate.example"})
	cap := &invokeCapture{ctx: context.Background()}
	s := newServer(Config{}, z)
	if err := s.Invoke(&pluginv1.InvokeRequest{
		Action: actionDeregister,
		Args:   &pluginv1.Payload{Bytes: []byte(`{"zone":"estate.example","name":"www","type":"CNAME"}`)},
	}, cap); err != nil {
		t.Fatal(err)
	}
	if !cap.msgs[len(cap.msgs)-1].GetEvent().GetOk() {
		t.Fatal("deregister failed")
	}
	if got, _ := z.Transfer(context.Background(), "estate.example"); len(got) != 0 {
		t.Fatalf("zone still holds %v", got)
	}
}
