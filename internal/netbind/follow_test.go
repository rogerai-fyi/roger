package netbind

import (
	"net"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

// A wildcard listener follows the machine's addresses: one that appears starts answering on the
// same port within a rescan, and one that left stops answering.
func TestListenEachFollowsAddressChanges(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("binding arbitrary 127.0.0.0/8 addresses needs Linux")
	}
	prev := RescanEvery
	RescanEvery = 20 * time.Millisecond
	t.Cleanup(func() { RescanEvery = prev })
	var mu sync.Mutex
	have := []string{"127.0.0.11", "127.0.0.12"}
	addrs := func() ([]net.Addr, error) {
		mu.Lock()
		defer mu.Unlock()
		var out []net.Addr
		for _, a := range have {
			out = append(out, &net.IPNet{IP: net.ParseIP(a), Mask: net.CIDRMask(8, 32)})
		}
		return out, nil
	}
	ln, err := ListenEach(":0", addrs, func(net.IP) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	answers := func(ip string) bool {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, port), 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}
	if !answers("127.0.0.12") {
		t.Fatal("127.0.0.12 does not answer at the start")
	}
	mu.Lock()
	have = []string{"127.0.0.11", "127.0.0.13"} // .12 left, .13 arrived
	mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !(answers("127.0.0.13") && !answers("127.0.0.12")) {
		time.Sleep(10 * time.Millisecond)
	}
	if !answers("127.0.0.13") {
		t.Fatal("the address that arrived never answered")
	}
	if answers("127.0.0.12") {
		t.Fatal("the address that left still answers")
	}
	if !answers("127.0.0.11") {
		t.Fatal("the address that stayed stopped answering")
	}
}
