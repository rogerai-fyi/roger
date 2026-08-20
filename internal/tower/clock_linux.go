//go:build linux

package tower

import "syscall"

// clockDisciplined asks the KERNEL whether anything is keeping this clock right, which is
// a stronger question than "is ntpd installed" and a much stronger one than "does the time
// look plausible". adjtimex reports the state of the kernel's own time discipline: a
// daemon that is actually steering the clock clears STA_UNSYNC, and one that is installed
// but not working does not.
//
// This is why the check is worth having next to the measured offset. An offset says the
// clock is wrong now; the discipline bit says whether it will still be wrong tomorrow, and
// it is the half that turns into an instruction an operator can carry out.
//
// staUnsync is spelled out rather than taken from the syscall package because the standard
// library does not export the STA_* constants on any platform. Its value is fixed ABI
// (linux/timex.h) and cannot change without breaking every program that reads it.
const staUnsync = 0x0040

func clockDisciplined() (disciplined, known bool, note string) {
	var t syscall.Timex
	if _, err := syscall.Adjtimex(&t); err != nil {
		return false, false, "adjtimex(2) failed: " + err.Error()
	}
	if t.Status&staUnsync != 0 {
		return false, true, "the kernel reports STA_UNSYNC. Repair: enable a time daemon - " +
			"`timedatectl set-ntp true`, or install chrony/ntpsec and start it"
	}
	return true, true, ""
}
