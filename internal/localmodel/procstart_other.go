//go:build !linux

package localmodel

import "time"

// readProcStart can't tell a process's start time here; a live pid is believed.
func readProcStart(int) (time.Time, bool, bool) { return time.Time{}, false, false }
