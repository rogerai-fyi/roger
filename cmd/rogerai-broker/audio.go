package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// transcriptText pulls the screenable text out of an STT result body. The OpenAI STT
// shape is {"text":"..."}; verbose_json adds a "segments" array of {"text":...}. We read
// the top-level "text" (present in both) and fall back to concatenating segment texts.
// A body that does not parse yields "" (the caller treats no-text as nothing to screen;
// a malformed body is separately rejected as a 502 by the result-shape guard).
func transcriptText(body []byte) (string, bool) {
	// Shape check on KEY PRESENCE, not just parseability: any JSON object would otherwise
	// pass (e.g. {"not":"a transcription"}) and be relayed RAW. A transcription must carry a
	// "text" or "segments" key; an empty "text" is a legitimate silent-audio result.
	var keys map[string]json.RawMessage
	if json.Unmarshal(body, &keys) != nil {
		return "", false // not the transcription shape -> never forwarded raw
	}
	hasKey := func(name string) bool {
		for k := range keys { // Go's struct unmarshal is case-insensitive; match it
			if strings.EqualFold(k, name) {
				return true
			}
		}
		return false
	}
	if !hasKey("text") && !hasKey("segments") {
		return "", false // a JSON object, but not a transcription
	}
	var out struct {
		Text     string `json:"text"`
		Segments []struct {
			Text string `json:"text"`
		} `json:"segments"`
	}
	if json.Unmarshal(body, &out) != nil {
		return "", false
	}
	if strings.TrimSpace(out.Text) != "" {
		return out.Text, true
	}
	var sb strings.Builder
	for _, s := range out.Segments {
		sb.WriteString(s.Text)
		sb.WriteString(" ")
	}
	return sb.String(), true
}

// audioTTSMaxChars is the per-request TTS input cap (Unicode runes). Default ~10k
// (a multi-minute read; roger say + the TUI preview send sentences, far below it).
// <=0 disables the cap. ROGERAI_TTS_MAX_CHARS overrides.
func audioTTSMaxChars() int {
	if n, err := strconv.Atoi(os.Getenv("ROGERAI_TTS_MAX_CHARS")); err == nil {
		return n // including <=0 (disabled) - an explicit operator choice
	}
	return 10000
}

// newAudioSem builds the in-flight-audio semaphore: a buffered channel whose depth is
// the max concurrent TTS+STT relays per instance (default 8; 8 x ~40 MiB worst case
// fits the 1 GB instance beside the brain). <=0 disables the bound (nil semaphore).
// ROGERAI_AUDIO_INFLIGHT overrides.
func newAudioSem() chan struct{} {
	n := 8
	if v, err := strconv.Atoi(os.Getenv("ROGERAI_AUDIO_INFLIGHT")); err == nil {
		n = v
	}
	if n <= 0 {
		return nil
	}
	return make(chan struct{}, n)
}

// audioSpec parameterizes the shared voice/audio money relay. TTS (/v1/audio/speech) and STT
// (/v1/audio/transcriptions) run the SAME spine (auth, rate limits, routing, pricing, hold==
// finalize, settle, meter headers) and differ only in: how the metered unit is counted (input
// chars vs uploaded bytes), whether there is screenable text, the routed modality, and the
// response content type. Keeping one core avoids two divergent copies of the money path.
type audioSpec struct {
	modality    string // protocol.ModalityTTS | ModalitySTT
	path        string // upstream Path tag the node's bridge serves (/v1/audio/speech | /transcriptions)
	contentType string // response Content-Type (audio/mpeg | application/json)
	// screenOutput screens the node's RESULT text before returning it (STT: the
	// transcription is text the broker hands the consumer, so it must pass the same
	// policy as chat/TTS input - else STT is a laundering channel). TTS output is
	// opaque audio, so it is false there; its INPUT is screened up front instead.
	screenOutput bool
	// resultText extracts the screenable text from the node's result body (the STT
	// transcription's "text" field). ok=false means the body did not parse as the
	// expected result shape -> the caller 502s rather than forward it raw. nil when
	// screenOutput is false.
	resultText func(body []byte) (text string, ok bool)
	// parse pulls the routing model, the exact metered unit count (the BROKER's count), and any
	// screenable text out of the request. A non-empty badReq is returned as a 400 (empty/invalid
	// payload) BEFORE any hold; moderate == "" means there is no text to screen (opaque audio).
	parse func(r *http.Request, body []byte) (model string, units int, moderate, badReq string)
}

