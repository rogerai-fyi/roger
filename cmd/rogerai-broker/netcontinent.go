package main

// netcontinent.go: the region cross-check (contract §14.B7 #21, founder ruling 2026-10-05).
// A station's declared region is checked against the continent its connecting address falls in,
// using a network-to-continent table the OPERATOR supplies (ROGERAI_NET_CONTINENTS: a text file
// of "CIDR CONTINENT" lines, # comments). The code ships only this loader. With no table
// configured nothing is ever contradicted and region stays declared only.
//
// The continent is stamped on the registration at register time (broker-set, outside the node's
// signed bytes), so it travels with the shared registry mirror and every instance judges the
// same station the same way. Contradiction is computed when read, from region, continent and the
// curated flag. Neither the address nor its continent is ever emitted.

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"

	"rogerai.fm/roger/v7/internal/protocol"
)

// continents is the vocabulary a table may use.
var continents = map[string]bool{"AF": true, "AN": true, "AS": true, "EU": true, "NA": true, "OC": true, "SA": true}

type netRange struct {
	prefix    netip.Prefix
	continent string
}

// netTable is a parsed table. Lookups go by prefix length, longest first: one map read per
// length present (at most 33 for IPv4 and 129 for IPv6), however many ranges the table holds.
// A table built from the registries' data has about 170,000 ranges.
type netTable struct {
	ranges []netRange
	byBits map[int]map[netip.Prefix]string
	bits   []int // the prefix lengths present, longest first
}

func parseNetTable(raw []byte) (*netTable, error) {
	t := &netTable{}
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 {
			return nil, fmt.Errorf("line %d: want \"CIDR CONTINENT\"", i+1)
		}
		p, err := netip.ParsePrefix(f[0])
		if err != nil {
			return nil, fmt.Errorf("line %d: %v", i+1, err)
		}
		c := strings.ToUpper(f[1])
		if !continents[c] {
			return nil, fmt.Errorf("line %d: %q is not a continent code", i+1, f[1])
		}
		t.ranges = append(t.ranges, netRange{prefix: p.Masked(), continent: c})
	}
	sort.SliceStable(t.ranges, func(i, j int) bool { return t.ranges[i].prefix.Bits() > t.ranges[j].prefix.Bits() })
	t.byBits = map[int]map[netip.Prefix]string{}
	for _, r := range t.ranges {
		bits := r.prefix.Bits()
		if r.prefix.Addr().Is4() {
			bits += 1000 // keep v4 and v6 lengths apart
		}
		if t.byBits[bits] == nil {
			t.byBits[bits] = map[netip.Prefix]string{}
			t.bits = append(t.bits, bits)
		}
		if _, dup := t.byBits[bits][r.prefix]; !dup {
			t.byBits[bits][r.prefix] = r.continent // the first line for a prefix wins, as the scan did
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(t.bits)))
	return t, nil
}

// netTableMaxBytes bounds the table read (a var so a test can lower it).
var netTableMaxBytes int64 = 64 << 20

// loadNetTable reads the table at path ("" = none configured: nil, no error).
func loadNetTable(path string) (*netTable, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, netTableMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > netTableMaxBytes {
		return nil, fmt.Errorf("the network table is larger than %d bytes", netTableMaxBytes)
	}
	return parseNetTable(raw)
}

// loadNetTableFromEnv loads ROGERAI_NET_CONTINENTS. A table that cannot be read or parsed is
// logged, reported on /admin/live (regionTableStatus) and ignored: the check fails open to
// declared-only, never to refusing stations.
func loadNetTableFromEnv() (*netTable, string) {
	path := os.Getenv("ROGERAI_NET_CONTINENTS")
	t, err := loadNetTable(path)
	if err != nil {
		log.Printf("network table %s unusable (%v): region stays declared only", path, err)
		return nil, err.Error()
	}
	return t, ""
}

// regionTableStatus is what /admin/live shows of the region table: whether one is loaded, its
// size, and why a configured one was refused.
func (b *broker) regionTableStatus() map[string]any {
	st := map[string]any{"configured": b.netTable != nil, "ranges": 0}
	if b.netTable != nil {
		st["ranges"] = len(b.netTable.ranges)
	}
	if b.netTableErr != "" {
		st["error"] = b.netTableErr
	}
	return st
}

// regionMismatches lists the stations whose declared region their network contradicts (ids only).
func (b *broker) regionMismatches() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []string{}
	for id, reg := range b.nodes {
		if regionContradicted(reg) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// continentOf is the continent addr falls in ("" = no table, no match, or not an address):
// the longest matching prefix, found by one map read per prefix length.
func (t *netTable) continentOf(addr string) string {
	if t == nil {
		return ""
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return ""
	}
	a = a.Unmap()
	for _, bits := range t.bits {
		n := bits
		if a.Is4() != (bits >= 1000) {
			continue
		}
		if n >= 1000 {
			n -= 1000
		}
		p, err := a.Prefix(n)
		if err != nil {
			continue
		}
		if c, ok := t.byBits[bits][p]; ok {
			return c
		}
	}
	return ""
}

// regionContinent maps a declared region to its continent ("" = no continent: never
// contradicted). Regions are free-form, so only tokens that name one continent without doubt
// map; anything ambiguous ("ap-southeast-2" is in Oceania, "america" is two continents) maps to
// nothing, because an honest station must never be contradicted.
func regionContinent(region string) string {
	r := strings.ToLower(strings.TrimSpace(region))
	if r == "" {
		return ""
	}
	head, _, _ := strings.Cut(r, "-")
	switch head {
	case "eu", "europe", "uk", "gb", "de", "fr", "nl":
		return "EU"
	case "us", "northamerica":
		return "NA"
	case "asia", "jp", "sg", "kr", "cn", "hk", "tw":
		return "AS"
	case "br", "southamerica":
		return "SA"
	case "af", "africa", "za":
		return "AF"
	case "oc", "oceania", "au", "nz":
		return "OC"
	}
	return ""
}

// regionContradicted reports whether a station's declared region names a different continent
// than its network maps to. A curated station's region is its provider's name: never contradicted.
func regionContradicted(reg protocol.NodeRegistration) bool {
	if reg.Curated || reg.NetContinent == "" {
		return false
	}
	rc := regionContinent(reg.Region)
	return rc != "" && rc != reg.NetContinent
}

// stampNetContinent sets the registration's network continent from the address it connected
// from (always broker-set: whatever the node sent is replaced).
func (b *broker) stampNetContinent(reg *protocol.NodeRegistration, addr string) {
	reg.NetContinent = b.netTable.continentOf(addr)
}
