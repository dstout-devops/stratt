package awximport

import (
	"strings"

	"github.com/dstout-devops/stratt/core/internal/awximport/awx"
)

// nativeSyncer maps an AWX inventory-source plugin to the Stratt Connector that
// already projects that estate, plus the selector shape to filter it. AWX
// dynamic inventories are exactly what native Syncers do — so the faithful
// mapping points a View at the Syncer's projected labels, never re-imports the
// hosts (§1.2). Empty Connector means no native Syncer ships yet.
var nativeSyncer = map[string]struct {
	connector string
	kind      string // the Entity kind the Syncer projects
	hint      string // a label the operator can scope on
}{
	"aws_ec2":  {"awsec2", "instance", "aws.region"},
	"ec2":      {"awsec2", "instance", "aws.region"},
	"vmware":   {"vcenter", "vm", "vcenter.name"},
	"azure_rm": {"msgraph", "device", "device.identity"},
}

// mapInventory transforms one AWX inventory into a View, dispatching on its
// shape: dynamic (inventory_source), smart (host_filter), or static (manual
// hosts). Returns the emitted View name and its YAML document.
func mapInventory(snap *awx.Snapshot, inv awx.Inventory, r *report) (string, string, error) {
	name := "awx/" + slug(inv.Name)
	v := yView{Name: name, Selector: ySelector{}}

	sources := snap.InventorySources[inv.ID]
	switch {
	case len(sources) > 0:
		mapDynamicInventory(name, inv, sources, &v.Selector, r)
	case inv.Kind == "smart":
		mapSmartInventory(name, inv, &v.Selector, r)
	default:
		mapStaticInventory(snap, name, inv, &v.Selector, r)
	}

	doc, err := marshalYAML(v)
	if err != nil {
		return "", "", mapErr("inventory", inv.Name, err)
	}
	return name, doc, nil
}

// mapDynamicInventory points the View at the native Syncer's projected labels
// and reports the source type + recommended Connector. It never imports hosts.
func mapDynamicInventory(name string, inv awx.Inventory, sources []awx.InventorySource, sel *ySelector, r *report) {
	for _, src := range sources {
		ns, ok := nativeSyncer[src.Source]
		if !ok || ns.connector == "" {
			r.block("View %q (was: dynamic inventory %q, source %q): no native Syncer ships for this source type yet — the View is a stub until one exists or you re-express the selector.", name, inv.Name, src.Source)
			continue
		}
		if sel.Kinds == nil {
			sel.Kinds = []string{ns.kind}
		}
		r.note("View %q (was: dynamic inventory %q): source %q ↦ run the native `%s` Connector; scope the View on its `%s` label. `source_vars` filters beyond that are yours to re-express.", name, inv.Name, src.Source, ns.connector, ns.hint)
	}
}

// mapSmartInventory reduces a host_filter to selector predicates where it maps
// cleanly; the irreducible remainder is dropped from the selector and reported.
func mapSmartInventory(name string, inv awx.Inventory, sel *ySelector, r *report) {
	labels, facets, irreducible := reduceHostFilter(inv.HostFilter)
	for k, val := range labels {
		if sel.Labels == nil {
			sel.Labels = map[string]string{}
		}
		sel.Labels[k] = val
	}
	sel.Facets = append(sel.Facets, facets...)
	if len(irreducible) > 0 {
		r.block("View %q (was: smart inventory %q): host_filter terms could not be reduced to a structured selector and were dropped — re-express manually: %s", name, inv.Name, strings.Join(irreducible, " ; "))
	}
	if len(labels) == 0 && len(facets) == 0 {
		r.note("View %q (was: smart inventory %q): host_filter %q produced no structured predicates — the View selects nothing until edited.", name, inv.Name, inv.HostFilter)
	}
}

// mapStaticInventory handles manual hosts — the writable-CMDB anti-pattern
// (§1.2). It NEVER projects the hosts as Entities. It emits a compat-label
// selector and a blocking note that a real Syncer must back this View.
func mapStaticInventory(snap *awx.Snapshot, name string, inv awx.Inventory, sel *ySelector, r *report) {
	sel.Labels = map[string]string{"awx.inventory.name": inv.Name}
	n := len(snap.Hosts[inv.ID])
	r.block("View %q (was: static inventory %q, %d manual host(s)): hand-entered hosts have no system of record. Stratt does not maintain a hand-entered host registry (§1.2, no writable CMDB). Project these from an authoritative Source (the cloud/vCenter/etc. they live in); until then this View is empty.", name, inv.Name, n)
}

// reduceHostFilter parses an AWX host_filter into structured predicates. Only a
// conjunction (`and`) of simple equalities reduces; anything with or/not/parens
// or a non-exact operator suffix is irreducible. Rules:
//
//	groups__name=<v>                    → label awx.group.name=<v>
//	name=<v> / name__exact=<v>          → label awx.host.name=<v>
//	ansible_facts__<seg>[__<seg>...]=<v>→ facet {namespace:seg0, path:seg1.., equals:<v>}
//
// Returns the reducible labels/facets and the raw irreducible term strings.
func reduceHostFilter(filter string) (map[string]string, []yFacet, []string) {
	labels := map[string]string{}
	var facets []yFacet
	var irreducible []string

	filter = strings.TrimSpace(filter)
	if filter == "" {
		return labels, facets, nil
	}
	// or/not/parens make the whole filter non-conjunctive → all irreducible.
	lower := strings.ToLower(filter)
	if strings.Contains(lower, " or ") || strings.Contains(lower, " not ") ||
		strings.ContainsAny(filter, "()~|") {
		return map[string]string{}, nil, []string{filter}
	}

	for _, term := range splitAnd(filter) {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		key, val, ok := strings.Cut(term, "=")
		if !ok {
			irreducible = append(irreducible, term)
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)

		switch {
		case key == "name" || key == "name__exact":
			labels["awx.host.name"] = val
		case key == "groups__name" || key == "groups__name__exact":
			labels["awx.group.name"] = val
		case strings.HasPrefix(key, "ansible_facts__"):
			segs := strings.Split(strings.TrimPrefix(key, "ansible_facts__"), "__")
			if len(segs) == 0 || segs[0] == "" {
				irreducible = append(irreducible, term)
				continue
			}
			facets = append(facets, yFacet{
				Namespace: segs[0],
				Path:      strings.Join(segs[1:], "."),
				Equals:    val,
			})
		default:
			// Non-exact operators (__icontains, __gt, __regex, …) and any
			// unrecognized field: cannot express as equality.
			irreducible = append(irreducible, term)
		}
	}
	return labels, facets, irreducible
}

// splitAnd splits on the ` and ` conjunction, case-insensitively, without a
// regex (keeps the boring-spine posture and is easy to audit).
func splitAnd(filter string) []string {
	var out []string
	rest := filter
	for {
		idx := indexFold(rest, " and ")
		if idx < 0 {
			out = append(out, rest)
			return out
		}
		out = append(out, rest[:idx])
		rest = rest[idx+len(" and "):]
	}
}

// indexFold is a case-insensitive strings.Index for the ASCII needle.
func indexFold(s, needle string) int {
	ls, ln := strings.ToLower(s), strings.ToLower(needle)
	return strings.Index(ls, ln)
}
