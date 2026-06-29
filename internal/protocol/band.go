package protocol

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Private bands ("frequency codes") code format + canonicalization. The full
// user-facing code looks like:
//
//	147.520 MHz · 8F3K-9M2Q
//
// where the "147.520 MHz" part is PURELY COSMETIC (radio flavor, NOT secret, NOT
// part of the key) and the 8-character Crockford-base32 tail ("8F3K-9M2Q", grouped
// 4-4 with a dash for readability) is the SECRET: 40 bits of entropy. The broker
// stores ONLY sha256(canonical tail); resolve hashes the tail alone. The cosmetic
// frequency is never folded into the key, so it can be regenerated/display-only.
//
// SECURITY: the full code (with the tail) is the SECRET, shown ONCE at mint for the
// owner to save. What is PERSISTED is a separate MASKED display - the same cosmetic
// frequency but with the tail replaced by maskedTail ("147.520 MHz · ••••-••••") - so
// the stored value carries NO secret and CanonicalBandTail can NEVER recover a tail
// from it (the band cannot be reconstructed/resolved from persisted state).

// crockfordAlphabet is Douglas Crockford's base32 alphabet: digits + uppercase
// letters with I, L, O, U removed (to avoid 1/I, 0/O confusion and an accidental
// profanity vowel). 32 symbols => 5 bits each => 8 symbols == 40 bits.
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// bandTailLen is the number of Crockford symbols in the secret tail (8 => 40 bits).
const bandTailLen = 8

// maskedTail is the placeholder shown in the PERSISTED cosmetic display in place of the
// secret tail. It contains NO Crockford symbols (the bullets/dash are all dropped by
// CanonicalBandTail), so a stored display canonicalizes to only the 6 frequency digits
// (< bandTailLen) and can therefore NEVER yield a resolvable tail.
const maskedTail = "••••-••••"

// bandSep separates the cosmetic frequency from the tail in BOTH the one-time code and
// the persisted display ("147.520 MHz · <tail>"). Defined once so NewBandCode (mint),
// MaskBandDisplay (the re-mask migration), and any split agree on the exact delimiter
// (a space + middot + space - the middot is dropped by CanonicalBandTail).
const bandSep = " · "

// NewBandCode mints a fresh frequency code. It returns THREE strings, separating the
// one-time SECRET from what is safe to persist:
//
//	code    - the full shareable code, e.g. "147.520 MHz · 8F3K-9M2Q". This carries the
//	          secret tail and resolves the band; it is shown ONCE at mint for the owner to
//	          save and is NEVER persisted.
//	display - a TRULY cosmetic, NON-RECOVERABLE display, e.g. "147.520 MHz · ••••-••••".
//	          Safe to persist + re-show: CanonicalBandTail(display) == "" (no tail), so it
//	          can never reconstruct or resolve the band.
//	tail    - the canonical secret tail, e.g. "8F3K9M2Q" (no dash/space), for hashing.
//
// The broker persists ONLY sha256(tail) + the masked display; the full code is never
// stored. crypto/rand backs the tail (40 bits => ~1.1e12 codes, unguessable).
func NewBandCode() (code, display, tail string) {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	// Cosmetic frequency: a plausible "MHz" channel from the first bytes. Range
	// chosen to read like a 2m/220 ham band; it is decoration, never the key.
	mhz := 144 + int(b[0])%76 // 144..219
	khz := (int(b[1])<<8 | int(b[2])) % 1000
	freq := itoa3(mhz) + "." + pad3(khz) + " MHz"

	// Secret tail: bandTailLen Crockford symbols, one symbol per uniformly-random
	// byte (each draw masks to 5 bits => an exactly-uniform symbol, no modulo bias).
	raw := make([]byte, bandTailLen)
	_, _ = rand.Read(raw)
	var sb strings.Builder
	for i := 0; i < bandTailLen; i++ {
		sb.WriteByte(crockfordAlphabet[int(raw[i])&0x1f])
	}
	t := sb.String()
	code = freq + bandSep + t[:4] + "-" + t[4:] // the SECRET full code, shown ONCE at mint
	display = freq + bandSep + maskedTail       // cosmetic, non-recoverable, safe to persist
	return code, display, t
}

