// Package tui is the interactive `rogerai` experience - a two-way radio for Local Models,
// and the terminal twin of the website's "Live Operating Manual". Stations
// (providers) go on air; you tune in to a channel and talk. The look is the web's:
// ~95% monochrome + ONE red beacon, the shared instrument glyphs (◉ on air, ○ off
// air, ◆ verified, ▁▂▃▄▅▆▇█ signal bars), flat hairline structure, and a single
// carrier beat driving the beacon, the ((•)) spinner, and the signal-bar shimmer.
// Built on Bubble Tea + Lipgloss.
package tui

import (
	"fmt"
	"sort"
	"strings"

	"rogerai.fm/roger/v6/internal/glyphs"
	"rogerai.fm/roger/v6/internal/pricetier"
)

// BandRow is a compact private-band summary for BASE STATION (metadata only, no secret).
// NodeID ("<station>-<model>") is what lets the list say WHICH model - and which machine -
// a band is on: the fact an operator otherwise has no way to discover.
type BandRow struct {
	ID, Display, Label, Status string
	NodeID                     string
}

// signalTerms mirrors the broker's per-factor signal breakdown (cmd/rogerai-broker
// market.go) so the TUI can decode + render the "why is this a 71?" detail. Each
// field is the term's point contribution to the 0..100 signal.
type signalTerms struct {
	Supply     float64 `json:"supply"`
	Speed      float64 `json:"speed"`
	Latency    float64 `json:"latency"`
	Verified   float64 `json:"verified"`
	Success    float64 `json:"success"`
	Trust      float64 `json:"trust"`
	Congestion float64 `json:"congestion"`
	Total      int     `json:"total"`
}

// band is one model grouped across stations, with its live cross-station
// out-price range (semantics A in the design doc).
type band struct {
	model string
	// quant is the compression label every station in this band is running ("Q4_K_M",
	// "IQ4_XS", ""). It is part of the band's IDENTITY, not a detail of it: bands are
	// grouped by (model, quant), so every station here is running the same weights and
	// tuning the row can never land on a different quant than the one displayed. Empty
	// means the stations did not state one - an absence, rendered as absent.
	quant string
	// modality is what the band DOES, canonical: "chat" (the back-compat default), "tts"
	// (speak), or "stt" (listen). A band groups offers of ONE model, which share a modality;
	// groupBands sets it. isVoice() (tts/stt) drives BOTH the separate "Voices" section in the
	// browser AND the preview-instead-of-chat divert in connect().
	modality string
	stations int     // online stations serving it
	minIn    float64 // cheapest active in-price now (the headline $/1M in, mirrors the web)
	minOut   float64 // cheapest active out-price now
	maxOut   float64 // priciest active out-price now
	cheapest *offer  // the station at minOut (broker's default route)
	online   bool    // any station on air
	free     bool    // any station FREE now
	lineage  int     // count of confidential/lineage stations
	verified bool    // any ONLINE station passed the broker's serving probe (✓, distinct from ◆)
	vision   bool    // any station DECLARED the "vision" capability (◪; never inferred)
	tools    bool    // any station carries the broker-VERIFIED "tools" capability (agent-ready ⌁,
	// no tilde). Unlike vision it is verified-not-declared: the broker only emits "tools" on an
	// offer after its tool-call canary passed, so a node can never fake it. Absence => inferred
	// (⌁~), never a false "no tools". See features/trust/toolcall_probe.feature.
	inFlight int // active (in-flight) requests summed across online stations - the REAL
	// activity that animates the signal meter (idle band steady, busy band scans). Honest:
	// it is the broker's live load, never a fabricated pulse.
	all []offer // every station in this band (online first)
}

