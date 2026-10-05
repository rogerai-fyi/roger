package main

// models.go serves GET /v1/models (and /v1/models/{id}): the OpenAI-shaped catalog every SDK
// probes at startup, one entry per distinct CHAT model id with a public offer ON AIR
// (features/discovery/models_endpoint.feature, ROUTING-EXPRESSION-CONTRACT §8). It is a
// collapse of the same cached /discover feed - the same liveness, ban and private-band rules
// by construction - and it honors the same filter params, applied after the cache. Public
// read: the same CORS + cache posture as /market, no per-IP gate.

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type modelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
	// The OpenRouter-compatible top-level fields SDKs read (contract §14.B4).
	ContextLength       int                 `json:"context_length,omitempty"` // max DECLARED window
	Pricing             map[string]string   `json:"pricing,omitempty"`        // per-token USD, ONE offer
	SupportedParameters []string            `json:"supported_parameters,omitempty"`
	Architecture        map[string][]string `json:"architecture,omitempty"`
	RogerAI             map[string]any      `json:"rogerai,omitempty"`
}

func (b *broker) models(w http.ResponseWriter, r *http.Request) {
	if corsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	cors(w)
	f, ok := readFilter(w, r)
	if !ok {
		return
	}
	id, one := strings.CutPrefix(r.URL.Path, "/v1/models/")
	if !one && !f.set {
		b.serveCachedJSON(w, "models", publicMarketTTL, b.computeModels)
		return
	}
	// A filter, or one id: collapsed from the cached feed (filtered), with each model's
	// created read off the cached catalog - never a per-hit recompute of the discover view.
	data := b.collapseModels(f.filterOffers(b.cachedOffers()), b.catalogCreated())
	if !one {
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
		return
	}
	for _, e := range data {
		if e.ID == id {
			writeJSON(w, http.StatusOK, e)
			return
		}
	}
	jsonErrCode(w, http.StatusNotFound, "model_not_found", "no public station is on air for that model")
}

// computeModels is the unfiltered catalog (the cached "models" entry).
func (b *broker) computeModels() any {
	offers, _ := b.computeDiscover().(map[string]any)["offers"].([]offerView)
	now := time.Now().Unix()
	return map[string]any{"object": "list", "data": b.collapseModels(offers, func(id string) int64 { return b.firstSeenModel(id, now) })}
}

// catalogCreated reads each model's created off the cached unfiltered catalog.
func (b *broker) catalogCreated() func(string) int64 {
	var list struct {
		Data []modelEntry `json:"data"`
	}
	_ = json.Unmarshal(b.cachedJSON("models", publicMarketTTL, b.computeModels), &list)
	created := make(map[string]int64, len(list.Data))
	for _, e := range list.Data {
		created[e.ID] = e.Created
	}
	now := time.Now().Unix()
	return func(id string) int64 {
		if t, ok := created[id]; ok {
			return t
		}
		return b.firstSeenModel(id, now)
	}
}

// modelAgg accumulates one model's rogerai block over its on-air chat offers.
type modelAgg struct {
	providers, curated                int
	minIn, minOut, bestTPS, blend     float64 // blend: the lowest single-station blended price
	priced                            bool
	ctxMax                            int
	declared, estimated               []float64
	caps, quants                      map[string]bool
	free, confidential, verified, all bool // all: every offer cooling
	coolUntil                         int64
	priceIn, priceOut, priceKey       float64 // the coherent pricing offer: lowest blended cost, first wins a tie
}

