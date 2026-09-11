package edge

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
)

// A MINIMAL mDNS responder and browser, written here rather than taken from a library.
//
// THE DEPENDENCY DECISION (CLAUDE.md's minimisation rung, recorded where the code is):
// the alternatives were a pure-Go zeroconf/mDNS library (hashicorp/mdns, grandcat/
// zeroconf, brutella/dnssd) or golang.org/x/net/dns/dnsmessage for the codec alone.
// Both were rejected.
//
//   - A zeroconf LIBRARY brings a whole service-discovery framework - its own goroutine
//     and cache model, its own interface handling, its own idea of what a service
//     instance is - for the two message shapes we need. It also owns the browse loop,
//     which is exactly the seam this spec needs to control: an advertisement here is a
//     HINT, and the authority is a certificate we pin ourselves. None of the candidates
//     is actively maintained, and this repository settles money.
//   - golang.org/x/net/dns/dnsmessage is the codec the standard library itself vendors,
//     and would have been the natural pick - but it is not in this module's graph, and
//     `go get` of it also pulled x/crypto and x/sys forward. Moving the crypto module of
//     a money service sideways to avoid writing 200 lines of DNS encoding is the wrong
//     trade.
//
// So: no new module. What is here is only what `_rogerai._tcp` needs - PTR, SRV, TXT, A
// and AAAA, no compression on the way out, and a decoder that is hardened for a HOSTILE
// LAN, because every byte it reads came from an unauthenticated multicast group:
// bounded message size, bounded record count, bounded label and name length, and a
// pointer-hop cap with a strictly-backwards rule so a crafted compression loop cannot
// spin. FuzzParseMessage keeps it honest.

const (
	// mdnsGroupV4 / mdnsPort are the mDNS multicast group and port (RFC 6762).
	mdnsGroupV4 = "224.0.0.251"
	mdnsGroupV6 = "ff02::fb"
	mdnsPort    = 5353

	// DefaultService is the service Roger Edge nodes advertise.
	DefaultService = "_rogerai._tcp"

	// mdnsDomain is the mDNS local domain every name here sits under.
	mdnsDomain = "local"
)

// Hard bounds on anything read off the wire. A LAN peer is not trusted to be small.
const (
	maxMsgSize   = 9000 // a jumbo mDNS response; anything larger is refused outright
	maxName      = 255  // RFC 1035 name length
	maxLabel     = 63   // RFC 1035 label length
	maxPtrHops   = 16   // compression-pointer hops before we give up
	maxRecords   = 256  // questions + answers we will decode from ONE message
	maxTXTString = 255  // one TXT character-string
)

// DNS record types and classes, the handful this needs.
const (
	typeA    uint16 = 1
	typePTR  uint16 = 12
	typeTXT  uint16 = 16
	typeAAAA uint16 = 28
	typeSRV  uint16 = 33

	classIN         uint16 = 1
	classFlushIN    uint16 = 0x8001 // IN with the mDNS cache-flush bit
	classQuestionIN uint16 = 1
)

var errBadMessage = errors.New("malformed mDNS message")

// question is one query in a message.
type question struct {
	name string
	typ  uint16
}

// record is one resource record, decoded as far as this package cares.
type record struct {
	name string
	typ  uint16
	// PTR
	ptr string
	// SRV
	target string
	port   uint16
	// TXT
	txt []string
	// A / AAAA
	ip net.IP
}

// message is a decoded mDNS packet.
type message struct {
	response  bool
	questions []question
	answers   []record
}

// --- encoding -------------------------------------------------------------

// appendName writes a DNS name uncompressed. Refusing to compress on the way OUT costs
// a few dozen bytes on a packet that is already tiny and removes a whole class of
// encoder bug; the DECODER still understands compression, because other stacks use it.
func appendName(b []byte, name string) ([]byte, error) {
	name = strings.TrimSuffix(name, ".")
	total := 1
	if name != "" {
		for _, label := range strings.Split(name, ".") {
			if label == "" || len(label) > maxLabel {
				return nil, fmt.Errorf("%w: label %q", errBadMessage, label)
			}
			total += 1 + len(label)
			if total > maxName {
				return nil, fmt.Errorf("%w: name %q is too long", errBadMessage, name)
			}
			b = append(b, byte(len(label)))
			b = append(b, label...)
		}
	}
	return append(b, 0), nil
}

