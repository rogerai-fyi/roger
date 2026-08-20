// Package clockprobe measures how far this machine's clock is from real time.
//
// It is one SNTP round trip and nothing else. It is its own package rather than a function
// in internal/tower because that package is under a Phase 1 isolation gate -
// TestStandaloneHasNoOutboundNetworkCallAtAll reads its source and fails if any file in it
// acquires the ability to reach the network - and a standalone Tower's promise that it
// makes no outbound connection has to stay a proof rather than becoming a promise with an
// exception in it. So the dialer sits out here, tower holds only the ClockSource function
// type, and `roger-tower doctor` decides whether to join them.
package clockprobe

import (
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// DefaultServer is the reference doctor measures against. It is an anycast public NTP
// service rather than one of ours, deliberately: the question is whether this clock agrees
// with the world, and asking our own infrastructure would answer a narrower question and
// would make the check fail whenever we did.
const DefaultServer = "time.cloudflare.com:123"

// NTP returns a time source backed by a single SNTP round trip. It is deliberately the
// smallest correct thing rather than a full NTP client - one exchange, no filtering, no
// discipline - because doctor needs to know whether this clock is minutes out, not
// microseconds, and a client that could tell the difference would be a project.
func NTP(server string, timeout time.Duration) func() (time.Time, string, error) {
	return func() (time.Time, string, error) {
		now, err := sntpQuery(server, timeout)
		return now, "NTP " + server, err
	}
}

// sntpQuery performs one SNTP exchange and returns what the server says the time is,
// corrected for the round trip.
//
// The correction is the standard NTP offset formula over the four timestamps - local send
// (t1), server receive (t2), server transmit (t3), local receive (t4) - which cancels a
// symmetric network delay. On an asymmetric path it is wrong by half the asymmetry, which
// at the scale doctor cares about (seconds, not milliseconds) does not matter and is worth
// saying out loud rather than implying a precision the method does not have.
func sntpQuery(server string, timeout time.Duration) (time.Time, error) {
	conn, err := net.DialTimeout("udp", server, timeout)
	if err != nil {
		return time.Time{}, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return time.Time{}, err
	}
	// LI=0, VN=3, Mode=3 (client). Everything else zero: a client request carries no
	// timestamps the server needs.
	req := make([]byte, 48)
	req[0] = 0x1b
	t1 := time.Now()
	if _, err := conn.Write(req); err != nil {
		return time.Time{}, err
	}
	resp := make([]byte, 48)
	if _, err := conn.Read(resp); err != nil {
		return time.Time{}, err
	}
	t4 := time.Now()
	t2, t3 := ntpTimestamp(resp[32:40]), ntpTimestamp(resp[40:48])
	if t3.IsZero() {
		return time.Time{}, fmt.Errorf("NTP reply carried no transmit timestamp")
	}
	// The offset of the SERVER's clock relative to ours, applied to our own receive time
	// to get "what time it really is".
	offset := (t2.Sub(t1) + t3.Sub(t4)) / 2
	return t4.Add(offset), nil
}

// ntpEpochOffset is the gap between the NTP epoch (1 Jan 1900) and the Unix epoch, in
// seconds. Named because a bare 2208988800 in the middle of a shift is unreadable.
const ntpEpochOffset = 2208988800

// ntpTimestamp decodes a 64-bit NTP timestamp: seconds since 1900 in the high word, a
// binary fraction of a second in the low word. A zero field means the server did not fill
// it in, which is reported as the zero time rather than as 1900.
func ntpTimestamp(b []byte) time.Time {
	sec := binary.BigEndian.Uint32(b[0:4])
	frac := binary.BigEndian.Uint32(b[4:8])
	if sec == 0 && frac == 0 {
		return time.Time{}
	}
	nsec := (int64(frac) * int64(time.Second)) >> 32
	return time.Unix(int64(sec)-ntpEpochOffset, nsec)
}
