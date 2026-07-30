package main

import (
	"os"
	"testing"
)

func TestAgentTimeoutConfig(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"unlimited", 0},
		{"off", 0},
		{"0", 0},
		{"10m", 600},
		{"2h", 7200},
	} {
		got, err := parseAgentTimeout(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("parseAgentTimeout(%q) = %d, %v; want %d", tc.raw, got, err, tc.want)
		}
	}
	for _, bad := range []string{"banana", "-1s", "500ms"} {
		if _, err := parseAgentTimeout(bad); err == nil {
			t.Errorf("parseAgentTimeout(%q) should fail", bad)
		}
	}
}

func TestApplyAgentTimeoutDefaultRespectsEnvironment(t *testing.T) {
	old, had := os.LookupEnv(agentTimeoutEnv)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(agentTimeoutEnv, old)
		} else {
			_ = os.Unsetenv(agentTimeoutEnv)
		}
	})
	_ = os.Unsetenv(agentTimeoutEnv)
	applyAgentTimeoutDefault(600)
	if got := os.Getenv(agentTimeoutEnv); got != "600s" {
		t.Fatalf("configured timeout env = %q", got)
	}
	_ = os.Setenv(agentTimeoutEnv, "unlimited")
	applyAgentTimeoutDefault(60)
	if got := os.Getenv(agentTimeoutEnv); got != "unlimited" {
		t.Fatalf("explicit timeout override replaced: %q", got)
	}
}
