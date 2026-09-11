package edge

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"rogerai.fm/roger/v6/internal/localplane"
)

// Advert is what a node broadcasts about itself on the LAN, and it is A HINT AND ONLY A
// HINT. Anyone on the network can send one of these claiming any id, any account and
// any capability; nothing in it is believed until the certificate at Addr() is dialed,
// hashed, and found to match Fingerprint. It carries exactly the fields a peer needs in
// order to go and check - and nothing that would be worth stealing if it did not.
type Advert struct {
	NodeID      string
	Account     string // empty on an unenrolled instance: a candidate, never a member
	Kind        string
	Caps        []string
	Host        string // the mDNS host label the A record answers for
	IP          net.IP
	Port        int
	Fingerprint string // SHA-256 of the certificate this node will present, lowercase hex
}

// TXT keys. Short, because a TXT character-string is at most 255 bytes.
const (
	txtNodeID = "id"
	txtAcct   = "acct"
	txtKind   = "kind"
	txtCaps   = "caps"
	txtPort   = "port"
	txtFP     = "fp"
)

// TXT renders the advertisement's TXT record.
//
// There is deliberately no key here for a token, a bearer secret or a key of any kind.
// mDNS is broadcast in clear to every device on the network, including the ones you did
// not install: the only credential in this exchange is the certificate, and a
// certificate is public by nature.
func (a Advert) TXT() []string {
	out := []string{
		txtNodeID + "=" + a.NodeID,
		txtAcct + "=" + a.Account,
		txtKind + "=" + a.Kind,
		txtCaps + "=" + strings.Join(a.Caps, ","),
		txtPort + "=" + strconv.Itoa(a.Port),
		txtFP + "=" + a.Fingerprint,
	}
	return out
}

// advertFromTXT reads the TXT half of a record. Unknown keys are ignored and a missing
// key simply leaves its field empty - a peer whose record we cannot read is refused by
// the dial that follows, not by a parse error here.
func advertFromTXT(txt []string) Advert {
	var a Advert
	for _, kv := range txt {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch k {
		case txtNodeID:
			a.NodeID = v
		case txtAcct:
			a.Account = v
		case txtKind:
			a.Kind = v
		case txtCaps:
			if v != "" {
				a.Caps = strings.Split(v, ",")
			}
		case txtPort:
			a.Port, _ = strconv.Atoi(v)
		case txtFP:
			a.Fingerprint = strings.ToLower(v)
		}
	}
	return a
}

// Addr is where a peer should dial to check this advertisement's claim.
func (a Advert) Addr() string {
	if a.IP == nil || a.Port <= 0 {
		return ""
	}
	return net.JoinHostPort(a.IP.String(), strconv.Itoa(a.Port))
}

// usable reports whether an advertisement is worth a dial at all. A record missing the
// id, the fingerprint or somewhere to dial cannot be checked, and an unverifiable hint
// is not a lead, it is noise.
func (a Advert) usable() error {
	switch {
	case a.NodeID == "":
		return fmt.Errorf("advertisement names no node")
	case a.Fingerprint == "":
		return fmt.Errorf("advertisement carries no certificate fingerprint")
	case a.Addr() == "":
		return fmt.Errorf("advertisement gives nowhere to dial")
	}
	return nil
}

// AdvertiseAddrs picks the addresses discovery may bind and advertise on: loopback and
// the RFC1918 / IPv6-ULA private ranges, and nothing else.
//
// The rule is not re-implemented here. internal/localplane already decides what counts
// as a private bind for the consumer plane, including the deliberate absence of
// link-local 169.254/16 (where cloud instance metadata lives, and where a
// self-assigned address means the network did not work). One rule, one place: if the
// plane's notion of "private" ever changes, discovery changes with it.
func AdvertiseAddrs(addrs []net.Addr) []net.IP {
	var out []net.IP
	for _, a := range addrs {
		ip := addrIP(a)
		if ip == nil || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			continue
		}
		if localplane.IsPrivateAddr(ip) {
			out = append(out, ip)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// HasLAN reports whether any of these addresses is a real local network rather than
// this machine talking to itself. Loopback alone is not a LAN: there is nothing out
// there to discover, and saying so once is more useful than browsing an empty group
// every interval forever.
func HasLAN(addrs []net.Addr) bool {
	for _, ip := range AdvertiseAddrs(addrs) {
		if !ip.IsLoopback() {
			return true
		}
	}
	return false
}

// addrIP pulls the IP out of the shapes net.Interface.Addrs returns.
func addrIP(a net.Addr) net.IP {
	switch v := a.(type) {
	case *net.IPNet:
		return v.IP
	case *net.IPAddr:
		return v.IP
	case *net.UDPAddr:
		return v.IP
	}
	return nil
}

// InterfaceAddrs flattens a set of interfaces to their addresses, skipping the ones
// that are down.
func InterfaceAddrs(ifaces []net.Interface) []net.Addr {
	var out []net.Addr
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		out = append(out, addrs...)
	}
	return out
}
