// The ansible-automation Connector plugin (ADR-0127): ONE plugin, ONE binary, ONE name
// in the estate for Red Hat's Ansible Automation Platform — which an operator runs as one
// system and which Stratt used to meet with two differently-named plugins (`awx` and
// `ansibleproject`, the latter naming an AWX noun rather than the product).
//
// It serves TWO halves, selected per instance by STRATT_ANSIBLE_AUTOMATION_ROLE, each
// registering its OWN Grant and therefore its OWN graph.source (ADR-0127 D1 — the
// unification is of the SURFACE, not of the ownership):
//
//   controller/ → an AAP Controller's /api/v2, projected as ansible.template/.workflow/
//                 .schedule/.org/.team under Source.Kind `ansible.controller`. Also carries
//                 the adopt/materialize Action — the AWX→CaC transform (ADR-0089).
//   content/    → a raw Ansible content root (a mounted Git checkout), projected as
//                 ansible.playbook/.role/.collection/.inventory under `ansible.content`.
//
// The halves stay two Sources on purpose: a shared Source would blank half the mirror on
// every full sync (each Observe drives a per-Source tombstone sweep), and it would collapse
// the CROSS-source `runs` edge ADR-0085's orphan Finding rests on. One instance per Source
// (ADR-0127 D1, corrected): endpoint/credential/content-root are instance config, exactly as
// vcenter is per-vCenter and netbox is per-NetBox.
//
// Read-only either way (§1.2): AAP and Git stay authoritative and AAP keeps executing; we
// never import — the projection is always-on and `stratt adopt` is the deliberate act that
// takes authority over an already-observed object. Its own build/test/CI unit; imports the
// lean plugin SDK and NOTHING from core/ (module isolation, ADR-0046). The plugin holds no
// graph write path — it proposes typed ObservedEntity values; the core-side host governs writes.
module github.com/dstout-devops/stratt/plugins/ansible-automation

go 1.26.0

require (
	github.com/dstout-devops/stratt/sdk v0.0.0-00010101000000-000000000000
	github.com/dstout-devops/stratt/sdk/secretbroker v0.0.0-00010101000000-000000000000
	github.com/dstout-devops/stratt/types v0.0.0-00010101000000-000000000000
	go.yaml.in/yaml/v3 v3.0.4
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af
	gopkg.in/yaml.v3 v3.0.1
	k8s.io/api v0.36.2
	k8s.io/apimachinery v0.36.2
	k8s.io/client-go v0.36.2
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/emicklei/go-restful/v3 v3.13.0 // indirect
	github.com/fxamacker/cbor/v2 v2.9.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-openapi/jsonpointer v0.22.5 // indirect
	github.com/go-openapi/jsonreference v0.20.2 // indirect
	github.com/go-openapi/swag v0.23.0 // indirect
	github.com/go-openapi/swag/jsonname v0.25.5 // indirect
	github.com/google/gnostic-models v0.7.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/term v0.43.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	gopkg.in/evanphx/json-patch.v4 v4.13.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	k8s.io/klog/v2 v2.140.0 // indirect
	k8s.io/kube-openapi v0.0.0-20260317180543-43fb72c5454a // indirect
	k8s.io/utils v0.0.0-20260210185600-b8788abfbbc2 // indirect
	sigs.k8s.io/json v0.0.0-20250730193827-2d320260d730 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.3.2 // indirect
	sigs.k8s.io/yaml v1.6.0 // indirect
)

replace github.com/dstout-devops/stratt/sdk => ../../sdk

replace github.com/dstout-devops/stratt/sdk/secretbroker => ../../sdk/secretbroker

replace github.com/dstout-devops/stratt/types => ../../types
