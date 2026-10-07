package tui

// MergeLimit lays every key `o` sets over `base` (a shallow merge per key, contract §10): a
// non-zero number, a non-empty list or string replaces, and SelfHosted is OR'd. It is the ONE
// rule both the CLI (`roger use`) and the booth resolve a band's rule by.
func MergeLimit(base, o Limit) Limit {
	pick := func(a, b float64) float64 {
		if b != 0 {
			return b
		}
		return a
	}
	base.MaxIn, base.MaxOut, base.MinTPS, base.MaxCost = pick(base.MaxIn, o.MaxIn), pick(base.MaxOut, o.MaxOut), pick(base.MinTPS, o.MinTPS), pick(base.MaxCost, o.MaxCost)
	if len(o.Quants) > 0 {
		base.Quants = o.Quants
	}
	if o.Pref != "" {
		base.Pref = o.Pref
	}
	if len(o.Require) > 0 {
		base.Require = o.Require
	}
	if len(o.ParamsB) > 0 {
		base.ParamsB = o.ParamsB
	}
	if o.MinCtx != 0 {
		base.MinCtx = o.MinCtx
	}
	if o.MaxTTFTMs != 0 {
		base.MaxTTFTMs = o.MaxTTFTMs
	}
	if o.TrustMin != "" {
		base.TrustMin = o.TrustMin
	}
	base.SelfHosted = base.SelfHosted || o.SelfHosted
	if len(o.Region) > 0 {
		base.Region = o.Region
	}
	return base
}

// LimitMergeCase is one row of the shared limit-merge table: the CLI (cmd/rogerai) and the
// TUI resolve a band's rule with the same per-key merge, and both test against these rows.
type LimitMergeCase struct {
	Name           string
	Default, Model Limit
	Want           Limit
}

// LimitMergeCases is the table both resolvers are held to (contract §10: the Default with
// each key the band sets laid over it).
func LimitMergeCases() []LimitMergeCase {
	def := Limit{MaxOut: 5, MinTPS: 10, TrustMin: "verified", SelfHosted: true, Region: []string{"eu"}, Pref: "cheap"}
	return []LimitMergeCase{
		{Name: "no band rule: the default", Default: def, Model: Limit{}, Want: def},
		{Name: "band sets one key, the default keys stay", Default: def, Model: Limit{MaxOut: 2},
			Want: Limit{MaxOut: 2, MinTPS: 10, TrustMin: "verified", SelfHosted: true, Region: []string{"eu"}, Pref: "cheap"}},
		{Name: "band overrides several keys", Default: def,
			Model: Limit{MinTPS: 30, TrustMin: "confidential", Region: []string{"us"}, Pref: "fast", Quants: []string{"Q8_0"}},
			Want:  Limit{MaxOut: 5, MinTPS: 30, TrustMin: "confidential", SelfHosted: true, Region: []string{"us"}, Pref: "fast", Quants: []string{"Q8_0"}}},
		{Name: "self-hosted is OR'd", Default: Limit{}, Model: Limit{SelfHosted: true}, Want: Limit{SelfHosted: true}},
		{Name: "band-only keys", Default: Limit{},
			Model: Limit{MaxIn: 0.5, MaxCost: 0.02, Require: []string{"tools"}, ParamsB: []float64{7, 70}, MinCtx: 32768, MaxTTFTMs: 1500},
			Want:  Limit{MaxIn: 0.5, MaxCost: 0.02, Require: []string{"tools"}, ParamsB: []float64{7, 70}, MinCtx: 32768, MaxTTFTMs: 1500}},
	}
}
