package main

// routing_cli.go is the `roger use` routing surface (ROUTING-EXPRESSION-CONTRACT.md §9, §10):
// one flag per body key, one-line refusals naming the flag, and the resolution order
// flag > profile > limits.models.<m> > limits.default > built-in. It also carries the
// `roger profile` command set and `roger config show`.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/client"
)

// maxListIDs is the contract's cap on a station-id list (provider.only / order / ignore).
const maxListIDs = 32

// maxModels is the contract's cap on distinct models in one request (the positional counts).
const maxModels = 5

// listFlag is a repeatable, comma-separated list flag: items are trimmed, empties dropped,
// duplicates removed keeping the first occurrence. norm canonicalizes an item (or refuses
// it); fold de-duplicates case-insensitively while keeping the first spelling.
type listFlag struct {
	items []string
	set   bool
	max   int
	norm  func(string) (string, error)
	fold  bool
}

func (l *listFlag) String() string { return strings.Join(l.items, ",") }
func (l *listFlag) Set(v string) error {
	parts := splitCSV(v)
	if len(parts) == 0 {
		return fmt.Errorf("needs at least one value")
	}
	for _, p := range parts {
		if l.norm != nil {
			n, err := l.norm(p)
			if err != nil {
				return err
			}
			p = n
		}
		dup := false
		for _, x := range l.items {
			dup = dup || x == p || (l.fold && strings.EqualFold(x, p))
		}
		if !dup {
			l.items = append(l.items, p)
		}
	}
	if l.max > 0 && len(l.items) > l.max {
		return fmt.Errorf("more than %d entries", l.max)
	}
	l.set = true
	return nil
}

// choiceFlag accepts one of a fixed set of values.
type choiceFlag struct {
	v       string
	choices []string
}

func (c *choiceFlag) String() string { return c.v }
func (c *choiceFlag) Set(v string) error {
	for _, x := range c.choices {
		if v == x {
			c.v = v
			return nil
		}
	}
	return fmt.Errorf("%q is not one of %s", v, strings.Join(c.choices, ", "))
}

// priceFlag is a non-negative finite number that remembers whether it was passed.
type priceFlag struct {
	v   float64
	set bool
}

func (p *priceFlag) String() string { return fmt.Sprintf("%g", p.v) }
func (p *priceFlag) Set(v string) error {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return fmt.Errorf("%q is not a number >= 0", v)
	}
	p.v, p.set = f, true
	return nil
}

// intFlag is a positive integer parsed by parse (tokens with a k suffix, durations in ms).
type intFlag struct {
	v     int
	parse func(string) (int, error)
}

func (i *intFlag) String() string { return strconv.Itoa(i.v) }
func (i *intFlag) Set(v string) error {
	n, err := i.parse(v)
	if err != nil {
		return err
	}
	i.v = n
	return nil
}

// paramsFlag is roger.params_b: an inclusive range of billions ("7-70", "7b-70b", "30",
// "30-" = at least 30, "-8" = at most 8).
type paramsFlag struct{ v []float64 }

func (p *paramsFlag) String() string { return fmt.Sprint(p.v) }
func (p *paramsFlag) Set(v string) error {
	r, err := parseParams(v)
	if err != nil {
		return err
	}
	p.v = r
	return nil
}

// paramsOpenMax is the upper bound an open range ("30-") sends: above any real model.
const paramsOpenMax = 10000

func parseParams(v string) ([]float64, error) {
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
		return []float64{f, paramsOpenMax}, nil
	}
	a, okA := num(lo)
	b, okB := num(hi)
	if !okA || !okB || b <= 0 || a > b {
		return nil, bad
	}
	return []float64{a, b}, nil
}

var ctxRE = regexp.MustCompile(`^([0-9]+)([kK]?)$`)

// parseCtx reads a context size in tokens: 8192, or 32k = 32768.
func parseCtx(v string) (int, error) {
	m := ctxRE.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0, fmt.Errorf("%q is not a token count (8192 or 32k)", v)
	}
	n, err := strconv.Atoi(m[1])
	if m[2] != "" {
		n *= 1024
	}
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%q is not a positive token count", v)
	}
	return n, nil
}

// parseTTFT reads a first-token ceiling: bare milliseconds (800) or a duration (1.5s).
func parseTTFT(v string) (int, error) {
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

var regionRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,7}$`)

func normRegion(s string) (string, error) {
	if !regionRE.MatchString(s) {
		return "", fmt.Errorf("%q is not a lowercase region (eu, us-west)", s)
	}
	return s, nil
}

func normRequire(s string) (string, error) {
	s = strings.ToLower(s)
	if s != "tools" && s != "vision" {
		return "", fmt.Errorf("%q is not a capability (tools, vision)", s)
	}
	return s, nil
}

// useFlags is one parsed `roger use` command line.
type useFlags struct {
	model                                 string
	maxOut                                maxOutFlag
	maxIn, minTPS, maxCost                priceFlag
	pref                                  prefFlag
	sort, trust                           choiceFlag
	models, only, order, exclude          listFlag
	quant, require, region                listFlag
	node, profile, freq                   string
	nodeSet, profileSet                   bool
	params                                paramsFlag
	minCtx, maxTTFT                       intFlag
	selfHosted, confidential, noFallbacks bool
	advanced, yes, raw                    bool
	port                                  int
}

// useHelp is `roger use --help`: the routing flags grouped under one heading.
const useHelp = `usage: roger use <model> [flags]

  --max-out $       ` + maxOutHelp + `
                    (--max-out 0 keeps the default $10/1M cap; it is never uncapped)
  --pref P          cheap, balanced, fast or reliable (a scoring knob, never a filter)
  --profile NAME    a named routing profile (roger profile list); @profile/NAME as <model> too

