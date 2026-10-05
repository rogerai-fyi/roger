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
	"log"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"

	"rogerai.fm/roger/v6/internal/protocol"
)

// continents is the vocabulary a table may use.
var continents = map[string]bool{"AF": true, "AN": true, "AS": true, "EU": true, "NA": true, "OC": true, "SA": true}

type netRange struct {
	prefix    netip.Prefix
	continent string
}

// netTable is a parsed table, longest prefix first.
type netTable struct{ ranges []netRange }

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
	return t, nil
}

// loadNetTable reads the table at path ("" = none configured: nil, no error).
func loadNetTable(path string) (*netTable, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseNetTable(raw)
}

// loadNetTableFromEnv loads ROGERAI_NET_CONTINENTS. A table that cannot be read or parsed is
// logged and ignored: the check fails open to declared-only, never to refusing stations.
func loadNetTableFromEnv() *netTable {
	path := os.Getenv("ROGERAI_NET_CONTINENTS")
	t, err := loadNetTable(path)
	if err != nil {
		log.Printf("network table %s unusable (%v): region stays declared only", path, err)
		return nil
	}
	return t
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

// continentOf is the continent addr falls in ("" = no table, no match, or not an address).
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
	for _, r := range t.ranges {
		if r.prefix.Contains(a) {
			return r.continent
		}
	}
	return ""
}

// regionContinent maps a declared region to its continent ("" = no continent: never
// contradicted). Regions are free-form; this knows the common spellings and prefixes.
func regionContinent(region string) string {
	r := strings.ToLower(strings.TrimSpace(region))
	if r == "" {
		return ""
	}
	head, _, _ := strings.Cut(r, "-")
	switch head {
	case "eu", "europe", "uk", "gb", "de", "fr", "nl":
		return "EU"
	case "us", "na", "ca", "northamerica", "america":
		return "NA"
	case "asia", "ap", "as", "jp", "sg", "in", "kr", "cn", "hk", "tw":
		return "AS"
	case "sa", "br", "southamerica":
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