// confirmView is the connect-time cost confirmation (3.2): the deal + an explicit
// accept/deny with the SAFE default on DENY.
// bandDetailView is the TUI's QSL-equivalent: the expanded per-station log for one
// band. It lists every station - callsign · coarse region · ◆/✓ marks · $in·out · t/s ·
// ttft · success% (or "no data") · hw-class - column-aligned in the monochrome+one-red
// language, plus a signal-TERM breakdown line (supply/speed/latency/verified/success/
// trust) from the strongest station's offer.Terms so a user sees WHY the band scores
// what it does. Honest-empty + privacy-bucket rules apply throughout (the same data the
// web /models QSL card shows, so CLI and web agree).
func (m model) bandDetailView(w int) string {
	bd := m.detailBand
	var b strings.Builder

	// Section-tab heading, matching the TUNE IN / SHARE look.
	bctx, bctxEst := bandCtx(bd)
	ctxTag := ""
	if bctx > 0 {
		if bctxEst {
			ctxTag = stDim.Render("  ~" + fmtCtx(bctx) + " ctx")
		} else {
			ctxTag = stDim.Render("  ") + stEmber.Render(fmtCtx(bctx)+" ctx")
		}
	}
	on := stDim.Render("offline")
	if bd.online {
		on = stLive.Render(fmt.Sprintf("%d on air", bd.stations))
	}
	// Clamped: the header and the empty state both ran off a narrow or minimized
	// terminal (the compact audit). The empty state also drops to its keys on a slim
	// width - the two keys ARE the message there, the sentence around them is not.
	b.WriteString("  " + truncVisible(stSelBar.Render("▌")+" "+stBrand.Render("STATION LOG")+
		stDim.Render("   ")+stKey.Render(bd.model)+stDim.Render(" · ")+on+ctxTag, w-2) + "\n\n")

	if len(bd.all) == 0 {
		empty := "no station detail for this band right now - r to re-scan, esc to go back"
		if w < 80 {
			empty = "no station detail - r re-scan · esc back"
		}
		b.WriteString("  " + truncVisible(stDim.Render(empty), w-2) + "\n")
		return b.String()
	}

	// Column header, tabular - widths match the body cells exactly so every column lines
	// up under a fixed grid. callsign · region · marks · $in·out · t/s · ttft · ok · hw.
	hdr := fmt.Sprintf("  %-14s  %-5s  %-3s  %-13s  %-7s  %-7s  %-7s  %s",
		"callsign", "rgn", "", "$/M in·out", "t/s", "ttft", "ok", "hw")
	b.WriteString("  " + stDim.Render(hdr) + "\n")

	// Stations: online first (bd.all is already online-first from groupBands), each on one
	// aligned row. The cheapest station (the broker's default route) is marked with the
	// lit ◉; the rest with a hollow ○ / dim offline dot.
	// BOUNDED TO THE TERMINAL. This listed every station unconditionally, so a popular
	// band emitted a frame taller than the screen - and a frame taller than the screen
	// SCROLLS the alt buffer, leaving the previous frame's top stranded above it. That
	// is the stack of ROGER logos and repeated STATION LOG lines the founder hit by
	// pressing i and esc (each press left another header behind).
	//
	// The chrome below is the header, the column head, the two footer lines and the
	// rule; 10 rows is that with a row to spare.
	// bandDetailChrome is every row this view emits that is NOT a station: the app
	// header and preset bar, the STATION LOG line and its blank, the column head, the
	// terms breakdown and its blanks, the legend, and the footer. MEASURED at 17 (a
	// 24-row terminal fit 14 stations in a 31-row frame before this bound was right),
	// with one row of slack.
	const bandDetailChrome = 18
	shown := bd.all
	if m.height > 0 {
		if room := m.height - bandDetailChrome; room > 0 && len(shown) > room {
			shown = shown[:room]
		} else if room <= 0 && len(shown) > 1 {
			shown = shown[:1] // a very short terminal still shows the default route
		}
	}
	for i := range shown {
		o := shown[i]
		dot := stDim.Render("○")
		if o.Online {
			dot = stRed.Render(glyphOnAir)
		}
		// confidential ◆ and verified ✓ are DISTINCT marks (the codebase's split).
		marks := ""
		if o.Confidential {
			marks += stGold.Render(glyphConf)
		}
		if o.Online && o.Verified {
			marks += stGold.Render(glyphLineage)
		}
		if marks == "" {
			marks = stDim.Render("·")
		}
		priceCell := stEmber.Render(money(o.PriceIn) + "·" + money(o.PriceOut))
		if o.FreeNow || (o.PriceIn == 0 && o.PriceOut == 0) {
			priceCell = stLive.Render("free")
		}
		tpsTxt := "-"
		if o.Online && o.TPS > 0 {
			tpsTxt = fmt.Sprintf("%d", int(o.TPS+0.5))
		}
		call := pad("@"+o.NodeID, 14)
		row := "  " + dot + " " + stKey.Render(call) + "  " +
			stDim.Render(pad(regionCell(o.Region), 5)) + "  " +
			pad(marks, 3) + "  " +
			pad(priceCell, 13) + "  " +
			stDim.Render(pad(tpsTxt, 7)) + "  " +
			stDim.Render(pad(fmtTtft(o.TTFTMs), 7)) + "  " +
			pad(successCell(o.SuccessRate, o.SuccessSeen), 7) + "  " +
			stDim.Render(hwLabelOr(o.HW))
		b.WriteString(row + "\n")
	}
	// SAY WHAT WAS DROPPED. A list silently cut at the terminal's height reads as the
	// whole list, and an operator counting stations would be counting wrong.
	if n := len(bd.all) - len(shown); n > 0 {
		b.WriteString("  " + stDim.Render(fmt.Sprintf("… %d more station(s) - widen or resize to see them", n)) + "\n")
	}

	// Signal-term breakdown: WHY the band scores what it does. Use the strongest online
	// station's broker Terms (the cheapest route is the default; fall back to the first
	// online station with a non-empty breakdown). Honest-empty when nothing is on air.
	terms, sig, haveTerms := bd.termsBreakdown()
	b.WriteString("\n")
	if haveTerms {
		line := fmt.Sprintf("supply %d · speed %d · latency %d · verified %d · success %d · trust %d",
			rnd(terms.Supply), rnd(terms.Speed), rnd(terms.Latency),
			rnd(terms.Verified), rnd(terms.Success), rnd(terms.Trust))
		cong := ""
		if terms.Congestion > 0 {
			cong = stDim.Render(fmt.Sprintf("  (−%d%% congestion)", int(terms.Congestion*40+0.5)))
		}
		b.WriteString("  " + stDim.Render("signal ") + stKey.Render(fmt.Sprintf("%d", sig)) +
			stDim.Render("/100  =  ") + stDim.Render(line) + cong + "\n")
	} else {
		b.WriteString("  " + stDim.Render("signal breakdown - no live station to score (offline)") + "\n")
	}

	b.WriteString("\n")
	b.WriteString("       " + stLive.Render("enter · tune in") + "     " + stDim.Render("esc / ← · back") + "     " + stDim.Render("r · re-scan") + "\n")
	return b.String()
}

