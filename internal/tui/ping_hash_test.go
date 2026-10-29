package tui

// The world's mixer is pinned BY VALUE, not just by compiling. It was rewritten on 2026-09-29 so
// roger would build for a 32-bit board at all (the multiplier does not fit in an int there), and the
// rewrite must not change a single star: the placement of every star, its twinkle and every
// wanderer's spawn in Ping World comes out of this function, so a different value is a different sky.
//
// The expected values below were computed by RUNNING THE OLD IMPLEMENTATION, kept verbatim in the
// comment, rather than by reading them off the new one - a golden value taken from the code it is
// meant to check proves nothing.
//
//	func old(x int) uint32 {
//		z := uint32(x)*2654435761 + 0x9e3779b9
//		z ^= z >> 15
//		z *= 0x85ebca6b
//		z ^= z >> 13
//		return z
//	}
//	func oldWorld(a, b, seed int) uint32 { return old(a*7349 + b*916703 + seed*2654435761) }

import "testing"

func TestTheWorldMixerIsUnchangedByThe32BitFix(t *testing.T) {
	for _, c := range []struct {
		in   int
		want uint32
	}{
		{0, 0xc321cfe3},
		{1, 0xd0694803},
		{42, 0xa6c292b6},
		{99999, 0xc0b2dfdb},
	} {
		if got := pingHash(c.in); got != c.want {
			t.Errorf("pingHash(%d) = %#08x, want %#08x - the sky changed shape", c.in, got, c.want)
		}
		if got := pingHash32(uint32(c.in)); got != c.want {
			t.Errorf("pingHash32(%d) = %#08x, want %#08x", c.in, got, c.want)
		}
	}
	// The world hash mixes all three of its inputs, through the same arithmetic, to the same answer.
	if got, want := worldHash(3, 4, 7), uint32(0xda25f4ea); got != want {
		t.Errorf("worldHash(3,4,7) = %#08x, want %#08x", got, want)
	}
	if got, want := worldHash(0, 0, 0), uint32(0xc321cfe3); got != want {
		t.Errorf("worldHash(0,0,0) = %#08x, want %#08x", got, want)
	}
	// And it does not collapse its inputs into a handful of values.
	seen := map[uint32]bool{}
	for a := 0; a < 8; a++ {
		for b := 0; b < 8; b++ {
			seen[worldHash(a, b, 7)] = true
		}
	}
	if len(seen) < 60 {
		t.Errorf("worldHash collapsed 64 inputs into %d values", len(seen))
	}
}