routing:
  --models a,b      fallback models after <model> (up to 5 in all)
  --only ids        only these stations may serve
  --order ids       try these stations first
  --exclude ids     never these stations (repeatable)
  --node id         pin one station (= --order id --no-fallbacks)
  --no-fallbacks    never route outside --order
  --sort S          price, throughput or latency (not with --pref)
  --quant labels    accept only these quant labels (e.g. Q8_0,BF16)
  --require caps    tools, vision
  --params R        model size in billions: 7-70, 30, 30-, -8
  --min-ctx N       context window floor in tokens (32k = 32768)
  --max-ttft D      first-token ceiling (1500ms, 1.5s or bare ms)
  --trust T         any, verified or confidential
  --self-hosted     never a curated commercial proxy
  --region list     lowercase regions (eu,us)
  --max-cost $      per-request USD cap; 0 = no per-request cap
  --freq CODE       tune a private band by its frequency code

advanced flags: --port --max-in --min-tps --confidential --yes --raw
  --port N          local endpoint port (0 = auto-pick a free one)
  --max-in $        skip stations above this $/1M INPUT price; 0 = none
  --min-tps N       require at least this measured throughput (tok/s); 0 = no floor
  --confidential    route only to confidential (TEE-attested) stations
  --yes             skip the connect-time confirm (scripts, bots)
  --raw             disable the reasoning->content fallback for this session
