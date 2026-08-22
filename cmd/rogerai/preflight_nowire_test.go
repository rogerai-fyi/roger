package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"rogerai.fm/roger/v6/internal/detect"
)

// preflight_nowire_test.go is the pin that keeps the local hardware preflight local.
//
// The whole design rests on one claim: the preflight may look at everything the privacy
// bucketing withholds from the network - GPU model, VRAM, system RAM, free disk, core
// count - BECAUSE none of it is transmitted. docs/relay-selection-design.md §4.1 is why
// that matters: a capability the supply side declares is a lever, and this session already
// found two of them. If a later change quietly widened `protocol.NodeRegistration`, or
// folded a VRAM figure into an offer to help placement, the reasoning above would silently
// become false and the check would have created the exact defect it exists to avoid.
//
// So the claim is tested the way the locality work pinned "nothing logged": not by reading
// the struct that is supposed to carry it, but by RECORDING EVERY BYTE the share path
// actually sends and searching all of it. A sentinel machine goes in - values no real host
// could produce and no other code could invent - and the test fails if any of them appear
// anywhere on the wire: any path, any query string, any header, any body.

// wireRecorder is a broker that keeps a verbatim transcript of everything a node sends it:
// method, path, query, headers and body, for every request on every endpoint. It answers
// the shapes the agent loops need so they neither spin nor stall - register and heartbeat
// 200 `{}`, poll long-holds then 204 - and it is deliberately indiscriminate about what it
// records, because the point is to catch a leak on an endpoint nobody thought to check.
type wireRecorder struct {
	srv *httptest.Server
	mu  sync.Mutex
	log []string
}

func newWireRecorder(t *testing.T) *wireRecorder {
	t.Helper()
	w := &wireRecorder{}
	done := make(chan struct{})
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var b strings.Builder
		fmt.Fprintf(&b, "%s %s?%s\n", r.Method, r.URL.Path, r.URL.RawQuery)
		for k, vs := range r.Header {
			fmt.Fprintf(&b, "%s: %s\n", k, strings.Join(vs, ","))
		}
		b.Write(body)
		w.mu.Lock()
		w.log = append(w.log, b.String())
		w.mu.Unlock()

		if r.URL.Path == "/agent/poll" {
			select {
			case <-done:
			case <-time.After(25 * time.Second):
			}
			rw.WriteHeader(http.StatusNoContent)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.Write([]byte(`{}`))
	}))
	t.Cleanup(w.srv.Close)
	t.Cleanup(func() { close(done) }) // before Close: let the long-polls drain
	return w
}

// transcript is everything the node sent, concatenated, for a substring search.
func (w *wireRecorder) transcript() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.log, "\n---\n")
}

// sentinelBox is a machine whose every measurable value is a marker no real host would
// produce, so a hit in the transcript is proof of provenance rather than a coincidence.
func sentinelBox() detect.LocalHW {
	return detect.LocalHW{
		Class:        detect.HWMultiGPU,
		GPUs:         []detect.LocalGPU{{Model: "SENTINELGPUMODELQX9", VRAMMiB: 65531}, {Model: "SENTINELGPUMODELQX9", VRAMMiB: 65531}},
		VRAMTotalMiB: 131062, VRAMKnown: true,
		RAMTotalMiB: 524269, RAMKnown: true,
		DiskFreeMiB: 999983, DiskKnown: true, DiskPath: "/sentineldiskpathqx9",
		CPUCores:     4093,
		Undetermined: []string{"SENTINELNOTEQX9: an undetermined line is operator-facing too"},
	}
}

// TestTheLocalHardwarePictureNeverReachesTheWire drives a REAL `roger share` - real
// agent.Start, real registration, real heartbeats, real relay-fabric attempt - on a
// sentinel machine, and searches everything it sent.
func TestTheLocalHardwarePictureNeverReachesTheWire(t *testing.T) {
	useTempConfig(t)
	hw := sentinelBox()
	stubPreflight(t, hw)
	rec := newWireRecorder(t)
	realAgentStart(t)

	if err := runShare(t, config{Broker: rec.srv.URL, User: "op"},
		[]string{"m1", "--upstream", "http://127.0.0.1:1234/v1"}); err != nil {
		t.Fatalf("cmdShare = %v, want nil", err)
	}
	// The relay-fabric attempt is a background goroutine by design (it is additive reach,
	// never a precondition), so give it a moment to have sent whatever it is going to send
	// before the transcript is read.
	time.Sleep(300 * time.Millisecond)

	sent := rec.transcript()
	if !strings.Contains(sent, "/nodes/register") {
		t.Fatalf("nothing registered, so this test proved nothing:\n%s", sent)
	}
	// Everything the preflight can see, and none of it may appear.
	for _, secret := range []string{
		"SENTINELGPUMODELQX9",  // the GPU model
		"SENTINELNOTEQX9",      // an undetermined note
		"/sentineldiskpathqx9", // the filesystem path that was measured
		"65531", "131062",      // per-GPU and total VRAM
		"524269", // system RAM
		"999983", // free disk
		"4093",   // core count
	} {
		if strings.Contains(sent, secret) {
			t.Errorf("the node transmitted %q, which is local-only hardware detail. "+
				"docs/relay-selection-design.md §4.1: supply-side capability may not be self-declared, "+
				"and the preflight is only allowed to read this much BECAUSE it sends none of it.\n%s",
				secret, sent)
		}
	}
	// The one value that IS allowed out, so this test also proves the share still works
	// rather than merely proving it sent nothing.
	if !strings.Contains(sent, `"hw":"multi-gpu"`) {
		t.Errorf("the privacy bucket did not reach the broker; the share is not doing its job:\n%s", sent)
	}
}

// TestTheBucketIsTheOnlyHardwareFieldOnTheWire is the same claim from the other side. The
// four bucket values are a closed set, and a registration carrying anything else in `hw`
// would mean some richer string had found a path to the field.
func TestTheBucketIsTheOnlyHardwareFieldOnTheWire(t *testing.T) {
	useTempConfig(t)
	stubPreflight(t, sentinelBox())
	srv, registrations := captureRegisterBroker(t)
	realAgentStart(t)

	if err := runShare(t, config{Broker: srv.URL, User: "op"},
		[]string{"m1", "--upstream", "http://127.0.0.1:1234/v1"}); err != nil {
		t.Fatalf("cmdShare = %v, want nil", err)
	}
	regs := registrations()
	if len(regs) == 0 {
		t.Fatal("no /nodes/register arrived at the broker")
	}
	allowed := map[string]bool{
		detect.HWMultiGPU: true, detect.HWSingleGPU: true,
		detect.HWApple: true, detect.HWCPU: true, detect.HWUnknown: true, "": true,
	}
	for _, reg := range regs {
		if !allowed[reg.HW] {
			t.Errorf("registered hw = %q, which is not one of the privacy buckets", reg.HW)
		}
	}
}