// collapseModels collapses offers into catalog entries, sorted by id.
func (b *broker) collapseModels(offers []offerView, created func(string) int64) []modelEntry {
	byID := map[string]*modelAgg{}
	var ids []string
	for _, o := range offers {
		if !o.Online || o.Modality != "chat" {
			continue // a voice offer is not a chat model
		}
		a := byID[o.Model]
		if a == nil {
			a = &modelAgg{caps: map[string]bool{}, quants: map[string]bool{}, all: true}
			byID[o.Model] = a
			ids = append(ids, o.Model)
		}
		if o.Curated {
			a.curated++
		} else {
			a.providers++
		}
		if bl := blendedPrice(o.In, o.Out); !a.priced || bl < a.blend {
			a.blend = bl // one station's in AND out, never one's in with another's out
		}
		in, out := o.In, o.Out
		if o.FreeNow {
			in, out = 0, 0 // an active free window prices the offer at nothing right now
		}
		if k := blendedPrice(in, out); !a.priced || k < a.priceKey {
			a.priceIn, a.priceOut, a.priceKey = in, out, k // strict: a tie keeps the earlier /discover offer
		}
		if !a.priced || o.In < a.minIn {
			a.minIn = o.In
		}
		if !a.priced || o.Out < a.minOut {
			a.minOut = o.Out
		}
		a.priced = true
		if o.TPS > a.bestTPS {
			a.bestTPS = o.TPS
		}
		if !o.CtxEstimated && o.Ctx > a.ctxMax {
			a.ctxMax = o.Ctx
		}
		if o.ParamsEstimated != nil {
			if *o.ParamsEstimated {
				a.estimated = append(a.estimated, o.ParamsB)
			} else {
				a.declared = append(a.declared, o.ParamsB)
			}
		}
		for _, c := range o.Capabilities {
			a.caps[c] = true
		}
		if o.Quant != "" {
			a.quants[o.Quant] = true
		}
		a.free = a.free || o.FreeNow || (o.In == 0 && o.Out == 0)
		a.confidential = a.confidential || o.Confidential
		a.verified = a.verified || o.Verified
		if o.CoolingUntil == 0 {
			a.all = false
		} else if a.coolUntil == 0 || o.CoolingUntil < a.coolUntil {
			a.coolUntil = o.CoolingUntil
		}
	}
	sort.Strings(ids)
	data := make([]modelEntry, 0, len(ids))
	for _, id := range ids {
		a := byID[id]
		block := map[string]any{
			"providers": a.providers, "curated": a.curated,
			"min_price_in": a.minIn, "min_price_out": a.minOut, "best_tps": a.bestTPS,
			"blended_price_per_1m": a.blend, "blend_ratio": blendLabel(),
			"free_now": a.free, "confidential": a.confidential, "verified": a.verified,
			"quants": sortedKeys(a.quants), "cooling": a.all,
		}
		if a.all {
			block["cooling_until"] = a.coolUntil
		}
		if a.ctxMax > 0 {
			block["ctx_max"] = a.ctxMax // DECLARED windows only
		}
		// params_b: the stations' declarations when any declares one, else the estimate.
		if vals, est := a.declared, false; len(vals) > 0 || len(a.estimated) > 0 {
			if len(vals) == 0 {
				vals, est = a.estimated, true
			}
			lo, hi := vals[0], vals[0]
			for _, v := range vals {
				lo, hi = min(lo, v), max(hi, v)
			}
			block["params_b"], block["params_estimated"] = hi, est
			if lo != hi {
				block["params_b_min"], block["params_b_max"] = lo, hi
			}
		}
		if len(a.caps) > 0 {
			block["capabilities"] = sortedKeys(a.caps)
		}
		params := []string{"max_tokens", "temperature", "top_p", "stop", "seed", "stream"}
		if a.caps["tools"] { // the canary-verified bit only (a declared "tools" is stripped at register)
			params = append(params, "tools", "tool_choice", "response_format")
		}
		input := []string{"text"}
		if a.caps["vision"] {
			input = append(input, "image")
		}
		data = append(data, modelEntry{
			ID: id, Object: "model", OwnedBy: "rogerai", Created: created(id), RogerAI: block,
			ContextLength:       a.ctxMax,
			Pricing:             map[string]string{"prompt": perTokenUSD(a.priceIn), "completion": perTokenUSD(a.priceOut)},
			SupportedParameters: params,
			Architecture:        map[string][]string{"input_modalities": input, "output_modalities": {"text"}},
		})
	}
	return data
}

// perTokenUSD renders a $/1M price as OpenRouter's per-token decimal string: the shortest
// decimal of the $/1M figure with its point moved six places left, so no float division
// adds noise and no exponent appears ("0.1" -> "0.0000001", 0 -> "0").
func perTokenUSD(perM float64) string {
	if perM <= 0 {
		return "0"
	}
	digits := strconv.FormatFloat(perM, 'f', -1, 64)
	whole, frac, _ := strings.Cut(digits, ".")
	whole = strings.Repeat("0", max(0, 6-len(whole))) + whole
	cut := len(whole) - 6
	out := strings.TrimLeft(whole[:cut], "0")
	if out == "" {
		out = "0"
	}
	if f := strings.TrimRight(whole[cut:]+frac, "0"); f != "" {
		out += "." + f
	}
	return out
}

// blendRatio is ROGERAI_BLEND_INPUT_RATIO (input tokens per output token, default 3), read
// per call like the other pricing knobs.
func blendRatio() float64 {
	if v, err := strconv.ParseFloat(os.Getenv("ROGERAI_BLEND_INPUT_RATIO"), 64); err == nil && v > 0 && !math.IsInf(v, 0) {
		return v
	}
	return 3
}

// blendedPrice is one offer's $/1M at the blend ratio: (r*in + out) / (r+1).
func blendedPrice(in, out float64) float64 {
	r := blendRatio()
	return (r*in + out) / (r + 1)
}

func blendLabel() string { return strconv.FormatFloat(blendRatio(), 'g', -1, 64) + ":1 input:output" }

// firstSeenModel is the unix time a model FIRST came on air: a shared-store record (the same
// on every broker instance), written once and read back; the in-memory map serves a
// single-instance broker without a shared store, or one whose store is unreachable.
func (b *broker) firstSeenModel(id string, now int64) int64 {
	if b.shared != nil {
		key := "model_first_seen:" + id
		if _, err := b.shared.setIfAbsent(key, strconv.FormatInt(now, 10), 0); err == nil {
			if v, found, err := b.shared.counterGet(key); err == nil && found {
				return int64(v)
			}
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if t, ok := b.modelFirstSeen[id]; ok {
		return t
	}
	if b.modelFirstSeen == nil {
		b.modelFirstSeen = map[string]int64{}
	}
	b.modelFirstSeen[id] = now
	return now
}
