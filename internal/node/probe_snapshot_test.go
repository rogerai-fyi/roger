package node

// ONE SOURCE OF TRUTH FOR EVERY CLIENT.
//
// The snapshot is the shared shape: the web console reads it over HTTP and the TUI mirrors
// it into its render cache. So the probe/traffic split belongs HERE, not in either front
// end - a client computing its own "served" would be free to disagree with the terminal
// about what the same rig did, and the operator would have no way to tell which was lying.
//
// The split itself is tested where it happens (internal/agent). What this file locks is the
// contract clients bind to: probes are carried, and carried SEPARATELY.

import (
	"encoding/json"
	"testing"
)

func TestSnapshotCarriesProbesSeparatelyFromServedTraffic(t *testing.T) {
	blob, err := json.Marshal(RowView{Served: 4, OutTokens: 900, Probes: 2738, ProbeTokens: 48001})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatal(err)
	}

	if got["served"] != float64(4) {
		t.Errorf("served = %v, want 4 - canary traffic must not be folded into it", got["served"])
	}
	if got["probes"] != float64(2738) {
		t.Errorf("probes = %v, want 2738 - hidden from the served count, not discarded", got["probes"])
	}
	if got["probe_tokens"] != float64(48001) {
		t.Errorf("probe_tokens = %v, want 48001", got["probe_tokens"])
	}
}

// A station that has served no probes sends no probe keys at all, rather than zeros. A
// printed 0 reads as a measurement ("we probed you nothing"), and the rest of this payload
// already follows that rule for quant/weights/variant.
func TestASnapshotWithNoProbesSaysNothingAboutThem(t *testing.T) {
	blob, err := json.Marshal(RowView{Served: 4})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(blob, &got)
	for _, k := range []string{"probes", "probe_tokens"} {
		if _, present := got[k]; present {
			t.Errorf("%q is present with no probe traffic - absent must render as absent", k)
		}
	}
}
