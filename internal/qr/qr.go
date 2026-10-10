// Package qr draws a QR code (ISO/IEC 18004, Model 2, byte mode) for a join invite, so a phone or
// another machine can scan what the owner's terminal shows (features/join/invite.feature). It is a
// small, dependency-free encoder: Reed-Solomon over GF(256), the function patterns, the zigzag
// placement, the eight masks and the standard penalty. Its output is checked module for module
// against an independent implementation (testdata/qr_vectors.json, made by python-qrcode).
package qr

import (
	"errors"
	"strings"
)

// Level is the error-correction level.
type Level int

const (
	L Level = iota // about 7% of codewords can be restored
	M              // about 15%
	Q              // about 25%
	H              // about 30%
)

// formatBits is each level's 2-bit code in the format information.
var formatBits = [4]int{1, 0, 3, 2}

type blockGroup struct{ count, total, data int }

// ErrTooLong is data that does not fit in the largest QR code at this level.
var ErrTooLong = errors.New("too much data for a QR code")

// Code is an encoded QR code: Modules[y][x] is true for a dark module.
type Code struct {
	Version int
	Mask    int
	Size    int
	Modules [][]bool
}

// Encode is the smallest QR code that holds data at level lvl, with the mask the standard penalty
// prefers.
func Encode(data []byte, lvl Level) (*Code, error) { return EncodeWith(data, lvl, 0, -1) }

// EncodeWith fixes the version (0 = the smallest that fits) and the mask (-1 = by penalty).
func EncodeWith(data []byte, lvl Level, version, mask int) (*Code, error) {
	if version == 0 {
		for v := 1; v <= 40; v++ {
			if fits(len(data), v, lvl) {
				version = v
				break
			}
		}
		if version == 0 {
			return nil, ErrTooLong
		}
	} else if version < 1 || version > 40 || !fits(len(data), version, lvl) {
		return nil, ErrTooLong
	}
	cw := codewords(data, version, lvl)
	c := newCode(version)
	c.drawFunctionPatterns()
	c.drawCodewords(cw)
	if mask < 0 {
		best := -1
		for m := 0; m < 8; m++ {
			c.applyMask(m)
			c.drawFormat(lvl, m)
			if p := c.penalty(); best < 0 || p < best {
				best, mask = p, m
			}
			c.applyMask(m) // undo: masking is its own inverse
		}
	}
	c.applyMask(mask)
	c.drawFormat(lvl, mask)
	c.Mask = mask
	return c.Code, nil
}

func dataCapacity(version int, lvl Level) int {
	n := 0
	for _, g := range ecBlocks[version][lvl] {
		n += g.count * g.data
	}
	return n
}

func countBits(version int) int {
	if version <= 9 {
		return 8
	}
	return 16
}

func fits(n, version int, lvl Level) bool {
	return 4+countBits(version)+8*n <= 8*dataCapacity(version, lvl)
}

// codewords is the final, interleaved codeword sequence: byte-mode data, padded, split into blocks,
// each block followed (after interleaving) by its Reed-Solomon codewords.
func codewords(data []byte, version int, lvl Level) []byte {
	capBits := 8 * dataCapacity(version, lvl)
	var bits []bool
	put := func(v, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, v>>i&1 == 1)
		}
	}
	put(4, 4) // byte mode
	put(len(data), countBits(version))
	for _, b := range data {
		put(int(b), 8)
	}
	for i := 0; i < 4 && len(bits) < capBits; i++ {
		bits = append(bits, false) // terminator
	}
	for len(bits)%8 != 0 {
		bits = append(bits, false)
	}
	var stream []byte
	for i := 0; i < len(bits); i += 8 {
		var b byte
		for j := 0; j < 8; j++ {
			if bits[i+j] {
				b |= 1 << (7 - j)
			}
		}
		stream = append(stream, b)
	}
	for pad := byte(0xEC); len(stream) < capBits/8; pad ^= 0xEC ^ 0x11 {
		stream = append(stream, pad)
	}

	var dataBlocks, ecc [][]byte
	off := 0
	for _, g := range ecBlocks[version][lvl] {
		for i := 0; i < g.count; i++ {
			blk := stream[off : off+g.data]
			off += g.data
			dataBlocks = append(dataBlocks, blk)
			ecc = append(ecc, rsRemainder(blk, g.total-g.data))
		}
	}
	var out []byte
	for i := 0; ; i++ {
		any := false
		for _, b := range dataBlocks {
			if i < len(b) {
				out = append(out, b[i])
				any = true
			}
		}
		if !any {
			break
		}
	}
	for i := 0; i < len(ecc[0]); i++ {
		for _, e := range ecc {
			out = append(out, e[i])
		}
	}
	return out
}

