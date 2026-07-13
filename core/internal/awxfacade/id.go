package awxfacade

import (
	"crypto/md5" //nolint:gosec // non-cryptographic: a stable AWX-compat id hash
	"encoding/binary"
)

// awxID synthesizes AWX tooling's required INTEGER id from a Stratt string
// identity (a Workflow/View name or a Run uuid). Stateless and deterministic:
// md5 of the string, first 4 bytes big-endian, masked to a positive int31. The
// SQL twin graph.awx_run_id(uuid) (migration 00014) computes the identical
// value, so a Run's id is reversible via an indexed query — no mapping table,
// nothing persisted (§1.5). Parity is pinned by TestIDParity.
func awxID(s string) int64 {
	sum := md5.Sum([]byte(s)) //nolint:gosec
	n := binary.BigEndian.Uint32(sum[:4])
	return int64(n & 0x7fffffff)
}