`

var flagNameRE = regexp.MustCompile(`-{1,2}([A-Za-z0-9][A-Za-z0-9_-]*)`)

// flagError turns the flag package's multi-word errors into one line naming the flag.
func flagError(cmd string, err error) error {
	msg := err.Error()
	name := ""
	if m := flagNameRE.FindAllStringSubmatch(msg, -1); len(m) > 0 {
		name = "--" + m[len(m)-1][1]
	}
	switch {
	case strings.HasPrefix(msg, "flag provided but not defined"):
		return fmt.Errorf("%s: unknown flag %s (roger %s --help lists them)", cmd, name, cmd)
	case strings.HasPrefix(msg, "flag needs an argument"):
		return fmt.Errorf("%s: %s needs a value", cmd, name)
	}
	if _, reason, ok := strings.Cut(msg, ": "); ok {
		if i := strings.Index(msg, " for flag -"); i >= 0 {
			name = "--" + strings.TrimSuffix(msg[i+len(" for flag -"):], ": "+reason)
		} else if i := strings.Index(msg, " for -"); i >= 0 {
			name = "--" + strings.TrimSuffix(msg[i+len(" for -"):], ": "+reason)
		}
		return fmt.Errorf("%s: %s: %s", cmd, name, reason)
	}
	return fmt.Errorf("%s: %v", cmd, err)
}

// parseUseFlags parses `roger use` arguments. help is true when -h/--help was asked.
func parseUseFlags(args []string) (f *useFlags, help bool, err error) {
	f = &useFlags{
		sort:    choiceFlag{choices: []string{"price", "throughput", "latency"}},
		trust:   choiceFlag{choices: []string{"any", "verified", "confidential"}},
		models:  listFlag{},
		only:    listFlag{max: maxListIDs},
		order:   listFlag{max: maxListIDs},
		exclude: listFlag{max: maxListIDs},
		quant:   listFlag{fold: true},
		require: listFlag{norm: normRequire},
		region:  listFlag{norm: normRegion},
		minCtx:  intFlag{parse: parseCtx},
		maxTTFT: intFlag{parse: parseTTFT},
	}
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		f.model, rest = args[0], args[1:]
	}
	fs := flag.NewFlagSet("use", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Var(&f.maxOut, "max-out", maxOutHelp)
	fs.Var(&f.maxIn, "max-in", "")
	fs.Var(&f.minTPS, "min-tps", "")
	fs.Var(&f.maxCost, "max-cost", "")
	fs.Var(&f.pref, "pref", "")
	fs.Var(&f.sort, "sort", "")
	fs.Var(&f.trust, "trust", "")
	fs.Var(&f.models, "models", "")
	fs.Var(&f.only, "only", "")
	fs.Var(&f.order, "order", "")
	fs.Var(&f.exclude, "exclude", "")
	fs.Var(&f.quant, "quant", "")
	fs.Var(&f.require, "require", "")
	fs.Var(&f.region, "region", "")
	fs.Var(&f.params, "params", "")
	fs.Var(&f.minCtx, "min-ctx", "")
	fs.Var(&f.maxTTFT, "max-ttft", "")
	fs.Func("node", "", func(v string) error {
		v = strings.TrimSpace(v)
		if v == "" || strings.Contains(v, ",") {
			return fmt.Errorf("names exactly one station (use --order for a list)")
		}
		f.node, f.nodeSet = v, true
		return nil
	})
	fs.Func("profile", "", func(v string) error {
		if !client.ValidProfileName(v) {
			return fmt.Errorf("%q is not a profile name", v)
		}
		f.profile, f.profileSet = v, true
		return nil
	})
	fs.StringVar(&f.freq, "freq", "", "")
	fs.BoolVar(&f.selfHosted, "self-hosted", false, "")
	fs.BoolVar(&f.confidential, "confidential", false, "")
	fs.BoolVar(&f.noFallbacks, "no-fallbacks", false, "")
	fs.BoolVar(&f.advanced, "advanced", false, "")
	fs.BoolVar(&f.yes, "yes", false, "")
	fs.BoolVar(&f.raw, "raw", false, "")
	fs.IntVar(&f.port, "port", 0, "")
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, true, nil
		}
		return nil, false, flagError("use", err)
	}
	if f.model == "" || fs.NArg() > 0 {
		return nil, false, fmt.Errorf("usage: roger use <model> [flags]")
	}
	switch {
	case f.sort.v != "" && f.pref.v != "":
		return nil, false, fmt.Errorf("use: --sort and --pref are exclusive: pick one way to rank stations")
	case f.nodeSet && f.order.set:
		return nil, false, fmt.Errorf("use: --node pins one station and --order lists several: pass one of them")
	case f.nodeSet && f.only.set && !containsStr(f.only.items, f.node):
		return nil, false, fmt.Errorf("use: --node %s is outside --only", f.node)
	}
	if f.models.set {
		kept := f.models.items[:0:0]
		for _, m := range f.models.items {
			if m != f.model {
				kept = append(kept, m)
			}
		}
		f.models.items = kept
		if len(kept)+1 > maxModels {
			return nil, false, fmt.Errorf("use: --models: more than %d models in all (the positional counts)", maxModels)
		}
	}
	return f, false, nil
}

func containsStr(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// ── the body object and its sources ─────────────────────────────────────────────────────

// routingLayer is one resolution layer's body object and the label every key it sets carries.
type routingLayer struct {
	label string
	body  map[string]any
}

// resolvedRouting is the merged body object and each dotted leaf's source label.
type resolvedRouting struct {
	body map[string]any
	src  map[string]string
}

// limitBody is a stored limit as the §1 body object. A standing quant rule carries
// "unknown" (client.RuleQuantizations).
func limitBody(l Limit) map[string]any {
	b := map[string]any{}
	set := func(path string, v any) { rfSetPath(b, path, v) }
	if l.MaxIn > 0 {
		set("provider.max_price.prompt", l.MaxIn)
	}
	if l.MaxOut > 0 {
		set("provider.max_price.completion", l.MaxOut)
	}
	if l.MaxCost > 0 {
		set("provider.max_price.request", l.MaxCost)
	}
	if q := client.RuleQuantizations(l.Quants); len(q) > 0 {
		set("provider.quantizations", anyList(q))
	}
	if l.Pref != "" {
		set("roger.pref", l.Pref)
	}
	if len(l.Require) > 0 {
		set("roger.require", anyList(l.Require))
	}
	if len(l.ParamsB) == 2 {
		set("roger.params_b", []any{l.ParamsB[0], l.ParamsB[1]})
	}
	if l.MinCtx > 0 {
		set("roger.min_ctx", float64(l.MinCtx))
	}
	if l.MaxTTFTMs > 0 {
		set("roger.max_ttft_ms", float64(l.MaxTTFTMs))
	}
	if l.TrustMin != "" {
		set("roger.trust_min", l.TrustMin)
	}
	if l.SelfHosted {
		set("roger.self_hosted_only", true)
	}
	if len(l.Region) > 0 {
		set("roger.region", anyList(l.Region))
	}
	if l.MinTPS > 0 {
		set("roger.min_tps", l.MinTPS)
	}
	return b
}

// flagsBody is the command line as a body layer. A null clears a lower layer's value
// (--max-in 0, --min-tps 0, --max-cost 0); --max-out 0 is the default consumer cap.
func flagsBody(f *useFlags) map[string]any {
	b := map[string]any{}
	set := func(path string, v any) { rfSetPath(b, path, v) }
	if f.models.set {
		set("models", anyList(f.models.items))
	}
	if f.only.set {
		set("provider.only", anyList(f.only.items))
	}
	if f.order.set {
		set("provider.order", anyList(f.order.items))
	}
	if f.nodeSet {
		set("provider.order", []any{f.node})
		set("provider.allow_fallbacks", false)
	}
	if f.exclude.set {
		set("provider.ignore", anyList(f.exclude.items))
	}
	if f.noFallbacks {
		set("provider.allow_fallbacks", false)
	}
	if f.sort.v != "" {
		set("provider.sort", f.sort.v)
	}
	if f.quant.set {
		set("provider.quantizations", anyList(f.quant.items))
	}
	nullable := func(path string, p priceFlag) {
		if !p.set {
			return
		}
		if p.v == 0 {
			set(path, nil)
			return
		}
		set(path, p.v)
	}
	nullable("provider.max_price.prompt", f.maxIn)
	nullable("provider.max_price.request", f.maxCost)
	nullable("roger.min_tps", f.minTPS)
	if f.maxOut.set {
		v := f.maxOut.v
		if v == 0 {
			v = client.ConsumerDefaultMaxOut
		}
		set("provider.max_price.completion", v)
	}
	if f.pref.v != "" {
		set("roger.pref", f.pref.v)
	}
	if f.require.set {
		set("roger.require", anyList(f.require.items))
	}
	if f.params.v != nil {
		set("roger.params_b", []any{f.params.v[0], f.params.v[1]})
	}
	if f.minCtx.v > 0 {
		set("roger.min_ctx", float64(f.minCtx.v))
	}
	if f.maxTTFT.v > 0 {
		set("roger.max_ttft_ms", float64(f.maxTTFT.v))
	}
	if f.trust.v != "" {
		set("roger.trust_min", f.trust.v)
	}
	if f.selfHosted {
		set("roger.self_hosted_only", true)
	}
	if f.region.set {
		set("roger.region", anyList(f.region.items))
	}
	if f.confidential {
		set("roger.confidential", true)
	}
	if f.freq = strings.TrimSpace(f.freq); f.freq != "" {
		set("roger.freq", f.freq)
	}
	return b
}

// mergeLayers resolves layers lowest first. A layer that states a ranking (provider.sort or
// roger.pref) replaces the other spelling from the layers below it: they are one choice.
func mergeLayers(layers ...routingLayer) resolvedRouting {
	r := resolvedRouting{body: map[string]any{}, src: map[string]string{}}
	for _, l := range layers {
		if hasPath(l.body, "provider.sort") {
			rfDelPath(r.body, "roger.pref")
			delete(r.src, "roger.pref")
		}
		if hasPath(l.body, "roger.pref") {
			rfDelPath(r.body, "provider.sort")
			delete(r.src, "provider.sort")
		}
		merged, err := client.MergeProfile(r.body, l.body)
		if err != nil { // unreachable: the only exclusive pair was cleared above
			continue
		}
		r.body = merged
		for _, leaf := range routingLeaves(l.body) {
			if v, _ := rfGetPath(l.body, leaf); v == nil {
				delete(r.src, leaf)
			} else {
				r.src[leaf] = l.label
			}
		}
	}
	return r
}

// routingLeaves lists a body's dotted routing leaves (provider.max_price per sub-key).
func routingLeaves(b map[string]any) []string {
	var out []string
	if _, ok := b["models"]; ok {
		out = append(out, "models")
	}
	if v, ok := b["model"]; ok && v != nil {
		out = append(out, "model")
	}
	for _, top := range []string{"provider", "roger"} {
		obj, _ := b[top].(map[string]any)
		for k, v := range obj {
			if mp, isObj := v.(map[string]any); isObj && k == "max_price" {
				for sk := range mp {
					out = append(out, top+".max_price."+sk)
				}
				continue
			}
			out = append(out, top+"."+k)
		}
	}
	sort.Strings(out)
	return out
}

// ── profiles in config.json ─────────────────────────────────────────────────────────────

// readProfiles parses the profiles section of the config on disk (an empty set when there is
// no file; the config loader has already moved a corrupt one aside).
func readProfiles() *client.Profiles {
	b, err := os.ReadFile(configPath())
	if err != nil {
		ps, _ := client.ParseProfiles([]byte(`{}`))
		return ps
	}
	ps, err := client.ParseProfiles(b)
	if err != nil {
		ps, _ = client.ParseProfiles([]byte(`{}`))
	}
	return ps
}

// defaultLayers are limits.default and limits.models.<model> as resolution layers (model ""
// = the default alone).
func defaultLayers(cfg config, model string) []routingLayer {
	ls := []routingLayer{{label: "limits.default", body: limitBody(cfg.Limits.Default)}}
	if l, ok := cfg.Limits.Models[model]; ok && model != "" {
		ls = append(ls, routingLayer{label: "limits.models." + model, body: limitBody(l)})
	}
	return ls
}

// useTarget is what a `roger use` resolves to: the band model (variant suffix kept) and the
// merged routing.
type useTarget struct {
	model string
	r     resolvedRouting
}

// resolveUse applies the resolution order to a parsed command line.
func resolveUse(cfg config, f *useFlags, profs *client.Profiles) (useTarget, error) {
	model, name := f.model, f.profile
	if ref, ok := strings.CutPrefix(f.model, client.ProfileRef); ok {
		switch {
		case f.profileSet && f.profile != ref:
			return useTarget{}, fmt.Errorf("use: two profiles named: @profile/%s and --profile %s", ref, f.profile)
		case f.models.set:
			return useTarget{}, fmt.Errorf("use: --models cannot be combined with @profile/%s (the profile's list is the one source)", ref)
		}
		name, model = ref, ""
	}
	var prof *client.Profile
	if name != "" {
		p, perr, ok := profs.Get(name)
		switch {
		case perr != nil:
			return useTarget{}, perr
		case !ok && f.profileSet:
			return useTarget{}, fmt.Errorf("use: --profile %s: no such profile (roger profile list)", name)
		case !ok:
			return useTarget{}, fmt.Errorf("use: @profile/%s: no such profile (roger profile list)", name)
		}
		prof = p
	}
	if model == "" && prof != nil {
		if m, _ := prof.Body["model"].(string); m != "" {
			model = m
		} else if l, _ := prof.Body["models"].([]any); len(l) > 0 {
			model, _ = l[0].(string)
		}
	}
	if model == "" {
		return useTarget{}, fmt.Errorf("profile %s names no model: roger use <model> --profile %s", name, name)
	}
	layers := defaultLayers(cfg, bareModelID(model))
	if prof != nil {
		pb := map[string]any{}
		for k, v := range prof.Body {
			if k != "model" {
				pb[k] = v
			}
		}
		layers = append(layers, routingLayer{label: "profile " + name, body: pb})
	}
	layers = append(layers, routingLayer{label: "flag", body: flagsBody(f)})
	return useTarget{model: model, r: mergeLayers(layers...)}, nil
}

// bareModelID strips a variant suffix (:free / :floor / :nitro) for the limits lookup.
func bareModelID(m string) string {
	for {
		t := m
		for _, s := range []string{":free", ":floor", ":nitro"} {
			t = strings.TrimSuffix(t, s)
		}
		if t == m {
			return m
		}
		m = t
	}
}

// useOptions translates the merged body object into the proxy's session options.
func useOptions(b map[string]any) client.UseOptions {
	o := client.UseOptions{}
	str := func(p string) string { v, _ := rfGetPath(b, p); s, _ := v.(string); return s }
	num := func(p string) float64 { v, _ := rfGetPath(b, p); f, _ := v.(float64); return f }
	list := func(p string) []string { v, _ := rfGetPath(b, p); return strList(v) }
	boolAt := func(p string) (bool, bool) { v, _ := rfGetPath(b, p); x, ok := v.(bool); return x, ok }
	o.Models = list("models")
	o.Only, o.Prefer, o.ExcludeNodes = list("provider.only"), list("provider.order"), list("provider.ignore")
	if v, ok := boolAt("provider.allow_fallbacks"); ok && !v {
		o.NoFallbacks = true
	}
	o.RequireParams, _ = boolAt("provider.require_parameters")
	o.Sort, o.Quantizations = str("provider.sort"), list("provider.quantizations")
	o.MaxIn, o.MaxOut, o.MaxCost = num("provider.max_price.prompt"), num("provider.max_price.completion"), num("provider.max_price.request")
	o.Pref, o.Require, o.Trust, o.Region = str("roger.pref"), list("roger.require"), str("roger.trust_min"), list("roger.region")
	if v, _ := rfGetPath(b, "roger.params_b"); v != nil {
		if a, _ := v.([]any); len(a) == 2 {
			lo, _ := a[0].(float64)
			hi, _ := a[1].(float64)
			o.ParamsB = []float64{lo, hi}
		}
	}
	o.MinCtx, o.MaxTTFT = int(num("roger.min_ctx")), int(num("roger.max_ttft_ms"))
	o.SelfHostedOnly, _ = boolAt("roger.self_hosted_only")
	o.Confidential, _ = boolAt("roger.confidential")
	o.MinTPS, o.Freq = num("roger.min_tps"), str("roger.freq")
	return o
}

// ── the effective routing line ──────────────────────────────────────────────────────────

// routingItems renders the body's routing keys in a fixed order, one short phrase each, with
// the dotted key it came from. A band code is never rendered.
func routingItems(b map[string]any) [][2]string {
	var out [][2]string
	add := func(key, text string) { out = append(out, [2]string{key, text}) }
	ids := func(p string) string {
		v, _ := rfGetPath(b, p)
		l := strList(v)
		if len(l) > 3 {
			return strings.Join(l[:3], ",") + fmt.Sprintf(" +%d", len(l)-3)
		}
		return strings.Join(l, ",")
	}
	has := func(p string) bool { v, ok := rfGetPath(b, p); return ok && v != nil }
	get := func(p string) any { v, _ := rfGetPath(b, p); return v }
	if has("roger.pref") {
		add("roger.pref", fmt.Sprint(get("roger.pref")))
	}
	if has("provider.sort") {
		add("provider.sort", fmt.Sprint(get("provider.sort")))
	}
	for _, k := range []struct{ key, word string }{
		{"models", "models"}, {"provider.only", "only"}, {"provider.order", "order"}, {"provider.ignore", "exclude"},
	} {
		if has(k.key) {
			add(k.key, k.word+" "+ids(k.key))
		}
	}
	if v, ok := get("provider.allow_fallbacks").(bool); ok && !v {
		add("provider.allow_fallbacks", "no fallbacks")
	}
	if has("roger.require") {
		add("roger.require", "require "+ids("roger.require"))
	}
	if a, _ := get("roger.params_b").([]any); len(a) == 2 {
		add("roger.params_b", fmt.Sprintf("%g-%gB", a[0], a[1]))
	}
	if has("roger.min_ctx") {
		add("roger.min_ctx", fmt.Sprintf("ctx ≥ %g", get("roger.min_ctx")))
	}
	if has("roger.max_ttft_ms") {
		add("roger.max_ttft_ms", fmt.Sprintf("ttft ≤ %gms", get("roger.max_ttft_ms")))
	}
	if has("roger.trust_min") {
		add("roger.trust_min", fmt.Sprint("trust ", get("roger.trust_min")))
	}
	if v, _ := get("roger.self_hosted_only").(bool); v {
		add("roger.self_hosted_only", "self-hosted")
	}
	if has("roger.region") {
		add("roger.region", "region "+ids("roger.region"))
	}
	if has("provider.quantizations") {
		add("provider.quantizations", "quant "+ids("provider.quantizations"))
	}
	if v, _ := get("provider.require_parameters").(bool); v {
		add("provider.require_parameters", "require params")
	}
	if v, _ := get("roger.confidential").(bool); v {
		add("roger.confidential", "confidential")
	}
	if has("roger.min_tps") {
		add("roger.min_tps", fmt.Sprintf("≥%g t/s", get("roger.min_tps")))
	}
	if has("provider.max_price.prompt") {
		add("provider.max_price.prompt", fmt.Sprintf("in ≤ $%g/1M", get("provider.max_price.prompt")))
	}
	if has("provider.max_price.completion") {
		add("provider.max_price.completion", fmt.Sprintf("out ≤ $%g/1M", get("provider.max_price.completion")))
	}
	if has("provider.max_price.request") {
		add("provider.max_price.request", fmt.Sprintf("cost ≤ $%g", get("provider.max_price.request")))
	}
	if has("roger.freq") {
		add("roger.freq", "private freq")
	}
	return out
}

// sourceWord is how the routing line names a source.
func sourceWord(src string) string {
	switch {
	case src == "limits.default":
		return "default limit"
	case strings.HasPrefix(src, "limits.models."):
		return "model limit"
	}
	return src
}

// routingLine is the connect plate's one effective-routing line ("" when nothing beyond the
// built-in cap is set). Each value names its source unless every value came from a flag.
func routingLine(r resolvedRouting) string {
	items := routingItems(r.body)
	if len(items) == 0 {
		return ""
	}
	mixed := false
	for _, it := range items {
		mixed = mixed || r.src[it[0]] != "flag"
	}
	parts := make([]string, 0, len(items))
	for _, it := range items {
		if mixed {
			parts = append(parts, fmt.Sprintf("%s (%s)", it[1], sourceWord(r.src[it[0]])))
		} else {
			parts = append(parts, it[1])
		}
	}
	return "routing: " + strings.Join(parts, " · ")
}

// ── roger use ───────────────────────────────────────────────────────────────────────────

func cmdUse(cfg config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: roger use <model> [flags]")
	}
	f, help, err := parseUseFlags(args)
	if help {
		fmt.Print(useHelp)
		return nil
	}
	if err != nil {
		return err
	}
	if f.advanced {
		fmt.Println("advanced flags: --port --max-in --min-tps --confidential --yes --raw")
	}
	profs := readProfiles()
	if profs.SectionWarn != "" {
		fmt.Fprintln(os.Stderr, profs.SectionWarn)
	}
	t, err := resolveUse(cfg, f, profs)
	if err != nil {
		return err
	}
	opt := useOptions(t.r.body)
	if opt.MaxOut > client.ConsumerCeilingMaxOut {
		// Sent as given (the broker clamps it); said once so the number on screen is honest.
		fmt.Printf("  max-out %g is above what any station may charge - capped at the network ceiling $%.0f/1M\n", opt.MaxOut, client.ConsumerCeilingMaxOut)
	}
	_, typical := cfg.resolve(bareModelID(t.model))
	port := f.port
	if port == 0 {
		p, err := freePort(4141) // auto-pick + the endpoint line prints the chosen port
		if err != nil {
			return err
		}
		port = p
	}
	opt.Port, opt.TypicalOut, opt.Yes, opt.Raw = port, typical, f.yes, f.raw
	opt.RoutingLine = routingLine(t.r)
	return client.Use(cfg.Broker, cfg.User, t.model, opt)
}

// ── roger config set-limit / show ───────────────────────────────────────────────────────

// setLimitFlags is the usage order `roger config set-limit` lists its flags in.
const setLimitFlags = "--max-in --max-out --min-tps --quant --pref --require --params --min-ctx --max-ttft --trust --self-hosted --region --max-cost"

// setLimitHelp is `roger config set-limit --help`.
const setLimitHelp = `usage: roger config set-limit <model|default> [flags]   (only the flags passed change)

  --max-out $       ` + maxOutHelp + `
                    (--max-out 0 clears the stored cap: the default $10/1M cap applies)
  --max-in $        input price cap; 0 = none
  --min-tps N       throughput floor in tok/s; 0 = none
  --quant labels    accepted quant labels (sent with "unknown" as a standing rule)
  --pref P          cheap, balanced, fast or reliable
  --require caps    tools, vision
  --params R        model size in billions: 7-70, 30, 30-, -8
  --min-ctx N       context window floor (32k = 32768)
  --max-ttft D      first-token ceiling (1500ms, 1.5s or bare ms)
  --trust T         any, verified or confidential
  --self-hosted B   true or false
  --region list     lowercase regions (eu,us)
  --max-cost $      per-request USD cap; 0 = no per-request cap
