package clockprobe

import (
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeNTP is a UDP server that answers every request as if its clock were `off` away from
// ours, so the whole exchange - packet layout, epoch conversion, round-trip correction -
// is exercised against a known answer rather than against the internet.
func fakeNTP(t *testing.T, off time.Duration) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 48)
		for {
			_, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			now := time.Now().Add(off)
			resp := make([]byte, 48)
			resp[0] = 0x1c                 // LI=0, VN=3, Mode=4 (server)
			writeNTPTime(resp[32:40], now) // receive
			writeNTPTime(resp[40:48], now) // transmit
			pc.WriteTo(resp, addr)
		}
	}()
	return pc.LocalAddr().String()
}

func writeNTPTime(b []byte, t time.Time) {
	sec := uint32(t.Unix() + ntpEpochOffset)
	frac := uint32((int64(t.Nanosecond()) << 32) / int64(time.Second))
	binary.BigEndian.PutUint32(b[0:4], sec)
	binary.BigEndian.PutUint32(b[4:8], frac)
}

// TestNTPReportsTheServersTime end to end, at a skew big enough that the round trip cannot
// account for it. The tolerance is generous on purpose: this probe is here to catch a clock
// that is minutes out, and pinning it tighter would be pinning the test machine's
// scheduling rather than the code.
func TestNTPReportsTheServersTime(t *testing.T) {
	for _, off := range []time.Duration{7 * time.Minute, -7 * time.Minute} {
		addr := fakeNTP(t, off)
		now, ref, err := NTP(addr, 2*time.Second)()
		require.NoError(t, err)
		require.Contains(t, ref, addr, "the reference must name what was asked")
		got := time.Until(now)
		require.InDelta(t, off.Seconds(), got.Seconds(), 1.0,
			"reported real time is %s away, want ~%s", got, off)
	}
}

// TestNTPFailsCleanlyWhenNothingAnswers. A blocked or absent server has to come back as an
// error, because the caller reports an error as "not checked" and a zero time as "your
// clock is off by a century".
func TestNTPFailsCleanlyWhenNothingAnswers(t *testing.T) {
	// A port nothing is listening on: the kernel answers with ICMP unreachable, so this is
	// fast rather than a two-second wait.
	_, _, err := NTP("127.0.0.1:1", 500*time.Millisecond)()
	require.Error(t, err)
}

// TestAnEmptyTransmitTimestampIsRefused. Some middleboxes and misconfigured servers answer
// with a well-formed packet whose timestamps are zero. Decoding that as a time would put
// the reference in 1900 and report the machine as a century ahead - a spectacular false
// alarm, and exactly the sort of confident wrong answer this whole check must not produce.
func TestAnEmptyTransmitTimestampIsRefused(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 48)
		_, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		pc.WriteTo(make([]byte, 48), addr) // all zeros
	}()

	_, _, err = NTP(pc.LocalAddr().String(), 2*time.Second)()
	require.Error(t, err)
	require.Contains(t, err.Error(), "no transmit timestamp")
}

// TestNTPTimestampDecoding pins the epoch shift and the binary fraction, which are the two
// places this is silently wrong by 70 years or by a factor of two.
func TestNTPTimestampDecoding(t *testing.T) {
	want := time.Unix(1700000000, 500*int64(time.Millisecond))
	b := make([]byte, 8)
	writeNTPTime(b, want)
	got := ntpTimestamp(b)
	require.WithinDuration(t, want, got, time.Millisecond)

	require.True(t, ntpTimestamp(make([]byte, 8)).IsZero(), "an all-zero timestamp must decode as unset, not as 1900")
}
