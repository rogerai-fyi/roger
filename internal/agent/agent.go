// Package agent is the provider side ("rogerai share"). It registers with a
// broker and then DIALS OUT - N outbound long-poll loops pull relayed jobs from
// the broker, serve them against the local OpenAI-compatible upstream, sign a
// lineage receipt, and POST the result back. No inbound ports, no public URL,
// no tunnel dependency (the AI-Horde pattern). NAT-friendly everywhere.
package agent

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bownux/rogerai/internal/protocol"
)

type Config struct {
	Broker, Upstream, UpstreamKey string
	NodeID, Region, HW, Model     string
	PriceIn, PriceOut             float64
	Ctx, Parallel                 int
	BridgeToken                   string
	Confidential                  bool
	Attestation                   string
	Schedule                      []protocol.PriceWindow
}

var (
	mu       sync.Mutex
	lastHash string
)

func Run(cfg Config) error {
	priv := loadOrCreateKey()
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	token := cfg.BridgeToken
	if token == "" {
		token = randHex(16)
	}
	if cfg.Parallel <= 0 {
		cfg.Parallel = 4
	}

	offer := protocol.ModelOffer{Model: cfg.Model, PriceIn: cfg.PriceIn, PriceOut: cfg.PriceOut, Ctx: cfg.Ctx, Schedule: cfg.Schedule}
	reg := protocol.NodeRegistration{
		NodeID: cfg.NodeID, PubKey: pubHex, BridgeToken: token,
		Region: cfg.Region, HW: cfg.HW, Offers: []protocol.ModelOffer{offer},
		Confidential: cfg.Confidential, Attestation: cfg.Attestation,
	}
	if err := register(cfg.Broker, reg); err != nil {
		return fmt.Errorf("register with %s: %w", cfg.Broker, err)
	}
	go heartbeat(cfg.Broker, cfg.NodeID)

	log.Printf("sharing: node=%s broker=%s upstream=%s model=%s ($%.2f/$%.2f per 1M) pollers=%d",
		cfg.NodeID, cfg.Broker, cfg.Upstream, cfg.Model, cfg.PriceIn, cfg.PriceOut, cfg.Parallel)

	for i := 0; i < cfg.Parallel; i++ {
		go pollLoop(cfg, token, offer, priv)
	}
	select {} // serve forever
}

// pollLoop: one outbound long-poll worker. Pulls a job, serves it, posts result.
func pollLoop(cfg Config, token string, offer protocol.ModelOffer, priv ed25519.PrivateKey) {
	poll := &http.Client{Timeout: 35 * time.Second} // must exceed the broker's hold
	up := &http.Client{Timeout: 120 * time.Second}
	pollURL := cfg.Broker + "/agent/poll?node=" + url.QueryEscape(cfg.NodeID)
	for {
		req, _ := http.NewRequest(http.MethodGet, pollURL, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := poll.Do(req)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		if resp.StatusCode == http.StatusNoContent {
			resp.Body.Close() // long-poll timed out with no work - re-poll immediately
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			time.Sleep(2 * time.Second)
			continue
		}
		var job protocol.Job
		json.NewDecoder(resp.Body).Decode(&job)
		resp.Body.Close()
		postResult(poll, cfg, token, serve(cfg, offer, priv, up, job))
	}
}

func serve(cfg Config, offer protocol.ModelOffer, priv ed25519.PrivateKey, up *http.Client, job protocol.Job) protocol.JobResult {
	upReq, _ := http.NewRequest(http.MethodPost, cfg.Upstream, bytes.NewReader(job.Body))
	upReq.Header.Set("Content-Type", "application/json")
	if cfg.UpstreamKey != "" {
		upReq.Header.Set("Authorization", "Bearer "+cfg.UpstreamKey)
	}
	resp, err := up.Do(upReq)
	if err != nil {
		return protocol.JobResult{ID: job.ID, Status: http.StatusBadGateway, Body: json.RawMessage(`{"error":"upstream unreachable"}`)}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var parsed struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(respBody, &parsed)

	rec := protocol.UsageReceipt{
		RequestID: job.ID, NodeID: cfg.NodeID, User: job.User, Model: cfg.Model,
		PromptTokens: parsed.Usage.PromptTokens, CompletionTokens: parsed.Usage.CompletionTokens,
		PriceIn: offer.PriceIn, PriceOut: offer.PriceOut, TS: time.Now().Unix(),
		LineageMethod: "p0-upstream-usage",
	}
	mu.Lock()
	rec.PrevHash = lastHash
	rec.SignNode(priv)
	lastHash = rec.Hash()
	mu.Unlock()
	return protocol.JobResult{ID: job.ID, Status: resp.StatusCode, Body: respBody, Receipt: rec}
}

func postResult(client *http.Client, cfg Config, token string, res protocol.JobResult) {
	b, _ := json.Marshal(res)
	req, _ := http.NewRequest(http.MethodPost, cfg.Broker+"/agent/result?node="+url.QueryEscape(cfg.NodeID), bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}

func register(broker string, reg protocol.NodeRegistration) error {
	b, _ := json.Marshal(reg)
	resp, err := http.Post(broker+"/nodes/register", "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	resp.Body.Close()
	log.Printf("registered with broker %s as node %s", broker, reg.NodeID)
	return nil
}

func heartbeat(broker, nodeID string) {
	for range time.Tick(10 * time.Second) {
		b, _ := json.Marshal(map[string]string{"node_id": nodeID})
		if resp, err := http.Post(broker+"/nodes/heartbeat", "application/json", bytes.NewReader(b)); err == nil {
			resp.Body.Close()
		}
	}
}

func loadOrCreateKey() ed25519.PrivateKey {
	dir, _ := os.UserConfigDir()
	path := filepath.Join(dir, "rogerai", "node.key")
	if data, err := os.ReadFile(path); err == nil {
		if raw, err := hex.DecodeString(string(bytes.TrimSpace(data))); err == nil && len(raw) == ed25519.PrivateKeySize {
			return ed25519.PrivateKey(raw)
		}
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	_ = os.WriteFile(path, []byte(hex.EncodeToString(priv)), 0600)
	log.Printf("generated node key at %s", path)
	return priv
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
