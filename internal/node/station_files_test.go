package node

// features/models/short_names_nearby.feature "a station on a pinned Tower reports what it serves, as
// its own word": a station started from the TUI or the console in LAN mode reports the files roger
// serves, the same as `roger share` does.

import (
	"testing"

	"rogerai.fm/roger/v7/internal/agent"
	"rogerai.fm/roger/v7/internal/servedfile"
)

func TestALANStationFromAFrontEndReportsItsFiles(t *testing.T) {
	isolateConfig(t)
	want := map[string]servedfile.File{"tiny": {Repo: "acme/Tiny-GGUF", File: "Tiny-Q4_K_M.gguf", SHA256: "x"}}
	var got agent.Config
	old := startLANAgent
	t.Cleanup(func() { startLANAgent = old })
	startLANAgent = func(cfg agent.Config, _ []string) *agent.Session { got = cfg; return nil }
	c := New(Config{Station: "kx1", StationFiles: func() map[string]servedfile.File { return want }})
	c.mu.Lock()
	_, err := c.startLocked(ShareRow{Model: "tiny", Upstream: "http://127.0.0.1:1"}, Pricing{}, false, []string{"10.0.0.2:8787"})
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got.StationFiles == nil || got.StationFiles()["tiny"].Repo != "acme/Tiny-GGUF" {
		t.Fatal("the LAN station does not report its files")
	}
	c.releaseLockLocked("tiny")
}
