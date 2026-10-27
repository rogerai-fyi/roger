//go:build unix

package main

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestConcurrentSetLimitsAllLand: limit edits from separate roger processes at once all land;
// each reads config.json inside the lock it writes under, so none overwrites another's edit.
func TestConcurrentSetLimitsAllLand(t *testing.T) {
	useTempConfig(t)
	require.NoError(t, cmdSetLimit([]string{"default", "--max-out", "9"})) // config.json exists
	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestSetLimitChildHelper$", "-test.count=1")
			cmd.Env = append(os.Environ(), fmt.Sprintf("ROGER_SETLIMIT_CHILD=model-%d", i))
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, string(out))
		}(i)
	}
	wg.Wait()
	c := loadConfig()
	for i := 0; i < n; i++ {
		_, ok := c.Limits.Models[fmt.Sprintf("model-%d", i)]
		require.True(t, ok, "model-%d's limit was lost to a concurrent write", i)
	}
}

// TestSetLimitChildHelper is the child half: one set-limit, then exit. Nothing in a normal run.
func TestSetLimitChildHelper(t *testing.T) {
	m := os.Getenv("ROGER_SETLIMIT_CHILD")
	if m == "" {
		return
	}
	if err := cmdSetLimit([]string{m, "--max-out", "1"}); err != nil {
		t.Fatal(err)
	}
}
