package tui

import "testing"

// raw_reasoning_env_test.go pins the TUI booth's honoring of ROGERAI_REASONING_RAW (pre-push
// audit minor): liveProxyOpts must set ProxyOptions.ReasoningFallbackOff from the global env
// toggle, so exporting the var disables the reasoning->content fallback in the TUI just as it
// does for `roger use`. Default (env unset) leaves the fallback ON.
func TestLiveProxyOptsHonorsRawReasoningEnv(t *testing.T) {
	m := model{broker: "http://b", user: "u", proxyKey: "k"}
	o := offer{Model: "gpt-oss-120b"}
	var a alertBox

	t.Run("env set disables the fallback", func(t *testing.T) {
		t.Setenv("ROGERAI_REASONING_RAW", "1")
		if !m.liveProxyOpts(o, &a).ReasoningFallbackOff {
			t.Fatal("ROGERAI_REASONING_RAW=1 did not disable the reasoning fallback in the TUI booth")
		}
	})

	t.Run("env unset keeps the fallback on", func(t *testing.T) {
		t.Setenv("ROGERAI_REASONING_RAW", "")
		if m.liveProxyOpts(o, &a).ReasoningFallbackOff {
			t.Fatal("TUI booth disabled the reasoning fallback with the env unset (should default ON)")
		}
	})
}
