// Package placement decides where a Station is placed and when it should move: the pure
// parts (locality class, ranking, trust stage), with the broker supplying the inputs.
//
// Contracts: features/tower/placement_locality.feature, placement_ranking.feature,
// station_mobility.feature, tower_trust_ramp.feature.
package placement

// Locality is where Core observed a party to be: a coarse network bucket (an IPv4 /24 or an
// IPv6 /32) and a continent from the operator's table. Either may be empty (unknown). It is
// never taken from anything a Tower or a Station says about itself.
type Locality struct {
	Bucket    string
	Continent string
}

// Class is the locality relation between a Station and a Tower. Lower is better.
type Class int

const (
	SameNetwork Class = iota
	SameContinent
	Unknown
	Far
)

func (c Class) String() string {
	return [...]string{"same_network", "same_continent", "unknown", "far"}[c]
}

// Better reports whether c ranks strictly above o.
func (c Class) Better(o Class) bool { return c < o }

// ClassOf is the locality class between a Station and a Tower.
func ClassOf(station, tower Locality) Class {
	switch {
	case station.Bucket != "" && station.Bucket == tower.Bucket:
		return SameNetwork
	case station.Continent == "" || tower.Continent == "":
		return Unknown
	case station.Continent == tower.Continent:
		return SameContinent
	}
	return Far
}

// localBucket is what the broker's bucketer answers for loopback and private ranges.
const localBucket = "local"

// FromAddr is the locality of one observed address.
func FromAddr(addr string, bucketOf, continentOf func(string) string) Locality {
	b := bucketOf(addr)
	if b == "" || b == localBucket {
		return Locality{}
	}
	return Locality{Bucket: b, Continent: continentOf(addr)}
}

// FromAddrs is a Tower's locality from every address its hub host resolved to, cross-checked
// against the continent of the address its link connected from (linkContinent, "" = unknown).
// One bucket across all addresses keeps the bucket; one continent across all keeps the
// continent; anything mixed or unknown is dropped. A hub continent that contradicts the link's
// is unknown and reported as a conflict.
func FromAddrs(addrs []string, linkContinent string, bucketOf, continentOf func(string) string) (Locality, bool) {
	if len(addrs) == 0 {
		return Locality{}, false
	}
	var out Locality
	for i, a := range addrs {
		l := FromAddr(a, bucketOf, continentOf)
		if l.Bucket == "" {
			return Locality{}, false
		}
		if i == 0 {
			out = l
			continue
		}
		if l.Bucket != out.Bucket {
			out.Bucket = ""
		}
		if l.Continent != out.Continent {
			out.Continent = ""
		}
	}
	if linkContinent != "" && out.Continent != "" && linkContinent != out.Continent {
		return Locality{}, true
	}
	return out, false
}
