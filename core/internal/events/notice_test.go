package events

import (
	"testing"
	"time"

	"github.com/dstout-devops/stratt/types"
)

// TestNoticeHashDedupAxis: the hash is the dedup identity for a publish. It
// must be stable across the At timestamp (a Temporal activity retry re-emits
// the same Notice with a fresh time — that retry must dedup) but differ on the
// Subject (a genuinely new occurrence must publish independently).
func TestNoticeHashDedupAxis(t *testing.T) {
	base := types.Notice{Kind: types.NoticeRunFailed, Subject: "run-1",
		Payload: map[string]any{"view": "prod"}, At: time.Unix(100, 0).UTC()}
	retry := base
	retry.At = time.Unix(999, 0).UTC() // different timestamp, same occurrence

	if NoticeHash(base) != NoticeHash(retry) {
		t.Fatal("hash must be independent of At (retries must dedup)")
	}

	other := base
	other.Subject = "run-2"
	if NoticeHash(base) == NoticeHash(other) {
		t.Fatal("distinct subjects must hash differently")
	}

	diffKind := base
	diffKind.Kind = types.NoticeRunCanceled
	if NoticeHash(base) == NoticeHash(diffKind) {
		t.Fatal("distinct kinds must hash differently")
	}
}