// MaskBandDisplay rewrites a band's PERSISTED cosmetic display into the masked,
// NON-RECOVERABLE form, keeping the cosmetic frequency but replacing the tail with
// maskedTail, e.g. "147.520 MHz · 8F3K-9M2Q" -> "147.520 MHz · ••••-••••". It is the
// per-row transform of the one-time store re-mask migration that scrubs bands minted
// BEFORE the display was masked at the source: pre-fix the persisted display WAS the
// resolvable code ("freq · TAIL"), so CanonicalBandTail/BandCodeHash recovered the secret
// straight out of stored state. The result always canonicalizes to "" (no tail), so it can
// NEVER reconstruct or resolve a band. IDEMPOTENT: an already-masked display is returned
// unchanged (so a re-run of the migration changes nothing). Only the DISPLAY is touched;
// the migration leaves the band's CodeHash intact, so the owner's one-time full code still
// resolves.
func MaskBandDisplay(display string) string {
	// A real minted display is "<cosmetic freq>·<tail>": keep the cosmetic part and replace
	// the tail (everything after the separator) with the non-recoverable mask.
	if freq, _, ok := strings.Cut(display, bandSep); ok {
		return freq + bandSep + maskedTail
	}
	// Defensive: an unrecognized display with no separator (never produced by a mint). The
	// bare mask carries no Crockford symbols, so the result is guaranteed non-recoverable
	// even if the input ended in a full tail's worth of symbols.
	return maskedTail
}

// CanonicalBandTail extracts the secret tail from anything the user might type and
// normalizes it to the canonical form used for hashing: it strips the cosmetic
// frequency / "MHz" / spaces / dashes / dots and any middot, uppercases, and maps
// Crockford's confusable inputs (I/L -> 1, O -> 0) so a human-transcribed code
// still resolves. It returns the trailing run of valid Crockford symbols (the tail
// is always the LAST bandTailLen symbols), or "" if there aren't enough. The
// cosmetic part is discarded here, never folded into the key.
func CanonicalBandTail(input string) string {
	up := strings.ToUpper(input)
	// "MHZ" contains M, H, Z which ARE Crockford symbols, so strip the "MHZ" unit
	// token BEFORE filtering or it would fold into the tail. The cosmetic frequency
	// digits (the leading "147.520") are harmless: they are leading and the tail is
	// taken from the END below, so they fall off.
	up = strings.ReplaceAll(up, "MHZ", " ")
	var sb strings.Builder
	for _, r := range up {
		switch r {
		case 'I', 'L':
			r = '1'
		case 'O':
			r = '0'
		}
		if strings.IndexRune(crockfordAlphabet, r) >= 0 {
			sb.WriteRune(r)
		}
		// everything else (spaces, dashes, dots, the middot) is dropped.
	}
	s := sb.String()
	if len(s) < bandTailLen {
		return ""
	}
	// The tail is the LAST bandTailLen symbols (the cosmetic frequency digits, if any
	// survived, are leading and dropped here).
	return s[len(s)-bandTailLen:]
}

// BandCodeHash is the canonical lookup key for a band: sha256 over the canonical
// secret tail ONLY (hex). The cosmetic frequency is never part of it. An input that
// has no valid tail hashes the empty string, which never matches a minted band.
func BandCodeHash(input string) string {
	tail := CanonicalBandTail(input)
	sum := sha256.Sum256([]byte(tail))
	return hex.EncodeToString(sum[:])
}

func pad3(n int) string {
	if n < 0 {
		n = 0
	}
	s := itoa3(n)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}

// itoa3 is a tiny non-negative int -> string (avoids importing strconv here for one use).
func itoa3(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