// appendRR writes one record header plus its already-encoded rdata.
func appendRR(b []byte, name string, typ, class uint16, ttl uint32, rdata []byte) ([]byte, error) {
	var err error
	if b, err = appendName(b, name); err != nil {
		return nil, err
	}
	b = binary.BigEndian.AppendUint16(b, typ)
	b = binary.BigEndian.AppendUint16(b, class)
	b = binary.BigEndian.AppendUint32(b, ttl)
	b = binary.BigEndian.AppendUint16(b, uint16(len(rdata)))
	return append(b, rdata...), nil
}

// buildQuery is the browse packet: one PTR question for the service.
func buildQuery(service string) ([]byte, error) {
	b := make([]byte, 12)
	binary.BigEndian.PutUint16(b[4:], 1) // one question
	var err error
	if b, err = appendName(b, service+"."+mdnsDomain); err != nil {
		return nil, err
	}
	b = binary.BigEndian.AppendUint16(b, typePTR)
	b = binary.BigEndian.AppendUint16(b, classQuestionIN)
	return b, nil
}

// buildResponse is the advertisement: PTR -> instance, then the instance's SRV, TXT and
// address records. One packet, so a browser learns everything from a single read.
func buildResponse(service, instance, host string, port uint16, txt []string, ips []net.IP) ([]byte, error) {
	const ttl = 120
	svc := service + "." + mdnsDomain
	inst := instance + "." + svc
	target := host + "." + mdnsDomain

	body := []byte{}
	var err error
	ptrData, err := appendName(nil, inst)
	if err != nil {
		return nil, err
	}
	if body, err = appendRR(body, svc, typePTR, classIN, ttl, ptrData); err != nil {
		return nil, err
	}
	srv := make([]byte, 6)
	binary.BigEndian.PutUint16(srv[4:], port) // priority 0, weight 0, then the port
	if srv, err = appendName(srv, target); err != nil {
		return nil, err
	}
	if body, err = appendRR(body, inst, typeSRV, classFlushIN, ttl, srv); err != nil {
		return nil, err
	}
	var txtData []byte
	for _, s := range txt {
		if len(s) > maxTXTString {
			return nil, fmt.Errorf("%w: TXT string is too long", errBadMessage)
		}
		txtData = append(txtData, byte(len(s)))
		txtData = append(txtData, s...)
	}
	if len(txtData) == 0 {
		txtData = []byte{0}
	}
	if body, err = appendRR(body, inst, typeTXT, classFlushIN, ttl, txtData); err != nil {
		return nil, err
	}
	answers := 3
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			if body, err = appendRR(body, target, typeA, classFlushIN, ttl, v4); err != nil {
				return nil, err
			}
		} else if v6 := ip.To16(); v6 != nil {
			if body, err = appendRR(body, target, typeAAAA, classFlushIN, ttl, v6); err != nil {
				return nil, err
			}
		} else {
			continue
		}
		answers++
	}
	head := make([]byte, 12)
	binary.BigEndian.PutUint16(head[2:], 0x8400) // response, authoritative
	binary.BigEndian.PutUint16(head[6:], uint16(answers))
	out := append(head, body...)
	if len(out) > maxMsgSize {
		return nil, fmt.Errorf("%w: advertisement is too large", errBadMessage)
	}
	return out, nil
}

// --- decoding (everything below reads UNTRUSTED bytes) --------------------

// decodeName reads a possibly-compressed name at off. It returns the name and the
// offset just past the name in the CURRENT stream (past the pointer, if one was taken).
//
// Two rules keep a hostile packet bounded: at most maxPtrHops pointers, and every
// pointer must point strictly BACKWARDS. Either one alone stops the classic
// self-referential-pointer spin; both are here because this is the single place a
// malformed LAN packet gets to choose how much work we do.
func decodeName(msg []byte, off int) (string, int, error) {
	var (
		labels []string
		hops   int
		next   = -1
		total  = 0
		cursor = off
		// A pointer must land STRICTLY before where the name started, and strictly
		// before every pointer already taken. That is what DNS compression actually
		// means (a back-reference to a name already written), and enforcing it is what
		// makes a crafted pointer chain finite.
		minJump = off
	)
	for {
		if cursor < 0 || cursor >= len(msg) {
			return "", 0, errBadMessage
		}
		n := int(msg[cursor])
		switch {
		case n == 0:
			cursor++
			if next < 0 {
				next = cursor
			}
			return strings.Join(labels, "."), next, nil
		case n&0xc0 == 0xc0:
			if cursor+1 >= len(msg) {
				return "", 0, errBadMessage
			}
			ptr := int(binary.BigEndian.Uint16(msg[cursor:]) & 0x3fff)
			if next < 0 {
				next = cursor + 2
			}
			hops++
			if hops > maxPtrHops || ptr >= minJump {
				return "", 0, errBadMessage // a loop, or a forward jump
			}
			minJump = ptr
			cursor = ptr
		case n&0xc0 != 0:
			return "", 0, errBadMessage // reserved label type
		default:
			if n > maxLabel || cursor+1+n > len(msg) {
				return "", 0, errBadMessage
			}
			total += n + 1
			if total > maxName {
				return "", 0, errBadMessage
			}
			labels = append(labels, string(msg[cursor+1:cursor+1+n]))
			cursor += 1 + n
		}
	}
}

