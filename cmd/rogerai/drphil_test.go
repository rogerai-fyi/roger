package main

import "testing"

// TestParseDrPhilFlags locks `roger drphil` flag parsing: defaults off, --fix / --json
// set, and an unknown flag errors (rather than silently running a destructive auto-fix).
func TestParseDrPhilFlags(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantFix  bool
		wantJSON bool
		wantErr  bool
	}{
		{name: "defaults", args: nil},
		{name: "fix", args: []string{"--fix"}, wantFix: true},
		{name: "json", args: []string{"--json"}, wantJSON: true},
		{name: "both", args: []string{"--fix", "--json"}, wantFix: true, wantJSON: true},
		{name: "unknown", args: []string{"--wat"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseDrPhilFlags(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error for unknown flag")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if opts.fix != tc.wantFix || opts.jsonOut != tc.wantJSON {
				t.Fatalf("got fix=%v json=%v, want fix=%v json=%v", opts.fix, opts.jsonOut, tc.wantFix, tc.wantJSON)
			}
		})
	}
}

// TestValidBrokerURL locks the broker-URL sanity check drphil's auto-fix keys on.
func TestValidBrokerURL(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"https://broker.rogerai.fyi", true},
		{"http://127.0.0.1:7070", true},
		{"", false},
		{"not-a-url", false},
		{"ftp://x", false},
		{"https://", false},
	} {
		if got := validBrokerURL(tc.in); got != tc.want {
			t.Errorf("validBrokerURL(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