// --- Reed-Solomon over GF(256), primitive polynomial 0x11D ---------------------------------------

func gfMul(x, y byte) byte {
	var z byte
	for i := 7; i >= 0; i-- {
		hi := z >> 7
		z = z<<1 ^ hi*0x1D
		z ^= (y >> uint(i) & 1) * x
	}
	return z
}

func rsDivisor(degree int) []byte {
	res := make([]byte, degree)
	res[degree-1] = 1
	root := byte(1)
	for i := 0; i < degree; i++ {
		for j := range res {
			res[j] = gfMul(res[j], root)
			if j+1 < len(res) {
				res[j] ^= res[j+1]
			}
		}
		root = gfMul(root, 2)
	}
	return res
}

func rsRemainder(data []byte, degree int) []byte {
	div := rsDivisor(degree)
	res := make([]byte, degree)
	for _, b := range data {
		f := b ^ res[0]
		copy(res, res[1:])
		res[len(res)-1] = 0
		for i := range res {
			res[i] ^= gfMul(div[i], f)
		}
	}
	return res
}

// --- the matrix ---------------------------------------------------------------------------------

type matrix struct {
	*Code
	fn [][]bool // function modules, never masked or written with data
}

func newCode(version int) *matrix {
	size := 4*version + 17
	c := &Code{Version: version, Size: size, Modules: make([][]bool, size)}
	m := &matrix{Code: c, fn: make([][]bool, size)}
	for i := range c.Modules {
		c.Modules[i] = make([]bool, size)
		m.fn[i] = make([]bool, size)
	}
	return m
}

func (m *matrix) set(x, y int, dark bool) { m.Modules[y][x], m.fn[y][x] = dark, true }

