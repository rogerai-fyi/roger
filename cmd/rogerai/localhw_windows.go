//go:build windows

package main

// diskFreeMiB reports free space on the filesystem holding path - except on Windows,
// where it reports that it cannot.
//
// The Go standard library exposes no free-space call on Windows; the answer lives behind
// GetDiskFreeSpaceExW in kernel32, reachable only through a hand-rolled DLL binding or a
// dependency this module does not carry. Writing that binding blind, on a platform this
// tree has no way to exercise, would produce a number nobody has ever seen be right - and
// a wrong disk figure in a report whose entire purpose is to be trustworthy is worse than
// an honest gap. So Windows reports "could not determine", the preflight verdict degrades
// to INCOMPLETE rather than passing or failing on it, and the report says which value is
// missing and why.
func diskFreeMiB(string) (int, bool) { return 0, false }
