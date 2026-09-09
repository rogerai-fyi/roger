package protocol

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRetryAfterSeconds(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	cases := map[string]int{
		"":        0,
		"30":      30,
		" 30 ":    30,
		"0":       0,
		"-5":      0,
		"99999":   99999,
		"garbage": 0,
		"1.5":     0,
		now.Add(45 * time.Second).Format(http.TimeFormat):                      45,
		now.Add(45*time.Second + 300*time.Millisecond).Format(http.TimeFormat): 45, // the format is second-granular
		now.Add(-10 * time.Second).Format(http.TimeFormat):                     0,
	}
	for in, want := range cases {
		require.Equal(t, want, RetryAfterSeconds(in, now), "input %q", in)
	}
}

// TestJobResultRetryAfterWire: omitted when 0 (an old station's result decodes as before), present otherwise.
func TestJobResultRetryAfterWire(t *testing.T) {
	var legacy JobResult
	require.NoError(t, json.Unmarshal([]byte(`{"id":"j","status":429,"body":null,"receipt":{}}`), &legacy))
	require.Equal(t, 0, legacy.RetryAfterSec)
	wire, _ := json.Marshal(legacy)
	require.NotContains(t, string(wire), "retry_after_sec")
	wire, _ = json.Marshal(JobResult{ID: "j", Status: 429, RetryAfterSec: 7})
	require.Contains(t, string(wire), `"retry_after_sec":7`)
}
