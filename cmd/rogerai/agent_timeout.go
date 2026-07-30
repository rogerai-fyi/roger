package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const agentTimeoutEnv = "ROGERAI_AGENT_TIMEOUT"

// parseAgentTimeout accepts Go durations (10m, 2h) plus explicit unlimited values.
func parseAgentTimeout(raw string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "0", "off", "none", "unlimited":
		return 0, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d < time.Second {
		return 0, fmt.Errorf("invalid agent timeout %q - use a duration such as 10m, or unlimited", raw)
	}
	seconds := int(d / time.Second)
	if time.Duration(seconds)*time.Second != d {
		return 0, fmt.Errorf("invalid agent timeout %q - use whole seconds or larger", raw)
	}
	return seconds, nil
}

func formatAgentTimeout(seconds int) string {
	if seconds <= 0 {
		return "unlimited"
	}
	return (time.Duration(seconds) * time.Second).String()
}

// applyAgentTimeoutDefault seeds the TUI environment from persisted config while
// preserving an explicit per-run environment override.
func applyAgentTimeoutDefault(seconds int) {
	if _, explicit := os.LookupEnv(agentTimeoutEnv); explicit {
		return
	}
	if seconds > 0 {
		_ = os.Setenv(agentTimeoutEnv, strconv.Itoa(seconds)+"s")
	}
}
