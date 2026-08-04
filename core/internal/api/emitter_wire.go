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

func verifyFromWire(in *EmitterVerify) *types.VerifySpec {
	if in == nil {
		return nil
	}
	out := &types.VerifySpec{Header: in.Header, Algorithm: string(in.Algorithm), KeyRef: in.KeyRef}
	if in.Format != nil {
		out.Format = string(*in.Format)
	}
	if in.SignatureKey != nil {
		out.SignatureKey = *in.SignatureKey
	}
	if in.TimestampKey != nil {
		out.TimestampKey = *in.TimestampKey
	}
	if in.SignedPayload != nil {
		out.SignedPayload = string(*in.SignedPayload)
	}
	if in.ToleranceSeconds != nil {
		out.ToleranceSeconds = int(*in.ToleranceSeconds)
	}
	if in.Encoding != nil {
		out.Encoding = string(*in.Encoding)
	}
	if in.Prefix != nil {
		out.Prefix = *in.Prefix
	}
	return out
}

func verifyToWire(in *types.VerifySpec) *EmitterVerify {
	if in == nil {
		return nil
	}
	out := &EmitterVerify{Header: in.Header, Algorithm: EmitterVerifyAlgorithm(in.Algorithm), KeyRef: in.KeyRef}
	if in.Format != "" {
		f := EmitterVerifyFormat(in.Format)
		out.Format = &f
	}
	if in.SignatureKey != "" {
		k := in.SignatureKey
		out.SignatureKey = &k
	}
	if in.TimestampKey != "" {
		k := in.TimestampKey
		out.TimestampKey = &k
	}
	if in.SignedPayload != "" {
		sp := EmitterVerifySignedPayload(in.SignedPayload)
		out.SignedPayload = &sp
	}
	if in.ToleranceSeconds > 0 {
		t := int64(in.ToleranceSeconds)
		out.ToleranceSeconds = &t
	}
	if in.Encoding != "" {
		e := EmitterVerifyEncoding(in.Encoding)
		out.Encoding = &e
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