// hwLabelOr renders a station's privacy-bucketed hw class, or a dim "-" when unknown.
func hwLabelOr(hw string) string {
	if c := hwClassLabel(hw); c != "" {
		return c
	}
	return "-"
}

// termsBreakdown returns the band's signal-term breakdown from the strongest online
// station's broker Terms, the band's signal, and whether a live breakdown exists. The
// cheapest station is the default route; if it has no breakdown we take the first online
// station that does.
func (bd band) termsBreakdown() (signalTerms, int, bool) {
	if bd.cheapest != nil && (bd.cheapest.Terms.Total > 0 || bd.cheapest.Signal > 0) {
		return bd.cheapest.Terms, bd.cheapest.Signal, true
	}
	for i := range bd.all {
		o := bd.all[i]
		if o.Online && (o.Terms.Total > 0 || o.Signal > 0) {
			return o.Terms, o.Signal, true
		}
	}
	return signalTerms{}, 0, false
}

// tpsCell renders a station's signal: the shared ◉ on-air glyph (the one red
// glint) + measured tok/s, or the hollow ○ off-air glyph, in mono. Same
// iconography the band table, share table, and channel header all use.
func tpsCell(tps float64, online bool) string {
	dot := stDim.Render(glyphOffAir)
	if online {
		dot = stRed.Render(glyphOnAir)
	}
	if tps > 0 {
		return dot + stLive.Render(fmt.Sprintf("  %.0f t/s", tps))
	}
	return dot + stDim.Render("  - t/s")
}

