package reconcile

import (
	"testing"

	"github.com/dsjodin/provider-box/services/dns-sync/internal/model"
)

func TestDiffDedupesDesired(t *testing.T) {
	rec := model.Record{Zone: "lab.io.", Name: "provider.lab.io.", Type: "A", Data: "10.10.10.10"}
	// provider.lab.io appears twice: NetBox canonical host IP + built-ins.
	ops := Diff([]model.Record{rec, rec}, nil, nil)

	creates := 0
	for _, op := range ops {
		if op.Kind == model.OpCreate {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("Diff emitted %d creates for a duplicated record, want 1 (the second create fails 'record already exists' and aborts the pass)", creates)
	}
}