`

func cmdSetLimit(args []string) error {
	usage := fmt.Errorf("usage: roger config set-limit <model|default> " + setLimitFlags)
	if len(args) > 0 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help") {
		fmt.Print(setLimitHelp)
		return nil
	}
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return usage
	}
	model := args[0]
	c := loadConfig()
	var cur Limit
	if model == "default" {
		cur = c.Limits.Default
	} else if c.Limits.Models != nil {
		cur = c.Limits.Models[model]
	}
	type change struct{ name, raw string }
	var changes []change
	note := func(name, raw string) { changes = append(changes, change{name, raw}) }
	var maxOutCleared bool

	fs := flag.NewFlagSet("set-limit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Func("max-in", "", func(v string) error {
		var p priceFlag
		if err := p.Set(v); err != nil {
			return err
		}
		cur.MaxIn = p.v
		note("max_in", v)
		return nil
	})
	fs.Func("max-out", maxOutHelp, func(v string) error {
		var m maxOutFlag
		if err := m.Set(v); err != nil {
			return err
		}
		cur.MaxOut = m.v
		maxOutCleared = m.v == 0
		if !maxOutCleared {
			note("max_out", v)
		}
		return nil
	})
	fs.Func("min-tps", "", func(v string) error {
		var p priceFlag
		if err := p.Set(v); err != nil {
			return err
		}
		cur.MinTPS = p.v
		note("min_tps", v)
		return nil
	})
	fs.Func("quant", "", func(v string) error {
		l := listFlag{fold: true}
		if err := l.Set(v); err != nil {
			return err
		}
		cur.Quants = l.items
		note("quants", v)
		return nil
	})
	fs.Func("pref", "", func(v string) error {
		var p prefFlag
		if err := p.Set(v); err != nil {
			return err
		}
		cur.Pref = p.v
		note("pref", v)
		return nil
	})
	fs.Func("require", "", func(v string) error {
		l := listFlag{norm: normRequire}
		if err := l.Set(v); err != nil {
			return err
		}
		cur.Require = l.items
		note("require", v)
		return nil
	})
	fs.Func("params", "", func(v string) error {
		r, err := parseParams(v)
		if err != nil {
			return err
		}
		cur.ParamsB = r
		note("params_b", v)
		return nil
	})
	fs.Func("min-ctx", "", func(v string) error {
		n, err := parseCtx(v)
		if err != nil {
			return err
		}
		cur.MinCtx = n
		note("min_ctx", v)
		return nil
	})
	fs.Func("max-ttft", "", func(v string) error {
		n, err := parseTTFT(v)
		if err != nil {
			return err
		}
		cur.MaxTTFTMs = n
		note("max_ttft_ms", v)
		return nil
	})
	fs.Func("trust", "", func(v string) error {
		c := choiceFlag{choices: []string{"any", "verified", "confidential"}}
		if err := c.Set(v); err != nil {
			return err
		}
		cur.TrustMin = c.v
		note("trust_min", v)
		return nil
	})
	fs.Func("self-hosted", "", func(v string) error {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("%q is not true or false", v)
		}
		cur.SelfHosted = b
		note("self_hosted", v)
		return nil
	})
	fs.Func("region", "", func(v string) error {
		l := listFlag{norm: normRegion}
		if err := l.Set(v); err != nil {
			return err
		}
		cur.Region = l.items
		note("region", v)
		return nil
	})
	fs.Func("max-cost", "", func(v string) error {
		var p priceFlag
		if err := p.Set(v); err != nil {
			return err
		}
		cur.MaxCost = p.v
		note("max_cost", v)
		return nil
	})
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Print(setLimitHelp)
			return nil
		}
		return flagError("config set-limit", err)
	}
	if fs.NArg() > 0 {
		return usage
	}
	if model == "default" {
		c.Limits.Default = cur
	} else {
		if c.Limits.Models == nil {
			c.Limits.Models = map[string]Limit{}
		}
		c.Limits.Models[model] = cur
	}
	if err := saveConfig(c); err != nil {
		return err
	}
	if maxOutCleared {
		fmt.Printf("max_out cleared for %s (the $%g/1M default applies)\n", model, client.ConsumerDefaultMaxOut)
	}
	for _, ch := range changes {
		fmt.Printf("set %s %s = %s\n", model, ch.name, ch.raw)
	}
	return nil
}

// showKeys is `roger config show`'s key list: the limit-style name and the body key.
var showKeys = [][2]string{
	{"pref", "roger.pref"}, {"sort", "provider.sort"}, {"models", "models"}, {"only", "provider.only"},
	{"order", "provider.order"}, {"exclude", "provider.ignore"}, {"allow_fallbacks", "provider.allow_fallbacks"},
	{"quants", "provider.quantizations"}, {"require", "roger.require"}, {"params_b", "roger.params_b"},
	{"min_ctx", "roger.min_ctx"}, {"max_ttft_ms", "roger.max_ttft_ms"}, {"trust_min", "roger.trust_min"},
	{"self_hosted", "roger.self_hosted_only"}, {"region", "roger.region"}, {"confidential", "roger.confidential"},
	{"min_tps", "roger.min_tps"}, {"max_in", "provider.max_price.prompt"}, {"max_out", "provider.max_price.completion"},
	{"max_cost", "provider.max_price.request"}, {"freq", "roger.freq"},
}

// showValue renders one body value for show / profile show; a band code is hidden.
func showValue(key string, v any) string {
	if key == "roger.freq" {
		return "(set, hidden)"
	}
	switch t := v.(type) {
	case []any:
		parts := make([]string, len(t))
		for i, x := range t {
			parts[i] = fmt.Sprint(x)
		}
		return strings.Join(parts, ",")
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	}
	return fmt.Sprint(v)
}

// cmdConfigShow prints the effective routing for a model and each value's source.
func cmdConfigShow(args []string) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: roger config show <model> [--profile NAME]")
	}
	f, help, err := parseUseFlags(args)
	if help {
		return fmt.Errorf("usage: roger config show <model> [--profile NAME]")
	}
	if err != nil {
		return err
	}
	cfg := loadConfig()
	t, err := resolveUse(cfg, f, readProfiles())
	if err != nil {
		return err
	}
	fmt.Printf("effective routing for %s:\n", t.model)
	for _, k := range showKeys {
		v, ok := rfGetPath(t.r.body, k[1])
		switch {
		case ok && v != nil:
			fmt.Printf("  %s: %s (%s)\n", k[0], showValue(k[1], v), t.r.src[k[1]])
		case k[0] == "max_out":
			fmt.Printf("  %s: %g (built-in)\n", k[0], client.ConsumerDefaultMaxOut)
		case k[0] == "min_tps":
			fmt.Printf("  %s: - (unset, unmeasured stations pass)\n", k[0])
		default:
			fmt.Printf("  %s: - (unset)\n", k[0])
		}
	}
	return nil
}

// printLimits shows the limits section, one row per model in the routing-line vocabulary.
func printLimits(c config) {
	typ := c.Limits.TypicalOutTok
	if typ <= 0 {
		typ = 800
	}
	fmt.Printf("limits (typical reply ~%d out tokens):\n", typ)
	if len(c.Limits.Models) == 0 && c.Limits.Default.unset() {
		fmt.Println("  (none set - no caps; `roger config set-limit <model> --max-out P`)")
		return
	}
	models := make([]string, 0, len(c.Limits.Models))
	for m := range c.Limits.Models {
		models = append(models, m)
	}
	sort.Strings(models)
	for _, m := range models {
		fmt.Printf("  %-22s %s\n", m, limitSummary(c.Limits.Models[m]))
	}
	fmt.Printf("  %-22s %s\n", "· default (any other)", limitSummary(c.Limits.Default))
}

// limitSummary renders a stored limit in the routing-line vocabulary ("no caps" when empty).
// A rule's implicit "unknown" quant label is not shown: the row names what the user set. The
// pref reads "pref=<v>" and a cap at or above the ceiling reads as the ceiling
// (regression_pins.feature).
func limitSummary(l Limit) string {
	b := limitBody(l)
	if len(l.Quants) > 0 {
		rfSetPath(b, "provider.quantizations", anyList(l.Quants))
	}
	var parts []string
	for _, it := range routingItems(b) {
		switch it[0] {
		case "roger.pref":
			it[1] = "pref=" + it[1]
		case "provider.max_price.completion":
			if l.MaxOut >= client.ConsumerCeilingMaxOut {
				it[1] = fmt.Sprintf("out ≤ $%.0f/1M (network ceiling)", client.ConsumerCeilingMaxOut)
			}
		}
		parts = append(parts, it[1])
	}
	if len(parts) == 0 {
		return "no caps"
	}
	return strings.Join(parts, " · ")
}

// ── roger profile ───────────────────────────────────────────────────────────────────────

const profileUsage = "usage: roger profile list | show <name> | set <name> <key> <value> | unset <name> <key> | rm <name>"

func cmdProfile(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf(profileUsage)
	}
	switch args[0] {
	case "list", "ls":
		return profileList()
	case "show":
		if len(args) != 2 {
			return fmt.Errorf("usage: roger profile show <name>")
		}
		return profileShow(args[1])
	case "set":
		if len(args) != 4 {
			return fmt.Errorf("usage: roger profile set <name> <key> <value>  (e.g. roger.pref fast)")
		}
		return profileWrite(args[1], func(p map[string]any) error {
			if err := profileKeyOK(args[2]); err != nil {
				return err
			}
			var v any
			if json.Unmarshal([]byte(args[3]), &v) != nil {
				v = args[3]
			}
			rfSetPath(p, args[2], v)
			return nil
		}, fmt.Sprintf("set profile %s %s", args[1], args[2]))
	case "unset":
		if len(args) != 3 {
			return fmt.Errorf("usage: roger profile unset <name> <key>")
		}
		return profileWrite(args[1], func(p map[string]any) error {
			rfDelPath(p, args[2])
			return nil
		}, fmt.Sprintf("unset profile %s %s", args[1], args[2]))
	case "rm", "remove", "delete":
		if len(args) != 2 {
			return fmt.Errorf("usage: roger profile rm <name>")
		}
		if args[1] == client.DefaultProfileName {
			return fmt.Errorf("default is built from limits.*; use roger config clear-limit default")
		}
		return editConfigRaw(func(doc map[string]any) error {
			profs, _ := doc["profiles"].(map[string]any)
			if _, ok := profs[args[1]]; !ok {
				return fmt.Errorf("no profile named %s", args[1])
			}
			delete(profs, args[1])
			fmt.Printf("removed profile %s\n", args[1])
			return nil
		})
	}
	return fmt.Errorf(profileUsage)
}

// profileKeyOK accepts a routing key a profile may hold.
func profileKeyOK(key string) error {
	top, sub, _ := strings.Cut(key, ".")
	switch {
	case key == "model" || key == "models":
		return nil
	case (top == "provider" || top == "roger") && sub != "":
		return nil
	}
	return fmt.Errorf("%s is not a routing key (model, models, provider.<key>, roger.<key>)", key)
}

// profileWrite edits one profile under the config lock and refuses a result that does not
// validate (the file is then unchanged).
func profileWrite(name string, edit func(map[string]any) error, done string) error {
	if name == client.DefaultProfileName {
		return fmt.Errorf("profile default is reserved (it is built from limits.*): use roger config set-limit default")
	}
	if !client.ValidProfileName(name) {
		return fmt.Errorf("profile %q: invalid name (lowercase letters, digits, - and _)", name)
	}
	return editConfigRaw(func(doc map[string]any) error {
		profs, _ := doc["profiles"].(map[string]any)
		if profs == nil {
			profs = map[string]any{}
		}
		p, _ := profs[name].(map[string]any)
		if p == nil {
			p = map[string]any{}
		}
		if err := edit(p); err != nil {
			return err
		}
		enc, _ := json.Marshal(map[string]any{"profiles": map[string]any{name: p}})
		ps, err := client.ParseProfiles(enc)
		if err != nil {
			return err
		}
		if _, perr, _ := ps.Get(name); perr != nil {
			return perr
		}
		profs[name] = p
		doc["profiles"] = profs
		fmt.Println(done)
		return nil
	})
}

// editConfigRaw is a read-modify-write of config.json as raw JSON under an exclusive lock
// file, so two concurrent writers both land; every key edit does not touch keeps its value.
// The write is atomic (temp file + rename), like saveConfig.
func editConfigRaw(edit func(map[string]any) error) error {
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	release, err := lockConfig(path + ".lock")
	if err != nil {
		return err
	}
	defer release()
	doc := map[string]any{}
	if b, rerr := os.ReadFile(path); rerr == nil {
		if json.Unmarshal(b, &doc) != nil || doc == nil {
			return fmt.Errorf("%s is not valid JSON; fix or remove it first", path)
		}
	}
	if err := edit(doc); err != nil {
		return err
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteConfig(path, b)
}

// lockConfig takes an exclusive lock file (O_EXCL, portable), waiting up to 5 s; a lock
// older than 30 s is a crashed writer's and is taken over.
func lockConfig(path string) (func(), error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if fi, serr := os.Stat(path); serr == nil && time.Since(fi.ModTime()) > 30*time.Second {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("config.json is locked by another roger command (%s)", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// profileSummary is a profile's one-line list summary (the routing-line vocabulary).
func profileSummary(b map[string]any) string {
	var parts []string
	for _, it := range routingItems(b) {
		parts = append(parts, it[1])
	}
	if m, _ := b["model"].(string); m != "" {
		parts = append([]string{m}, parts...)
	}
	return strings.Join(parts, " · ")
}

func profileList() error {
	ps := readProfiles()
	cfg := loadConfig()
	if ps.SectionWarn != "" {
		fmt.Fprintln(os.Stderr, ps.SectionWarn)
	}
	if ps.ReservedSeen {
		fmt.Fprintln(os.Stderr, "profile default is reserved (it is built from limits.*)")
	}
	for _, n := range ps.BadNames {
		fmt.Fprintf(os.Stderr, "profile %q: invalid name (lowercase letters, digits, - and _)\n", n)
	}
	errNames := make([]string, 0, len(ps.Errs))
	for n := range ps.Errs {
		errNames = append(errNames, n)
	}
	sort.Strings(errNames)
	for _, n := range errNames {
		fmt.Fprintln(os.Stderr, ps.Errs[n])
	}
	def := mergeLayers(defaultLayers(cfg, "")...)
	if !hasPath(def.body, "provider.max_price.completion") {
		rfSetPath(def.body, "provider.max_price.completion", client.ConsumerDefaultMaxOut)
	}
	fmt.Printf("%-9s built from limits.* (%s)\n", client.DefaultProfileName, profileSummary(def.body))
	for _, n := range ps.Names() {
		p, _, _ := ps.Get(n)
		fmt.Printf("%-9s %s\n", n, profileSummary(p.Body))
	}
	return nil
}

func profileShow(name string) error {
	cfg := loadConfig()
	layers := defaultLayers(cfg, "")
	var prof *client.Profile
	if name != client.DefaultProfileName {
		ps := readProfiles()
		p, perr, ok := ps.Get(name)
		if perr != nil {
			return perr
		}
		if !ok {
			return fmt.Errorf("no profile named %s (roger profile list)", name)
		}
		prof = p
		layers = append(layers, routingLayer{label: "profile " + name, body: p.Body})
	}
	r := mergeLayers(layers...)
	shown := hideFreq(r.body)
	b, _ := json.MarshalIndent(shown, "", "  ")
	if prof == nil {
		fmt.Println("default (built from limits.default)")
	} else {
		fmt.Printf("profile %s\n", name)
	}
	fmt.Println(string(b))
	fmt.Println("keys (precedence: flag > profile > limits.models.<m> > limits.default > built-in):")
	for _, leaf := range routingLeaves(r.body) {
		v, _ := rfGetPath(r.body, leaf)
		fmt.Printf("  %s = %s  (%s)\n", leaf, showValue(leaf, v), r.src[leaf])
	}
	if !hasPath(r.body, "provider.max_price.completion") {
		fmt.Printf("  provider.max_price.completion = %g  (built-in)\n", client.ConsumerDefaultMaxOut)
	}
	if prof != nil {
		if len(prof.NonRouting) > 0 {
			fmt.Printf("ignored keys (not routing): %s\n", strings.Join(prof.NonRouting, ", "))
		}
		if len(prof.Unknown) > 0 {
			fmt.Printf("ignored keys (unknown): %s\n", strings.Join(prof.Unknown, ", "))
		}
	}
	return nil
}

// hideFreq is the body with a band code replaced by "(set, hidden)".
func hideFreq(b map[string]any) map[string]any {
	enc, _ := json.Marshal(b)
	var out map[string]any
	_ = json.Unmarshal(enc, &out)
	if hasPath(out, "roger.freq") {
		rfSetPath(out, "roger.freq", "(set, hidden)")
	}
	return out
}

// ── small JSON path helpers ─────────────────────────────────────────────────────────────

func rfGetPath(m map[string]any, path string) (any, bool) {
	var cur any = m
	for _, k := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = obj[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func hasPath(m map[string]any, path string) bool {
	v, ok := rfGetPath(m, path)
	return ok && v != nil
}

func rfSetPath(m map[string]any, path string, v any) {
	keys := strings.Split(path, ".")
	cur := m
	for _, k := range keys[:len(keys)-1] {
		next, ok := cur[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[k] = next
		}
		cur = next
	}
	cur[keys[len(keys)-1]] = v
}

// rfDelPath removes a dotted key and any parent object it leaves empty.
func rfDelPath(m map[string]any, path string) {
	keys := strings.Split(path, ".")
	if len(keys) == 1 {
		delete(m, path)
		return
	}
	child, ok := m[keys[0]].(map[string]any)
	if !ok {
		return
	}
	rfDelPath(child, strings.Join(keys[1:], "."))
	if len(child) == 0 {
		delete(m, keys[0])
	}
}

func anyList(xs []string) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

func strList(v any) []string {
	a, _ := v.([]any)
	out := make([]string, 0, len(a))
	for _, x := range a {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
