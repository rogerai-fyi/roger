package client

// The routing values a person types (roger use flags, the TUI's [3] CONFIG plate), parsed one
// way for every first-party client (ROUTING-EXPRESSION-CONTRACT.md §10).

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParamsOpenMax is the upper bound an open size range ("30-") sends: above any real model.
const ParamsOpenMax = 10000

// ParseParams reads roger.params_b: an inclusive range of billions ("7-70", "7b-70b", "30",
// "30-" = at least 30, "-8" = at most 8).
func ParseParams(v string) ([]float64, error) {
	bad := fmt.Errorf("%q is not a size range in billions (7-70, 30, 30-, -8)", v)
	num := func(s string) (float64, bool) {
		s = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), "b")
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) && f >= 0
	}
	lo, hi, ranged := strings.Cut(strings.TrimSpace(v), "-")
	switch {
	case !ranged:
		f, ok := num(lo)
		if !ok || f <= 0 {
			return nil, bad
		}
		return []float64{f, f}, nil
	case lo == "" && hi == "":
		return nil, bad
	case lo == "":
		f, ok := num(hi)
		if !ok || f <= 0 {
			return nil, bad
		}
		return []float64{0, f}, nil
	case hi == "":
		f, ok := num(lo)
		if !ok || f <= 0 {
			return nil, bad
		}
		return []float64{f, ParamsOpenMax}, nil
	}
	a, okA := num(lo)
	b, okB := num(hi)
	switch {
	case !okA || !okB || b <= 0:
		return nil, bad
	case a > b:
		return nil, fmt.Errorf("min must be ≤ max")
	}
	return []float64{a, b}, nil
}

var ctxTokensRE = regexp.MustCompile(`^([0-9]+)([kK]?)$`)

// ParseCtx reads a context size in tokens: 8192, or 32k = 32768.
func ParseCtx(v string) (int, error) {
	m := ctxTokensRE.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0, fmt.Errorf("%q is not a token count (8192 or 32k)", v)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not a positive token count", v)
	}
	// Bounded before the multiply (so a k count never wraps), at what the broker accepts.
	if limit := math.MaxInt32; n > limit || (m[2] != "" && n > limit/1024) {
		return 0, fmt.Errorf("%q is more tokens than any context (max %d)", v, math.MaxInt32)
	}
	if m[2] != "" {
		n *= 1024
	}
	return n, nil
}

// ParseTTFT reads a first-token ceiling in ms: bare milliseconds (800) or a duration (1.5s).
func ParseTTFT(v string) (int, error) {
	t := strings.TrimSpace(v)
	if n, err := strconv.Atoi(t); err == nil {
		if n <= 0 {
			return 0, fmt.Errorf("%q is not a positive duration", v)
		}
		return n, nil
	}
	d, err := time.ParseDuration(t)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%q is not a duration (1500ms, 1.5s or bare ms)", v)
	}
	ms := int(d / time.Millisecond)
	if ms <= 0 {
		return 0, fmt.Errorf("%q is under a millisecond", v)
	}
	return ms, nil
}

var regionTokenOK = regexp.MustCompile(`^[a-z][a-z0-9-]{1,7}$`)

// NormRegion accepts one lowercase region token (eu, us-west).
func NormRegion(s string) (string, error) {
	if !regionTokenOK.MatchString(s) {
		return "", fmt.Errorf("%q is not a lowercase region (eu, us-west)", s)
	}
	return s, nil
}

// NormRequire lower-cases and accepts one capability (tools, vision).
func NormRequire(s string) (string, error) {
	s = strings.ToLower(s)
	if s != "tools" && s != "vision" {
		return "", fmt.Errorf("%q is not a capability (tools, vision)", s)
	}
	return s, nil
}