// tpsPlain is tpsCell without color (for a reverse-video selected row, where one
// accent style must govern the whole row). Same ◉/○ shared glyphs, no color.
func tpsPlain(tps float64, online bool) string {
	dot := glyphOffAir
	if online {
		dot = glyphOnAir
	}
	if tps > 0 {
		return fmt.Sprintf("%s %.0f t/s", dot, tps)
	}
	return dot + " - t/s"
}

func (m model) bandOnAir(model string) bool {
	for _, b := range m.bands {
		if b.model == model && b.online {
			return true
		}
	}
	if m.share != nil && m.share.Model() == model {
		return true
	}
	for mdl, s := range m.shares {
		if mdl == model && s != nil {
			return true
		}
	}
	return false
}

// bandSignal is the same proxy the signal tower uses, so the "strongest signal"
// sort orders by what the meter shows: the broker's 0..100 signal (cheapest
// station) when carried, else the legacy measured tok/s. An on-air band with no
// traffic still sorts by its baseline signal instead of dropping to 0.
func bandSignal(b band) float64 {
	if b.cheapest == nil {
		return 0
	}
	if b.cheapest.Signal > 0 {
		return float64(b.cheapest.Signal)
	}
	return b.cheapest.TPS
}

// quantsOnAir is every distinct quant currently on the dial, in a stable order - the set
// the Q toggle cycles through. A dial where nothing states a quant yields nothing, and the
// toggle then has nothing to do, which is the honest outcome rather than an empty filter
// that hides every row.
func (m model) quantsOnAir() []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range m.bands {
		if b.isVoice() || b.quant == "" || seen[b.quant] {
			continue
		}
		seen[b.quant] = true
		out = append(out, b.quant)
	}
	sort.Strings(out)
	return out
}

// bandAgentReady reports whether a band is coding-agent capable, and whether that readiness
// is INFERRED (from the window alone) rather than VERIFIED (the broker's tool-call probe).
// Readiness needs the representative window to meet the agent-ready floor (operatorCtxFloor,
// the same 16k gate the handoff uses). It is VERIFIED (inferred=false, ⌁) when a station on
// the band carries the broker-probed "tools" capability, and INFERRED (inferred=true, ⌁~) when
// the window qualifies but no tool-call proof exists yet. An UNKNOWN window (ctx 0) is NOT
// claimed agent-ready here - the badge never asserts a window it cannot see.
func bandAgentReady(bd band) (ready, inferred bool) {
	ctx, _ := bandCtx(bd)
	if ctx >= operatorCtxFloor {
		return true, !bd.tools // probed tools -> VERIFIED (no tilde); absent -> INFERRED (~)
	}
	return false, false
}

// bandKnownSmall reports a band whose window is KNOWN and under the agent-ready floor -
// the one partition auto-tune de-prioritises for a coding handoff (R6). Unknown (ctx 0)
// is NOT known-small: it may well be a large model the broker sent without ctx metadata.
func bandKnownSmall(bd band) bool {
	ctx, _ := bandCtx(bd)
	return ctx > 0 && ctx < operatorCtxFloor
}

// bandBadge renders the right-hand flag cell: a lit "◉ connected" marker for the
// open channel's band, the gold "◆ N" count of TEE-verified confidential stations on
// the band (bd.lineage is the confidential count from /discover), a live FREE tag, and
// the ember above-limit warning.
func bandBadge(bd band, limits *LimitStore, connected bool) string {
	parts := []string{}
	if connected {
		parts = append(parts, stRed.Render(glyphOnAir+" connected"))
	}
	// verified ✓ = a station passed the broker's live serving probe (the IDENTITY/lineage
	// glint), kept DISTINCT from the gold confidential ◆ tier per the codebase's mark split.
	if bd.verified {
		parts = append(parts, stGold.Render(glyphLineage)+stDim.Render(" verified"))
	}
	if bd.lineage > 0 {
		parts = append(parts, stGold.Render(fmt.Sprintf("◆ %d", bd.lineage)))
	}
	// Agent-ready ⌁ (inferred ⌁~) - the coding-agent-capable mark, keyed like the ctx
	// value it is derived from. Vision ◪ - a declared multimodal band.
	if tag := agentReadyTag(bd); tag != "" {
		parts = append(parts, stKey.Render(tag))
	}
	if bd.vision {
		parts = append(parts, stKey.Render(visionGlyph()))
	}
	if bd.free {
		parts = append(parts, stLive.Render("FREE"))
	}
	if bandOverLimit(bd, limits) {
		parts = append(parts, stEmber.Render("above limit"))
	}
	if len(parts) == 0 {
		return stDim.Render("·")
	}
	return strings.Join(parts, " ")
}

