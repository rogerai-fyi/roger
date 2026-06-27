package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// External same-model reference prices: what the SAME open model costs on a popular
// commercial aggregator (OpenRouter), used as the preferred baseline for the price tier
// (see pricetier.go / features/pricing/price_tier.feature). Synced best-effort on a slow
// cadence (these change rarely); the last-known value is kept on any fetch failure and a
// static seed covers the pre-first-sync / offline case. Per-instance + idempotent public
// data, so each broker fetches independently (no shared-store coordination needed).

// refPriceSeed is the static, pre-first-sync fallback: public same-model reference
// OUT-prices ($/1M tokens) for common OPEN models. Keys are NORMALIZED model names. This
// is the per-OPEN-model analogue of metrics_series.go's (cross-model) frontierTable.
// Tunable; kept short and in one place.
var refPriceSeed = map[string]float64{
	"qwen3-8b":               0.20,
	"llama-3.1-8b":           0.06,
	"llama-3.3-70b-instruct": 0.40,
	"mixtral-8x7b":           0.24,
	"gpt-oss-120b":           0.60,
	"deepseek-r1":            0.80,
}

// refPriceSyncInterval is how often the external prices are refreshed. A var so it is
// tunable (and shortenable in a test). Slow by design — commercial list prices are sticky.
var refPriceSyncInterval = 12 * time.Hour

// normalizeModelName collapses an OpenRouter "vendor/model" id and an operator's
// free-text model name to one lookup key: lowercased, the vendor prefix before the last
// "/" dropped, any ":variant" suffix (":free", ":nitro") dropped, trimmed.
func normalizeModelName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// parseOpenRouterModels parses the public GET /api/v1/models payload into a normalized
// model -> OUT-price ($/1M) map. OpenRouter quotes pricing.completion in $/TOKEN, so it
// is scaled by 1e6. Entries with a missing / zero / unparseable completion price are
// skipped (never store a 0 reference that would mislabel a band as $$$$).
func parseOpenRouterModels(body []byte) (map[string]float64, error) {
	var doc struct {
		Data []struct {
			ID      string `json:"id"`
			Pricing struct {
				Completion string `json:"completion"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(doc.Data))
	for _, m := range doc.Data {
		perTok, err := strconv.ParseFloat(strings.TrimSpace(m.Pricing.Completion), 64)
		if err != nil || perTok <= 0 {
			continue
		}
		if name := normalizeModelName(m.ID); name != "" {
			out[name] = perTok * 1e6
		}
	}
	return out, nil
}

// openRouterFetch fetches the public models list. It is a package var (default: a real
// HTTP GET) ONLY so tests can drive the sync without a live network call — production
// behaviour is unchanged.
var openRouterFetch = func(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter models: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// refOut returns the same-model external reference OUT-price ($/1M) and whether one is
// known: the freshest synced value, else the static seed. Concurrency-safe.
func (b *broker) refOut(model string) (float64, bool) {
	key := normalizeModelName(model)
	b.refMu.RLock()
	v, ok := b.refPrices[key]
	b.refMu.RUnlock()
	if ok && v > 0 {
		return v, true
	}
	if s, ok := refPriceSeed[key]; ok && s > 0 {
		return s, true
	}
	return 0, false
}

// syncRefPricesOnce fetches + parses the external model prices and MERGES them into the
// live map. Best-effort: any fetch/parse error (or an empty result) leaves the last-known
// map untouched, so classification never depends on a live fetch. Returns the merged size.
func (b *broker) syncRefPricesOnce(ctx context.Context) int {
	body, err := openRouterFetch(ctx)
	if err != nil {
		return 0
	}
	m, err := parseOpenRouterModels(body)
	if err != nil || len(m) == 0 {
		return 0
	}
	b.refMu.Lock()
	if b.refPrices == nil {
		b.refPrices = make(map[string]float64, len(m))
	}
	for k, v := range m {
		b.refPrices[k] = v
	}
	n := len(b.refPrices)
	b.refMu.Unlock()
	return n
}

// refPriceSync primes the external reference prices on boot, then refreshes them on a
// slow ticker (best-effort). Stop-channel pattern: production passes nil (the loop runs
// until process exit); a closed channel returns at once. A nil channel never fires, so
// the production behaviour is the bare ticker loop.
func (b *broker) refPriceSync(stop <-chan struct{}) {
	prime, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	b.syncRefPricesOnce(prime)
	cancel()
	t := time.NewTicker(refPriceSyncInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			ctx, c := context.WithTimeout(context.Background(), 30*time.Second)
			b.syncRefPricesOnce(ctx)
			c()
		}
	}
}