// audioRelay handles POST /v1/audio/speech (TTS): metered by the EXACT input characters (Unicode
// runes) — the broker's count, never the node's claim — so the hold equals the final charge.
func (b *broker) audioRelay(w http.ResponseWriter, r *http.Request) {
	b.audioRelayCore(w, r, audioSpec{
		modality: protocol.ModalityTTS, path: "/v1/audio/speech", contentType: "audio/mpeg",
		parse: func(_ *http.Request, body []byte) (string, int, string, string) {
			var req struct {
				Model string `json:"model"`
				Input string `json:"input"`
			}
			_ = json.Unmarshal(body, &req)
			chars := len([]rune(strings.TrimSpace(req.Input)))
			if chars == 0 {
				return "", 0, "", "empty input"
			}
			return req.Model, chars, req.Input, "" // the input text IS screened
		},
	})
}

// transcribeRelay handles POST /v1/audio/transcriptions (STT): metered by the EXACT uploaded audio
// BYTES — the broker's own count of the request body (tamper-proof: no audio to parse, no node
// claim). The model is a ?model= query param so the broker routes without touching the binary body.
func (b *broker) transcribeRelay(w http.ResponseWriter, r *http.Request) {
	b.audioRelayCore(w, r, audioSpec{
		modality: protocol.ModalitySTT, path: "/v1/audio/transcriptions", contentType: "application/json",
		parse: func(r *http.Request, body []byte) (string, int, string, string) {
			if len(body) == 0 {
				return "", 0, "", "empty audio upload"
			}
			return r.URL.Query().Get("model"), len(body), "", "" // opaque audio IN: no text to screen
		},
		screenOutput: true, // ...but the transcription OUT is text - screen it before returning
		resultText:   transcriptText,
	})
}