// bandBadgeLegend is the one dim key line under the band table explaining the flag
// glyphs that are NOT self-describing text: the agent-ready ⌁ (inferred ⌁~) and the
// vision ◪. FREE / ◆ / ✓ carry their own words in the cell, so the legend stays short.
// Rendered plain (dim) and folded for ASCII so a legacy console shows "%~ / [v]".
func bandBadgeLegend() string {
	ar := agentReadyGlyph()
	return stDim.Render("  " + ar + " agent-ready (" + ar + "~ inferred) · " + visionGlyph() + " vision")
}

// groupBands groups offers by model into bands, computing each band's live
// cross-station out-price range (min..max of out-price across ONLINE stations),
// the cheapest station, and flags. Bands are sorted cheapest-first, with any band
// whose cheapest station is over the user's limit sorted last (it still shows,
// flagged "above limit" per the design). Offline-only bands sort after online.
// bandNameCell renders a band's identity in exactly w cells: the model, and the quant when
// the band has one.
//
// The quant is part of the IDENTITY now, not a decoration, because bands are grouped by
// (model, quant): two rows can carry the same model name and differ only here. So when
// space is short the MODEL NAME gives way, not the quant - a truncated name next to
// "Q4_K_M" still tells you which row you are on, while a full name with no quant leaves
// two rows that differ in no visible way, which is the failure splitting was meant to fix.
//
// A band with no stated quant renders as just the model. Absent is absent: no placeholder,
// no "unknown", nothing that could be mistaken for a station's claim.
func bandNameCell(bd band, w int) string {
	if bd.quant == "" || w <= 0 {
		return pad(bd.model, w)
	}
	// Keep at least a few characters of the model, or the row loses the other half of its
	// identity; below that there is no room for both and the name wins.
	const minModel = 6
	if w < minModel+1+len([]rune(bd.quant)) {
		return pad(bd.model, w)
	}
	name := truncVisible(bd.model, w-1-len([]rune(bd.quant)))
	return pad(name+" "+bd.quant, w)
}