// parseMessage decodes one mDNS packet. Anything it cannot make sense of is an error
// rather than a partial result: a half-understood advertisement is a hint we would then
// act on, and the whole posture here is that a hint must be cheap to throw away.
func parseMessage(buf []byte) (*message, error) {
	if len(buf) < 12 || len(buf) > maxMsgSize {
		return nil, errBadMessage
	}
	flags := binary.BigEndian.Uint16(buf[2:])
	qd := int(binary.BigEndian.Uint16(buf[4:]))
	an := int(binary.BigEndian.Uint16(buf[6:]))
	ns := int(binary.BigEndian.Uint16(buf[8:]))
	ar := int(binary.BigEndian.Uint16(buf[10:]))
	if qd+an+ns+ar > maxRecords {
		return nil, errBadMessage
	}
	m := &message{response: flags&0x8000 != 0}
	off := 12
	for i := 0; i < qd; i++ {
		name, next, err := decodeName(buf, off)
		if err != nil {
			return nil, err
		}
		off = next
		if off+4 > len(buf) {
			return nil, errBadMessage
		}
		m.questions = append(m.questions, question{name: name, typ: binary.BigEndian.Uint16(buf[off:])})
		off += 4
	}
	// Answers, authority and additional all carry records we are glad to have: a
	// responder that puts its A record in "additional" is being ordinary, not evasive.
	for i := 0; i < an+ns+ar; i++ {
		rec, next, err := decodeRecord(buf, off)
		if err != nil {
			return nil, err
		}
		off = next
		if rec != nil {
			m.answers = append(m.answers, *rec)
		}
	}
	return m, nil
}

// decodeRecord reads one RR. An unrecognised type is skipped, not refused: a LAN is full
// of records that are none of our business.
func decodeRecord(buf []byte, off int) (*record, int, error) {
	name, next, err := decodeName(buf, off)
	if err != nil {
		return nil, 0, err
	}
	off = next
	if off+10 > len(buf) {
		return nil, 0, errBadMessage
	}
	typ := binary.BigEndian.Uint16(buf[off:])
	rdlen := int(binary.BigEndian.Uint16(buf[off+8:]))
	off += 10
	if rdlen < 0 || off+rdlen > len(buf) {
		return nil, 0, errBadMessage
	}
	rdata := buf[off : off+rdlen]
	end := off + rdlen
	rec := &record{name: name, typ: typ}
	switch typ {
	case typePTR:
		ptr, _, err := decodeName(buf, off)
		if err != nil {
			return nil, 0, err
		}
		rec.ptr = ptr
	case typeSRV:
		if rdlen < 7 {
			return nil, 0, errBadMessage
		}
		rec.port = binary.BigEndian.Uint16(rdata[4:])
		target, _, err := decodeName(buf, off+6)
		if err != nil {
			return nil, 0, err
		}
		rec.target = target
	case typeTXT:
		for i := 0; i < len(rdata); {
			n := int(rdata[i])
			if i+1+n > len(rdata) {
				return nil, 0, errBadMessage
			}
			if n > 0 {
				rec.txt = append(rec.txt, string(rdata[i+1:i+1+n]))
			}
			i += 1 + n
		}
	case typeA:
		if rdlen != 4 {
			return nil, 0, errBadMessage
		}
		rec.ip = net.IP(append([]byte(nil), rdata...))
	case typeAAAA:
		if rdlen != 16 {
			return nil, 0, errBadMessage
		}
		rec.ip = net.IP(append([]byte(nil), rdata...))
	default:
		return nil, end, nil
	}
	return rec, end, nil
}
