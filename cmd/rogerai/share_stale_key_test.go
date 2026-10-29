package main

// After a restart, the saved share upstream is gone and detection finds this machine's new serve,
// which has a NEW key. The saved key belongs to the saved endpoint only: carrying it onto the newly
// found one sent a stale bearer, so every peer request and every serving probe was refused and the
// serve stayed claimed on every member (found 2026-10-10, after a simulated Pi rebooted).

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/detect"
)

func TestASavedKeyIsNotCarriedOntoANewlyFoundUpstream(t *testing.T) {
	s := &lanShareBDD{t: t}
	s.reset()
	t.Cleanup(s.after)
	t.Setenv("ROGER_LAN", "1")
	up := completionUpstream(t, "self")
	c := loadConfig()
	c.Share = &Share{Upstream: "http://127.0.0.1:1/v1", UpstreamKey: "stale-key"}
	require.NoError(t, saveConfig(c))
	stubDetectProbeKey(t, func(string, string) (detect.Found, detect.Status) { return detect.Found{}, detect.Unreachable })
	stubDetectFull(t, []detect.Found{{Name: "roger", BaseURL: up.URL + "/v1", Chat: up.URL + "/v1/chat/completions",
		Key: "fresh-key", Models: []string{"qwen3-8b"}}}, nil)
	require.NoError(t, s.addTower("tower-a", true))
	s.runFor = 800 * time.Millisecond
	require.NoError(t, s.runShare("roger share"))
	got := loadConfig().Share
	require.Equal(t, up.URL+"/v1", got.Upstream, "the upstream found now is remembered:\n%s", s.out)
	require.Equal(t, "fresh-key", got.UpstreamKey, "with the key that upstream takes, not the old endpoint's:\n%s", s.out)
}