func groupBands(offers []offer, limits *LimitStore) []band {
	// GROUPED BY (MODEL, QUANT), not by model alone (MODEL-VARIANTS-DESIGN-2026-08-22,
	// founder ruling: split into rows).
	//
	// Two stations both offering "qwen3.8-27b" can be running very different weights - one
	// Q4_K_M on a laptop, one bf16 on a 4090 - and merging them made the dial claim they
	// were interchangeable while the broker routed between them on price. Splitting is the
	// honest shape AND the one that makes choosing work: a row is now a routable set, so
	// "tune this row" already means "only these weights" without the router learning a new
	// concept.
	//
	// Offers with NO stated quant collapse into ONE row per model, which falls out of the
	// key for free: they are not a quant, they are an absence, and splitting absences by
	// nothing would produce rows that differ in no visible way.
	byKey := map[string]*band{}
	order := []string{}
	for _, o := range offers {
		key := o.Model + "\x00" + o.Quant
		b, ok := byKey[key]
		if !ok {
			b = &band{model: o.Model, quant: o.Quant, modality: canonModality(o.Modality)}
			byKey[key] = b
			order = append(order, key)
		}
		oc := o
		b.all = append(b.all, oc)
		if o.Confidential {
			b.lineage++
		}
		// A DECLARED capability is intrinsic to the model, so it counts from any station
		// (online or not) - a vision model does not stop being multimodal while off air.
		if offerHasCapability(o, "vision") {
			b.vision = true
		}
		// A broker-VERIFIED "tools" capability is intrinsic to the model too (it earned it
		// from the tool-call canary), so it counts from any station carrying it. It upgrades
		// the agent-ready badge from inferred (⌁~) to verified (⌁) - never a declared claim.
		if offerHasCapability(o, "tools") {
			b.tools = true
		}
		if !o.Online {
			continue
		}
		if o.FreeNow {
			b.free = true
		}
		if o.Verified {
			b.verified = true // a serving-probe pass on any online station (✓)
		}
		// Real live load: sum the broker's in-flight count across the band's online
		// stations. This (not a frame counter) is what makes the meter animate ONLY when
		// the band is genuinely serving traffic.
		if o.InFlight > 0 {
			b.inFlight += o.InFlight
		}
		if b.stations == 0 || o.PriceOut < b.minOut {
			b.minOut = o.PriceOut
			b.cheapest = &b.all[len(b.all)-1]
		}
		if b.stations == 0 || o.PriceOut > b.maxOut {
			b.maxOut = o.PriceOut
		}
		// Headline in-price: the cheapest active input price across online stations,
		// tracked independently of the out-price so the band row can show $/1M in·out
		// exactly like the web /models row (which reports minIn · minOut).
		if b.stations == 0 || o.PriceIn < b.minIn {
			b.minIn = o.PriceIn
		}
		b.stations++
		b.online = true
	}
	out := make([]band, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	sort.SliceStable(out, func(i, j int) bool {
		oi := bandOverLimit(out[i], limits)
		oj := bandOverLimit(out[j], limits)
		if out[i].online != out[j].online {
			return out[i].online // online first
		}
		if oi != oj {
			return !oi // within-limit before above-limit
		}
		return out[i].minOut < out[j].minOut // then cheapest first
	})
	return out
}

// bandOverLimit reports whether a band's cheapest online station is over the
// user's per-model out-price max (so it sorts last and is flagged).
func bandOverLimit(b band, limits *LimitStore) bool {
	if !b.online {
		return false
	}
	lim := limits.resolve(b.model)
	return lim.MaxOut > 0 && b.minOut > lim.MaxOut
}

// bandTierSuffix is priceTierSuffix for a band row: the cheapest online station's tier
// vs the live market. Empty for an offline / free / unknown band.
func bandTierSuffix(b band) string {
	if !b.online || b.cheapest == nil {
		return ""
	}
	return priceTierSuffix(b.cheapest.PriceTier, b.minOut)
}

// bandTierTag returns the compact $-tier glyphs for a band's cheapest active price
// ("$".."$$$$", where more $ = pricier vs the live market reference), or "" when the band
// is free / offline / has no tier yet. It is the band-LIST twin of the tier shown in the
// [i] DETAIL view (bandTierSuffix), so the wide table can be price-judged at a glance.
func bandTierTag(b band) string {
	if !b.online || b.cheapest == nil {
		return ""
	}
	bars, _ := pricetier.Render(b.cheapest.PriceTier, b.minOut)
	if bars == "" || bars == "FREE" { // free has its own FREE tag; unknown shows nothing
		return ""
	}
	return bars
}

// bandBestTPS returns the band's fastest measured output throughput across its
// ONLINE stations - the same "best_tps" headline the web /models row shows. 0 when no
// online station has reported throughput yet (the caller renders an honest "-").
func bandBestTPS(bd band) float64 {
	best := 0.0
	for i := range bd.all {
		o := bd.all[i]
		if o.Online && o.TPS > best {
			best = o.TPS
		}
	}
	return best
}

// bandCtx returns the band's representative context window and whether it is
// estimated: the largest DETECTED window across its stations (so one real window wins),
// falling back to the largest estimated window, else the cheapest station's value. A
// band is "estimated" only when NO station reported a detected window.
func bandCtx(bd band) (ctx int, estimated bool) {
	bestDetected, bestEst := 0, 0
	for i := range bd.all {
		o := bd.all[i]
		if o.Ctx <= 0 {
			continue
		}
		if o.CtxEstimated {
			if o.Ctx > bestEst {
				bestEst = o.Ctx
			}
		} else if o.Ctx > bestDetected {
			bestDetected = o.Ctx
		}
	}
	if bestDetected > 0 {
		return bestDetected, false
	}
	if bestEst > 0 {
		return bestEst, true
	}
	if bd.cheapest != nil && bd.cheapest.Ctx > 0 {
		return bd.cheapest.Ctx, bd.cheapest.CtxEstimated
	}
	return 0, false
}

// bandCardView is the one-time PRIVATE-band code card (modeBandCard), shown right
// after a row goes private. It presents the full one-time CODE BIG and mono, states it
// is shown once, and offers c=copy. Any other key returns to SHARE (which clears the
// secret). Width/NO_COLOR-safe: no animation, plain glyphs.
func (m model) bandCardView(w int) string {
	var b strings.Builder
	line := func(s string) { b.WriteString("  " + truncVisible(s, w-2) + "\n") }
	head := stSelBar.Render("▌") + " " + stBrand.Render("PRIVATE BAND")
	line(head + stDim.Render("  shown once"))
	b.WriteString("\n")
	if m.bandCardModel != "" {
		line(stDim.Render("model ") + stKey.Render(m.bandCardModel))
	}
	// The big mono code line. This is the ONE-TIME reveal, so it surfaces the FULL code
	// ("147.520 MHz · 8F3K-9M2Q") with the secret tail - the thing the owner must save now.
	// The broker persists only sha256(tail) + a MASKED display, so this card is the only
	// place the code is ever shown (modeBandCard is entered only with a freshly-minted code).
	code := m.bandCardCode
	if code == "" {
		code = m.bandCardDisp
	}
	b.WriteString("\n")
	line(stRed.Render(glyphOnAir) + "  " + stKey.Render(code))
	b.WriteString("\n")
	line(stDim.Render("tune in: ") + stKey.Render("roger use <model> --freq \""+m.bandCardCode+"\""))
	line(stDim.Render("the MHz part is cosmetic; the code is the secret."))
	b.WriteString("\n")
	line(stKey.Render("c") + stDim.Render(" copy · any key returns (not shown again)"))
	return b.String()
}

// ---- helpers / cmds ----
// signalBarsRaw returns the 5-cell equalizer glyphs WITHOUT color, so callers can
// pad/align on the true display width before tinting. It is an HONEST readout: every
// visual is tied to a real offer field, never a decorative loop.
//
//   - LEVEL (bar height) reflects the broker's 0..100 signal (tps fallback when no
//     signal is carried), +1/notch per extra station (capped +2) - the web's "more
//     stations, stronger carrier" rule. Bands with different signals look different.
//   - ANIMATION reflects real ACTIVITY: inFlight is the broker's live in-flight count.
//     A band actively serving (inFlight>0) SCANS - a wave rides across the tower, its
//     amplitude scaled by how busy it is (more in-flight / faster tps = a bigger swing).
//     An idle-but-online band (inFlight==0) is STEADY (the static measured level, no
//     motion). Offline returns the flat tower below - dim and motionless.
//
// quiet/reduced-motion (anim() freezes the frame): the scan collapses to the steady
// truthful level, so a pipe / NO_COLOR / windowshade sees the honest height with no
// animation. The motion never changes the underlying LEVEL - a busy band scans AROUND
// its real signal, it does not inflate it.
//
// signalRamp returns the 8-level signal-tower ramp (low -> high) for the resolved
// glyph set: the Unicode ▁..█ on capable terminals, an ASCII .:-=+*#@ fallback on a
// legacy Windows console. signalPeak indexes into either ramp identically.
func signalRamp() []rune { return glyphs.Current().Signal }

// signalLevel maps the broker's 0..100 signal onto the LIT-BAR COUNT (0..5) of the
// staircase meter: ceil(signal/20), so 1-20 -> 1 bar, 41-60 -> 3 bars (the ~43
// baseline lands mid-meter), 81-100 -> the full 5. A positive signal always returns
// >= 1 so an online node never reads blank. 0 means "no broker signal carried" so
// the caller can fall back to the tps-derived count. Kept in lock-step with
// client.signalLevel (the plain-CLI meter) so both agree.
func signalLevel(signal int) int {
	if signal <= 0 {
		return 0
	}
	n := (signal*5 + 99) / 100 // ceil(signal/20)
	if n > 5 {
		n = 5
	}
	return n
}

// signalFlat is the 5-cell "no signal" tower (offline / unmeasured) for the resolved
// glyph set.
func signalFlat() string { return glyphs.Current().SigOff }

func signalBarsRaw(frame, signal int, tps float64, online bool, inFlight, stations int) string {
	if !online {
		return signalFlat()
	}
	// LEVEL: the broker's 0..100 signal is the primary driver: an online node earns a
	// baseline (supply + quality) even at tps==0, so the band never reads blank
	// while on air. Fall back to the legacy tps level only when no signal is carried.
	base := signalLevel(signal)
	if base == 0 {
		switch {
		case tps >= 600:
			base = 5
		case tps >= 300:
			base = 4
		case tps >= 150:
			base = 3
		case tps >= 60:
			base = 2
		case tps > 0:
			base = 1
		}
	}
	if base == 0 {
		// Online with neither a broker signal nor measured tps: one faint bar, never
		// a fully blank meter (online always reads as at least a carrier).
		base = 1
	}
	// More stations on the band -> a stronger carrier: +1 bar per extra station
	// beyond the first, capped at +2 (and at the meter's 5), so a single fast node
	// and a crowded band stay distinguishable without pinning everything full.
	if stations > 1 {
		boost := stations - 1
		if boost > 2 {
			boost = 2
		}
		base += boost
	}
	if base > 5 {
		base = 5
	}
	// ACTIVITY -> animation amplitude. amp is how far the scanning wave swings around the
	// measured level: 0 = idle (a STEADY tower, no shimmer), 1..2 = actively serving
	// (wider swing the busier the band). See signalAmp.
	amp := signalAmp(inFlight, tps)
	// Reduced-motion / quiet: anim() pins the frame, so the wave is frozen to a single
	// static phase - a truthful still height, no animation. The amp (real activity) still
	// governs whether there is any motion to freeze in the first place.
	return signalTowerAt(anim(frame), base, amp)
}

// signalTowerAt renders the 5-cell staircase at an ALREADY-RESOLVED frame (the caller
// has applied any reduced-motion freeze via anim()/sigFrame). count (0..5) is how many
// bars are lit; motion = real activity, and it moves ONLY the top of the staircase so
// the lit-bar COUNT never wavers: at amp 1 the top bar breathes one ramp step, at amp
// 2 it swings both ways and the bar below ripples with it. The frozen frame (anim()
// pins frame=1, where scanOffset returns 0) is exactly the pure staircase.
func signalTowerAt(frame, count, amp int) string {
	set := signalRamp()
	if count > 5 {
		count = 5
	}
	var sb strings.Builder
	for i := 0; i < 5; i++ {
		if i >= count {
			sb.WriteRune(set[0]) // the unlit rail: visible, clearly empty
			continue
		}
		lvl := stairHeights[i]
		switch {
		case i == count-1:
			lvl += scanOffset(frame, amp)
		case amp >= 2 && i == count-2:
			lvl += scanOffset(frame, 1)
		}
		// Clamp the swing: never down to the rail (the count stays honest) and never
		// past the ramp top.
		if lvl < 1 {
			lvl = 1
		}
		if lvl >= len(set) {
			lvl = len(set) - 1
		}
		sb.WriteRune(set[lvl])
	}
	return sb.String()
}

// signalAmp maps a band's REAL activity (broker in-flight load + measured tps) onto the
// signal meter's animation amplitude: 0 = idle/steady, 1..2 = actively serving (wider =
// busier). Exposed so callers + tests reason about motion from the same honest inputs.
func signalAmp(inFlight int, tps float64) int {
	switch {
	case inFlight >= 3 || tps >= 150:
		return 2
	case inFlight >= 1:
		return 1
	case tps >= 20:
		// Measured throughput but the broker reported no in-flight snapshot (a station
		// that just finished a burst): a faint single-cell breath, not dead-steady.
		return 1
	}
	return 0
}

// signalPeak is the glyph level at and above which a signal cell glints red - the
// "data-as-decoration" grade (like Serie / regex-tui): the tower is mono ink, but
// its tallest bars (a strong carrier) tip into the one accent red at the peak. The
// glyph ramp is ▁▂▃▄▅▆▇█ (indices 0..7); ▇/█ (>= 6) read as "peaking".
const signalPeak = 6
