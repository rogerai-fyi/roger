package onair

import (
	"fmt"
	"os"
	"path/filepath"
)

// Tower is a running standalone Tower's local record: which data directory this user's
// `roger tower serve` holds, under what name and where it serves, so this user's TUI and console
// can show the Towers they run and reach each one's owner-only admin socket in that directory
// (features/join/surfaces.feature). Local and owner-only like a share record; it holds no secret.
type Tower struct {
	PID   int    `json:"pid"`
	Dir   string `json:"dir"`
	Label string `json:"label"`
	Addr  string `json:"addr"`
}

// TowersDir is where Tower records live, beside the share records.
func TowersDir() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "rogerai", "towers-serving")
}

// TowerFile is the record of the serve process pid.
func TowerFile(pid int) string {
	return filepath.Join(TowersDir(), fmt.Sprintf("tower-%d.json", pid))
}

// WriteTower records t and returns the remover serve runs when it stops.
func WriteTower(t Tower) (release func(), err error) {
	return writeRecord(TowersDir(), TowerFile(t.PID), t)
}

// Towers lists the Towers this user is serving now; a record whose process is gone is removed.
func Towers() []Tower {
	return liveRecords(filepath.Join(TowersDir(), "tower-*.json"), func(t Tower) (int, bool) {
		return t.PID, t.PID > 0 && t.Dir != ""
	})
}
