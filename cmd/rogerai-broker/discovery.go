package main

// discovery.go - the filter params of the public reads /discover, /market and /v1/models
// (ROUTING-EXPRESSION-CONTRACT §8, features/discovery/filters.feature). A filter has the
// relay's meaning (§5): a declared attribute an offer does not declare drops it, a measured
// one never measured passes. Filters run AFTER the cache: the cache holds the unfiltered
// feed, so a hundred filter combinations are one compute, and the filter pass over the
// cached offers takes no broker lock.

import (
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"rogerai.fm/roger/v6/internal/protocol"
)

// discoverCacheKey is the ONE shared cache entry for the public offer feed.
const discoverCacheKey = "discover:"

// offerFilter is a parsed filter query. The zero value keeps every offer.
type offerFilter struct {
	set                  bool // any filter param was given
	model                string
	minTPS, maxTTFT      float64
	maxIn, maxOut        *float64
	paramsMin, paramsMax float64
	minCtx               int
	regions, quants      map[string]bool
	caps                 []string
	selfHosted, conf     bool
	free, verified       bool
}

// queryError is a 400 a filter query earns: unknown_query_param or invalid_query_param.
type queryError struct{ code, msg string }

var regionQueryRe = regionToken

// parseOfferFilter reads the filter params. Every param must be known and non-empty; a
// repeated single-valued param takes the last value (Go's URL query rule).
func parseOfferFilter(q url.Values) (offerFilter, *queryError) {
	var f offerFilter
	invalid := func(name, why string) *queryError {
		return &queryError{code: "invalid_query_param", msg: "invalid value for " + name + ": " + why}
	}
	num := func(name string, vals []string, ok func(float64) bool, why string) (float64, *queryError) {
		v, err := strconv.ParseFloat(vals[len(vals)-1], 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || !ok(v) {
			return 0, invalid(name, why)
		}
		return v, nil
	}
	flag := func(name string, vals []string) (bool, *queryError) {
		switch vals[len(vals)-1] {
		case "1", "true":
			return true, nil
		}
		return false, invalid(name, "want 1 or true")
	}
	for name, vals := range q {
		for _, v := range vals {
			if v == "" {
				return f, invalid(name, "empty value")
			}
		}
		f.set = true
		var qe *queryError
		switch name {
		case "model":
			f.model = vals[len(vals)-1]
		case "min_tps":
			f.minTPS, qe = num(name, vals, func(v float64) bool { return v >= 0 }, "want a number >= 0")
		case "max_ttft_ms":
			f.maxTTFT, qe = num(name, vals, func(v float64) bool { return v > 0 }, "want a number > 0")
		case "max_price_in", "max_price_out":
			var v float64
			v, qe = num(name, vals, func(v float64) bool { return v >= 0 }, "want a price >= 0")
			if name == "max_price_in" {
				f.maxIn = &v
			} else {
				f.maxOut = &v
			}
		case "params_min":
			f.paramsMin, qe = num(name, vals, func(v float64) bool { return v > 0 }, "want billions > 0")
		case "params_max":
			f.paramsMax, qe = num(name, vals, func(v float64) bool { return v > 0 }, "want billions > 0")
		case "min_ctx":
			var v float64
			v, qe = num(name, vals, func(v float64) bool { return v > 0 && v == math.Trunc(v) && v <= math.MaxInt32 }, "want a positive integer")
			f.minCtx = int(v)
		case "region":
			f.regions = map[string]bool{}
			for _, v := range vals {
				if !regionQueryRe.MatchString(v) {
					return f, invalid(name, "want lowercase tokens of 2-8 letters")
				}
				f.regions[v] = true
			}
		case "quant":
			f.quants = map[string]bool{}
			for _, v := range vals {
				c := strings.ToLower(protocol.CanonicalQuant(v))
				if c == "" {
					return f, invalid(name, "empty label")
				}
				f.quants[c] = true
			}
		case "capability":
			for _, v := range vals {
				if canon := protocol.CanonicalCapabilities([]string{v}); len(canon) != 1 || canon[0] != v {
					return f, invalid(name, "want tools or vision")
				}
				f.caps = append(f.caps, v)
			}
		case "self_hosted":
			f.selfHosted, qe = flag(name, vals)
		case "confidential":
			f.conf, qe = flag(name, vals)
		case "free":
			f.free, qe = flag(name, vals)
		case "trust_min":
			switch vals[len(vals)-1] {
			case "any":
			case "verified":
				f.verified = true
			case "confidential":
				f.conf = true
			default:
				qe = invalid(name, "want any, verified or confidential")
			}
		default:
			return f, &queryError{code: "unknown_query_param", msg: "unknown query parameter " + name}
		}
		if qe != nil {
			return f, qe
		}
	}
	if f.paramsMin > 0 && f.paramsMax > 0 && f.paramsMin > f.paramsMax {
		return f, invalid("params_min", "above params_max")
	}
	return f, nil
}

// keeps reports whether an offer survives the filter.
func (f offerFilter) keeps(o offerView) bool {
	switch {
	case f.model != "" && o.Model != f.model:
		return false
	case f.minTPS > 0 && o.TPS > 0 && o.TPS < f.minTPS:
		return false
	case f.maxTTFT > 0 && o.TTFTMs > 0 && o.TTFTMs > f.maxTTFT:
		return false
	case f.maxIn != nil && o.In > *f.maxIn, f.maxOut != nil && o.Out > *f.maxOut:
		return false
	case (f.paramsMin > 0 || f.paramsMax > 0) && o.ParamsB <= 0:
		return false
	case f.paramsMin > 0 && o.ParamsB < f.paramsMin, f.paramsMax > 0 && o.ParamsB > f.paramsMax:
		return false
	case f.minCtx > 0 && (o.CtxEstimated || o.Ctx < f.minCtx):
		return false
	case f.regions != nil && !f.regions[strings.ToLower(strings.TrimSpace(o.Region))]:
		return false
	case f.quants != nil && !f.quants[strings.ToLower(o.Quant)]:
		return false
	case f.selfHosted && o.Curated, f.conf && !o.Confidential, f.free && !(o.FreeNow || (o.In == 0 && o.Out == 0)), f.verified && !o.Verified:
		return false
	}
	for _, c := range f.caps {
		if !slices.Contains(o.Capabilities, c) {
			return false
		}
	}
	return true
}

// cachedOffers is the unfiltered public offer feed out of the shared cache (one compute per
// publicMarketTTL across every caller and filter), decoded without taking a broker lock.
func (b *broker) cachedOffers() []offerView {
	var feed struct {
		Offers []offerView `json:"offers"`
	}
	_ = json.Unmarshal(b.cachedJSON(discoverCacheKey, publicMarketTTL, b.computeDiscover), &feed)
	return feed.Offers
}

// filterOffers applies the filter, preserving the feed's order.
func (f offerFilter) filterOffers(in []offerView) []offerView {
	out := make([]offerView, 0, len(in))
	for _, o := range in {
		if f.keeps(o) {
			out = append(out, o)
		}
	}
	return out
}

// readFilter parses the request's filter query, answering the 400 itself (with the CORS
// headers already set by the caller) when it is malformed.
func readFilter(w http.ResponseWriter, r *http.Request) (offerFilter, bool) {
	f, qe := parseOfferFilter(r.URL.Query())
	if qe != nil {
		jsonErrCode(w, http.StatusBadRequest, qe.code, qe.msg)
		return f, false
	}
	return f, true
}
