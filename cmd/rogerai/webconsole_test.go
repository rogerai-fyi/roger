package main

import (
	"reflect"
	"testing"
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
