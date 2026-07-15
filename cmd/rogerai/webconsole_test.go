package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/tui"
)

func TestStripWebuiFlags(t *testing.T) {
	tt := []struct {
		name        string
		args        []string
		inEnabled   bool
		wantRest    []string
		wantEnabled bool
		wantPort    string
	}{
		{"no flags keeps args + default", []string{"share", "gpt-oss"}, true, []string{"share", "gpt-oss"}, true, defaultWebuiPort},
		{"--no-webui disables, leaves command", []string{"--no-webui", "share"}, true, []string{"share"}, false, defaultWebuiPort},
		{"--webui forces on", []string{"--webui"}, false, nil, true, defaultWebuiPort},
		{"--webui-port overrides", []string{"--webui-port=5000"}, true, nil, true, "5000"},
		{"flags mixed with a command", []string{"share", "--no-webui", "--webui-port=4200"}, true, []string{"share"}, false, "4200"},
		{"bare no-flag run stays enabled", nil, true, nil, true, defaultWebuiPort},
	}
	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			rest, enabled, port := stripWebuiFlags(tc.args, tc.inEnabled, defaultWebuiPort)
			if !reflect.DeepEqual(rest, tc.wantRest) {
				t.Errorf("rest = %#v, want %#v", rest, tc.wantRest)
			}
			if enabled != tc.wantEnabled {
				t.Errorf("enabled = %v, want %v", enabled, tc.wantEnabled)
			}
			if port != tc.wantPort {
				t.Errorf("port = %q, want %q", port, tc.wantPort)
			}
		})
	}
}

// Founder spec (2026-07-14): launching roger must NOT auto-open the web console in a
// browser anymore (on some setups it opened inside the terminal and trapped the TUI).
// The console server still comes up (URL printed; `w` / /webui open it on demand); a
// saved `webui_open: true` opts back into the old auto-open.
func TestWebuiOpenDefaultOff(t *testing.T) {
	if (config{}).webuiOpenEnabled() {
		t.Error("browser auto-open must be OFF by default (WebuiOpen nil)")
	}
	on, off := true, false
	if !(config{WebuiOpen: &on}).webuiOpenEnabled() {
		t.Error("webui_open=true should auto-open")
	}
	if (config{WebuiOpen: &off}).webuiOpenEnabled() {
		t.Error("webui_open=false should not auto-open")
	}
}

// TestStartWebConsoleOpenGating: the console server binds and returns its tokenized URL
// either way; the browser open fires ONLY when the config opts in.
func TestStartWebConsoleOpenGating(t *testing.T) {
	origOpen := openBrowser
	var opened []string
	openBrowser = func(url string) { opened = append(opened, url) }
	t.Cleanup(func() { openBrowser = origOpen })

	ctrl := tui.NewController("http://broker.invalid", tui.Hooks{})
	url := startWebConsole(config{}, ctrl, "0")
	if url == "" || !strings.Contains(url, "127.0.0.1") {
		t.Fatalf("default run should still serve the console, url = %q", url)
	}
	if len(opened) != 0 {
		t.Fatalf("default run must NOT open a browser, opened %v", opened)
	}

	on := true
	url2 := startWebConsole(config{WebuiOpen: &on}, ctrl, "0")
	if len(opened) != 1 || opened[0] != url2 {
		t.Fatalf("webui_open=true should open the console URL once, opened %v url %q", opened, url2)
	}
}

// TestConfigWebuiOpenSetGet: the settings surface - `roger config set webui-open
// true|false` persists, junk errors, and the bare listing shows the state.
func TestConfigWebuiOpenSetGet(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := cmdConfig([]string{"set", "webui-open", "true"}); err != nil {
		t.Fatalf("set webui-open true: %v", err)
	}
	if !loadConfig().webuiOpenEnabled() {
		t.Fatal("set webui-open true did not persist")
	}
	if err := cmdConfig([]string{"set", "webui-open", "false"}); err != nil {
		t.Fatalf("set webui-open false: %v", err)
	}
	if loadConfig().webuiOpenEnabled() {
		t.Fatal("set webui-open false did not persist")
	}
	if err := cmdConfig([]string{"set", "webui-open", "sideways"}); err == nil {
		t.Fatal("junk value should error")
	}
	out := captureStdout(t, func() {
		if err := cmdConfig(nil); err != nil {
			t.Errorf("bare config: %v", err)
		}
	})
	if !strings.Contains(out, "webui-open") {
		t.Errorf("bare `roger config` should list webui-open, got:\n%s", out)
	}
	// `config get webui-open` answers too (audit finding: it was a silent no-output).
	out = captureStdout(t, func() {
		if err := cmdConfig([]string{"get", "webui-open"}); err != nil {
			t.Errorf("get webui-open: %v", err)
		}
	})
	if strings.TrimSpace(out) != "false" {
		t.Errorf("config get webui-open = %q, want false", strings.TrimSpace(out))
	}
}

func TestWebuiEnabledDefault(t *testing.T) {
	if !(config{}).webuiEnabled() {
		t.Error("webui should be ON by default (Webui nil)")
	}
	on, off := true, false
	if !(config{Webui: &on}).webuiEnabled() {
		t.Error("Webui=true should be on")
	}
	if (config{Webui: &off}).webuiEnabled() {
		t.Error("Webui=false should be off")
	}
}