func (m *matrix) drawFunctionPatterns() {
	n := m.Size
	for i := 0; i < n; i++ {
		m.set(6, i, i%2 == 0)
		m.set(i, 6, i%2 == 0)
	}
	m.finder(3, 3)
	m.finder(n-4, 3)
	m.finder(3, n-4)
	pos := alignPos[m.Version]
	for i, a := range pos {
		for j, b := range pos {
			last := len(pos) - 1
			if (i == 0 && j == 0) || (i == 0 && j == last) || (i == last && j == 0) {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					m.set(a+dx, b+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	m.drawFormat(L, 0) // reserve the format areas; drawn for real once the mask is chosen
	if m.Version >= 7 {
		rem := m.Version
		for i := 0; i < 12; i++ {
			rem = rem<<1 ^ (rem>>11)*0x1F25
		}
		bits := m.Version<<12 | rem
		for i := 0; i < 18; i++ {
			dark := bits>>i&1 == 1
			a, b := n-11+i%3, i/3
			m.set(a, b, dark)
			m.set(b, a, dark)
		}
	}
}

// finder draws a finder pattern centred at (x, y) with its light separator.
func (m *matrix) finder(x, y int) {
	for dy := -4; dy <= 4; dy++ {
		for dx := -4; dx <= 4; dx++ {
			xx, yy := x+dx, y+dy
			if xx < 0 || xx >= m.Size || yy < 0 || yy >= m.Size {
				continue
			}
			d := max(abs(dx), abs(dy))
			m.set(xx, yy, d != 2 && d != 4)
		}
	}
}

func (m *matrix) drawFormat(lvl Level, mask int) {
	data := formatBits[lvl]<<3 | mask
	rem := data
	for i := 0; i < 10; i++ {
		rem = rem<<1 ^ (rem>>9)*0x537
	}
	bits := (data<<10 | rem) ^ 0x5412
	bit := func(i int) bool { return bits>>i&1 == 1 }
	for i := 0; i <= 5; i++ {
		m.set(8, i, bit(i))
	}
	m.set(8, 7, bit(6))
	m.set(8, 8, bit(7))
	m.set(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		m.set(14-i, 8, bit(i))
	}
	n := m.Size
	for i := 0; i < 8; i++ {
		m.set(n-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		m.set(8, n-15+i, bit(i))
	}
	m.set(8, n-8, true) // the dark module
}

func (m *matrix) drawCodewords(data []byte) {
	n, i := m.Size, 0
	for right := n - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < n; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				y := vert
				if (right+1)&2 == 0 {
					y = n - 1 - vert
				}
				if !m.fn[y][x] && i < len(data)*8 {
					m.Modules[y][x] = data[i>>3]>>(7-i&7)&1 == 1
					i++
				}
			}
		}
	}
}

func (m *matrix) applyMask(mask int) {
	for y := 0; y < m.Size; y++ {
		for x := 0; x < m.Size; x++ {
			var inv bool
			switch mask {
			case 0:
				inv = (x+y)%2 == 0
			case 1:
				inv = y%2 == 0
			case 2:
				inv = x%3 == 0
			case 3:
				inv = (x+y)%3 == 0
			case 4:
				inv = (x/3+y/2)%2 == 0
			case 5:
				inv = x*y%2+x*y%3 == 0
			case 6:
				inv = (x*y%2+x*y%3)%2 == 0
			default:
				inv = ((x+y)%2+x*y%3)%2 == 0
			}
			if !m.fn[y][x] && inv {
				m.Modules[y][x] = !m.Modules[y][x]
			}
		}
	}
}

// penalty is the standard's mask evaluation: runs of five or more, 2x2 blocks, finder-like
// patterns, and the dark/light balance.
func (m *matrix) penalty() int {
	n, res := m.Size, 0
	at := func(x, y int, cols bool) bool {
		if cols {
			return m.Modules[x][y]
		}
		return m.Modules[y][x]
	}
	for _, cols := range []bool{false, true} {
		for y := 0; y < n; y++ {
			color, run := false, 0
			var hist [7]int
			for x := 0; x < n; x++ {
				if at(x, y, cols) == color {
					run++
					if run == 5 {
						res += 3
					} else if run > 5 {
						res++
					}
				} else {
					m.addHistory(run, &hist)
					if !color {
						res += m.countPatterns(hist) * 40
					}
					color, run = at(x, y, cols), 1
				}
			}
			if color {
				m.addHistory(run, &hist)
				run = 0
			}
			run += n
			m.addHistory(run, &hist)
			res += m.countPatterns(hist) * 40
		}
	}
	for y := 0; y < n-1; y++ {
		for x := 0; x < n-1; x++ {
			c := m.Modules[y][x]
			if c == m.Modules[y][x+1] && c == m.Modules[y+1][x] && c == m.Modules[y+1][x+1] {
				res += 3
			}
		}
	}
	dark := 0
	for _, row := range m.Modules {
		for _, d := range row {
			if d {
				dark++
			}
		}
	}
	total := n * n
	k := (abs(dark*20-total*10)+total-1)/total - 1
	return res + k*10
}

func (m *matrix) addHistory(run int, h *[7]int) {
	if h[0] == 0 {
		run += m.Size // the light border before the first run
	}
	copy(h[1:], h[:6])
	h[0] = run
}

func (m *matrix) countPatterns(h [7]int) int {
	n := h[1]
	core := n > 0 && h[2] == n && h[3] == n*3 && h[4] == n && h[5] == n
	c := 0
	if core && h[0] >= n*4 && h[6] >= n {
		c++
	}
	if core && h[6] >= n*4 && h[0] >= n {
		c++
	}
	return c
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Quiet is the light border around a code: four modules, as the standard asks.
const Quiet = 4

// Terminal draws the code with half blocks, two module rows per text line, black on white with
// explicit colours (so it scans the same in a dark terminal and a light one), inside a quiet zone.
func (c *Code) Terminal() string {
	n := c.Size + 2*Quiet
	dark := func(x, y int) bool {
		x, y = x-Quiet, y-Quiet
		return x >= 0 && y >= 0 && x < c.Size && y < c.Size && c.Modules[y][x]
	}
	var b strings.Builder
	for y := 0; y < n; y += 2 {
		b.WriteString("\x1b[30;107m") // black on bright white
		for x := 0; x < n; x++ {
			top, bot := dark(x, y), y+1 < n && dark(x, y+1)
			switch {
			case top && bot:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bot:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString("\x1b[0m\n")
	}
	return b.String()
}