// audioRelayCore is the shared voice money path (see audioSpec). Same spine as relay(): grant/
// signed auth, rate limits, moderation (when there is text), pickFor modality isolation, resolve-
// Pricing, HoldFor/settleRequest on the same wallet, multi-instance bus dispatch, node-receipt
// verification, and the X-RogerAI-* meter headers. Because the unit count is exact + known up
// front, the hold equals the final charge (no recount). See VOICE-AUDIO-DESIGN.md.
func (b *broker) audioRelayCore(w http.ResponseWriter, r *http.Request, spec audioSpec) {
	if !allow(w, r, http.MethodPost) {
		return
	}
	// In-flight bound: a non-blocking slot acquire caps concurrent 32 MiB audio relays
	// so they can't stack N-deep and exhaust the small instance's memory. A full pool
	// sheds load with 503 + Retry-After (the client retries) rather than OOMing. Released
	// on EVERY return path below via defer. nil semaphore = disabled.
	if b.audioSem != nil {
		select {
		case b.audioSem <- struct{}{}:
			defer func() { <-b.audioSem }()
		default:
			w.Header().Set("Retry-After", "2")
			jsonErr(w, http.StatusServiceUnavailable, "voice relay saturated - retry shortly")
			return
		}
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 32<<20)) // audio uploads run larger than chat

	// --- Auth: a grant key, else a signed identity (identical to the chat relay). ---
	gc, gok, gerr := b.resolveGrant(r)
	if gerr != "" {
		jsonErr(w, http.StatusUnauthorized, gerr)
		return
	}
	var user, wallet string
	if gok {
		user, wallet = gc.wallet, gc.wallet
	} else {
		u, authed, iok := b.identityOf(r, body)
		if !iok {
			jsonErr(w, http.StatusUnauthorized, "invalid request signature")
			return
		}
		user, wallet = u, b.walletOf(r, u)
		if !authed {
			jsonErr(w, http.StatusUnauthorized, "spending requires a signed request")
			return
		}
	}

	// --- Rate limit (grant bucket, else per-IP for anon + per-user). ---
	if gok {
		if ok, retry := b.grantRL.allowAt(gc.grant.ID, gc.grant.RPM, gc.grant.Burst); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			jsonErr(w, http.StatusTooManyRequests, "grant rate limit exceeded - slow down")
			return
		}
	} else {
		if user == "anon" {
			if ok, retry := b.anonRL.allow(clientIP(r)); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(retry))
				jsonErr(w, http.StatusTooManyRequests, "rate limit exceeded - slow down")
				return
			}
		}
		if ok, retry := b.rl.allow(user); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			jsonErr(w, http.StatusTooManyRequests, "rate limit exceeded - slow down")
			return
		}
	}

	// Parse the routing model + the EXACT metered unit count (chars for TTS, bytes for STT). An
	// empty / invalid payload is refused BEFORE any hold - no charge for nothing.
	model, units, moderate, badReq := spec.parse(r, body)
	if badReq != "" {
		jsonErr(w, http.StatusBadRequest, badReq)
		return
	}
	// TTS input cap: refuse an over-long synth BEFORE any hold or dispatch (units is the
	// exact rune count for TTS, so the cap and the meter agree). Only TTS has a screenable
	// text unit to bound this way; STT is bounded by the 32 MiB body cap above.
	if spec.modality == protocol.ModalityTTS && b.ttsMaxChars > 0 && units > b.ttsMaxChars {
		jsonErr(w, http.StatusRequestEntityTooLarge,
			"input too long: "+strconv.Itoa(units)+" characters exceeds the "+strconv.Itoa(b.ttsMaxChars)+"-character limit")
		return
	}

	if gok {
		if st, msg := b.grantCapCheck(gc.grant); st != 0 {
			jsonErr(w, st, msg)
			return
		}
	}
	// Moderation screens the text (TTS input) before any node is paid; STT audio has no text.
	if moderate != "" {
		if res := b.mod.screen(moderate); !res.allow() {
			if res.csam {
				b.preserveCSAM(b.pseudonym(user, "audio"), clientIP(r), res.category, body)
			}
			jsonErr(w, res.status, res.msg)
			return
		}
	}

	// --- Route to a node of THIS modality ONLY (isolation via pickFor). ---
	// A NAMESPACED model "@<station>/<slug>" is RESOLVED to the SPECIFIC on-air node whose
	// operator station + voice-name slug match, then dispatched PINNED to that node on its RAW
	// offer model. This is what makes the money land on the RIGHT operator when two operators
	// share a raw model (e.g. both offer "af_heart"): pickFor(rawModel) alone would pick EITHER
	// node (power-of-two over the RNG) and could bill the wrong owner, so we constrain selection
	// to the resolved node id. A RAW id (no "@") routes exactly as before (pin=""). A namespaced
	// id with no matching on-air voice falls through to the uniform 503 below.
	routeModel, pinNode := model, ""
	if station, slug, isNS := parseNamespacedVoice(model); isNS {
		raw, node, resolved := b.resolveNamespacedVoice(station, slug, spec.modality)
		if !resolved {
			jsonErr(w, http.StatusServiceUnavailable, "no station on air for "+model)
			return
		}
		routeModel, pinNode = raw, node
	}
	requestID := protocol.NewRequestID()
	b.mu.Lock()
	node, offer, ok := b.pickFor(routeModel, false, 0, 0, 0, pinNode, nil, nil, nil,
		pickReq{modality: spec.modality, rng: seededRand(requestID)})
	t := b.tunnels[node.NodeID]
	b.mu.Unlock()
	if !ok || t == nil {
		jsonErr(w, http.StatusServiceUnavailable, "no station on air for "+model)
		return
	}

	pricing := b.resolvePricing(gc, gok, user, wallet, node, offer)
	payer := pricing.payer
	grantID := ""
	if gok {
		grantID = gc.grant.ID
	}

	// Per-1M-unit price -> this request's exact cost (units is the broker's count, never a node
	// claim). A price-0 / free-window offer is $0.
	pin := pricing.in
	if !pricing.fixed {
		ain, _, afree, _ := offer.ActivePrice(time.Now())
		pin = ain
		if afree {
			pin = 0
		}
	}
	cost := float64(units) * pin / 1e6
	if pricing.free {
		cost = 0
	}

	// A PAID request with no funded wallet -> 403 (the app shows "sign in ...").
	if !gok && cost > 0 && !walletLoggedIn(payer) {
		jsonErr(w, http.StatusForbidden, "sign in to use this voice model")
		return
	}

	// Hold the exact unit cost before dispatch (hold == finalize; count known up front).
	settled := false
	if cost > 0 {
		if st, msg := b.monthlyCapCheck(w, payer, cost, time.Now()); st != 0 {
			jsonErr(w, st, msg)
			return
		}
		b.ensureSeeded(payer)
		held, herr := b.db.HoldFor(payer, requestID, cost)
		if herr != nil {
			jsonErr(w, http.StatusInternalServerError, "wallet error")
			return
		}
		if !held {
			jsonErr(w, http.StatusPaymentRequired, "insufficient balance - add funds")
			return
		}
		defer func() {
			if !settled {
				b.db.ReleaseHoldFor(payer, requestID)
			}
		}()
	}

	// Dispatch to the node's bridge (tagged with spec.path so it serves the right local endpoint) +
	// await the result. In multi-instance prod the poller may be on a PEER, so route over the Valkey
	// bus (mirrors relay()); single-instance falls through to the local job channel.
	job := protocol.Job{ID: requestID, User: b.pseudonym(user, node.NodeID), Body: body, Path: spec.path}
	resCh := make(chan protocol.JobResult, 1)
	t.mu.Lock()
	t.waiters[job.ID] = resCh
	t.mu.Unlock()
	defer func() { t.mu.Lock(); delete(t.waiters, job.ID); t.mu.Unlock() }()

	var busRes <-chan []byte
	if b.multiInstance && b.shared != nil {
		ch, cancel, derr := b.busDispatchJob(r.Context(), node.NodeID, job)
		if cancel != nil {
			defer cancel()
		}
		if derr != nil {
			jsonErr(w, http.StatusServiceUnavailable, "station busy (no poller free)")
			return
		}
		busRes = ch
	} else {
		select {
		case t.jobs <- job:
		case <-time.After(3 * time.Second):
			jsonErr(w, http.StatusServiceUnavailable, "station busy")
			return
		}
	}
	if busRes != nil {
		go func() {
			raw, ok := <-busRes
			if !ok {
				return
			}
			var br protocol.JobResult
			if json.Unmarshal(raw, &br) == nil {
				select {
				case resCh <- br:
				default:
				}
			}
		}()
	}

	select {
	case res := <-resCh:
		if res.Status < 200 || res.Status >= 400 || len(res.Body) == 0 {
			// NODE-SIDE FAILURE (error_passthrough.feature): a 5xx whose JSON body the edge
			// passes through (origin 502/504 bodies are replaced with its HTML page), carrying
			// a SHORT reason extracted from a standard error shape and SANITIZED here — never
			// the node's raw body. The reason passes the same screen as STT output; flagged or
			// screen-down degrades to the generic form (an error is never withheld, and this
			// path never charges - the hold refunds via defer).
			reason, extracted := stationErrReason(res.Status, res.Body)
			if extracted {
				if sres := b.mod.screen(strings.TrimPrefix(reason, "station error: ")); !sres.allow() {
					reason = fmt.Sprintf("station error (status %d)", res.Status)
				}
			}
			jsonErr(w, http.StatusInternalServerError, reason)
			return
		}
		rec := res.Receipt
		// Verify the node's signed receipt before it is stored + broker-re-signed (as relay() does),
		// so an unverified node claim never enters lineage or grant-usage accounting. 500 (not
		// 502) so the reason survives the edge (see above).
		if !rec.VerifyNode(node.PubKey) {
			jsonErr(w, http.StatusInternalServerError, "station error: station receipt failed verification") // hold refunds via defer
			return
		}
		rec.PriceIn, rec.GrantID = pin, grantID
		rec.SignBroker(b.priv)

		// settle charges the request (or records a $0 metering receipt on the free path)
		// and marks the hold consumed. Factored out so the STT-block path can CHARGE the
		// abuser (the node did the work in good faith) while still withholding the text.
		settle := func() float64 {
			if cost > 0 {
				nb, ferr := b.settleRequest(payer, node.NodeID, cost, cost, rec, grantID, pricing.free)
				if ferr != nil {
					log.Printf("audio settle FAILED user=%s node=%s: %v", user, node.NodeID, ferr)
					return 0
				}
				settled = true
				return nb
			}
			if b.db != nil { // free path: record a $0 metering receipt for lineage (as chat does)
				_, _ = b.db.Settle(payer, node.NodeID, 0, 0, rec)
			}
			settled = true
			return 0
		}

		// STT output screen: the transcription is text the broker is about to hand the
		// consumer, so it passes the SAME policy as chat/TTS input. A screen OUTAGE (503,
		// require=1) withholds AND releases the hold (the failure is ours - `settled`
		// stays false, the defer refunds). A FLAGGED result (451) still CHARGES (the node
		// worked in good faith on opaque audio; the abuser eats the cost + is priced out
		// of repeat probing) but the body is withheld - no fragment leaks. CSAM preserves
		// the audio (primary evidence) + the transcription, exactly like the chat path.
		if spec.screenOutput && spec.resultText != nil {
			text, okShape := spec.resultText(res.Body)
			if !okShape {
				// 500 (not 502) so the reason survives the edge (see the node-failure path).
				jsonErr(w, http.StatusInternalServerError, "station error: station returned an unreadable result") // hold refunds via defer
				return
			}
			if strings.TrimSpace(text) != "" {
				if sres := b.mod.screen(text); !sres.allow() {
					if sres.status == http.StatusServiceUnavailable {
						jsonErr(w, sres.status, sres.msg) // outage: hold refunds via defer
						return
					}
					if sres.csam {
						b.preserveCSAM(b.pseudonym(user, node.NodeID), clientIP(r), sres.category, append(append([]byte{}, body...), []byte("\n--transcript--\n"+text)...))
					}
					newBal := settle() // charge the abuser
					w.Header().Set("X-RogerAI-Provider", node.NodeID)
					w.Header().Set("X-RogerAI-Cost", fmtCostHeader(cost))
					if cost > 0 {
						w.Header().Set("X-RogerAI-Balance", ftoa(round6(newBal)))
					}
					jsonErr(w, sres.status, sres.msg) // withhold the body
					return
				}
			}
		}

		newBal := settle()
		w.Header().Set("X-RogerAI-Provider", node.NodeID)
		w.Header().Set("X-RogerAI-Cost", fmtCostHeader(cost))
		if cost > 0 {
			w.Header().Set("X-RogerAI-Balance", ftoa(round6(newBal)))
		}
		w.Header().Set("Content-Type", spec.contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(res.Body)
	case <-time.After(nonStreamRelayWait):
		jsonErr(w, http.StatusGatewayTimeout, "station timed out")
	}
}
