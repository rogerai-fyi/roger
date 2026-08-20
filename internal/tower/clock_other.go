//go:build !linux

package tower

// clockDisciplined has no honest answer outside Linux in this build.
//
// macOS keeps the equivalent state behind ntp_adjtime and the Windows one behind W32Time's
// service API; neither is reachable from the Go standard library, and inventing an answer
// for the check whose entire value is that an operator can act on it would be worse than
// admitting the gap. The measured offset still works everywhere, so a wrong clock is still
// FOUND on these platforms - what is missing is only the second half, whether anything is
// keeping it right.
func clockDisciplined() (disciplined, known bool, note string) {
	return false, false, "the kernel's time-sync state is not readable from this build outside Linux; " +
		"the measured offset below is still the real answer to whether this clock is wrong"
}
