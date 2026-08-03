package api

import "github.com/dstout-devops/stratt/types"

// The Emitter's fan-out declaration across the wire, both ways (ADR-0163 D1).
//
// Written out rather than left to a struct tag because BOTH directions were silently dropping
// it when the field was added: the apply door would have accepted a declaration and stored an
// Emitter that fans nothing out, and the list door would have shown an estate a declaration it
// does not have. That is the "a declared X never reached the thing that runs it" defect this
// repo keeps finding, and it is invisible until a POST arrives.

func tokenFromWire(in *EmitterToken) *types.TokenSpec {
	if in == nil {
		return nil
	}
	out := &types.TokenSpec{}
	if in.Header != nil {
		out.Header = *in.Header
	}
	if in.Prefix != nil {
		out.Prefix = *in.Prefix
	}
	return out
}

func tokenToWire(in *types.TokenSpec) *EmitterToken {
	if in == nil {
		return nil
	}
	out := &EmitterToken{}
	if in.Header != "" {
		h := in.Header
		out.Header = &h
	}
	if in.Prefix != "" {
		p := in.Prefix
		out.Prefix = &p
	}
	return out
}

func explodeFromWire(in *EmitterExplode) *types.ExplodeSpec {
	if in == nil {
		return nil
	}
	out := &types.ExplodeSpec{Path: in.Path}
	if in.Merge != nil {
		for _, m := range *in.Merge {
			mk := types.MergeKey{Path: m.Path}
			if m.As != nil {
				mk.As = *m.As
			}
			out.Merge = append(out.Merge, mk)
		}
	}
	return out
}

func explodeToWire(in *types.ExplodeSpec) *EmitterExplode {
	if in == nil {
		return nil
	}
	out := &EmitterExplode{Path: in.Path}
	if len(in.Merge) > 0 {
		merge := make([]struct {
			As   *string `json:"as,omitempty"`
			Path string  `json:"path"`
		}, 0, len(in.Merge))
		for _, m := range in.Merge {
			e := struct {
				As   *string `json:"as,omitempty"`
				Path string  `json:"path"`
			}{Path: m.Path}
			if m.As != "" {
				as := m.As
				e.As = &as
			}
			merge = append(merge, e)
		}
		out.Merge = &merge
	}
	return out
}
