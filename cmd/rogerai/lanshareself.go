package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"rogerai.fm/roger/v6/internal/node"
)

// lanShareRecord is a headless `roger share` process's own serve, so this machine's LAN relay
// (in another roger process) can route a band to it as THIS machine's serve. It is local and
// owner-only by design: it never rides the household or fleet records peers receive, because
// it carries the upstream's address and key.
type lanShareRecord struct {
	PID     int    `json:"pid"`
	Band    string `json:"band"`
	ChatURL string `json:"chat_url"`
	Key     string `json:"key,omitempty"`
}

func lanSharesDir() string { return filepath.Join(filepath.Dir(configPath()), "lan-shares") }

func lanShareFile(pid int) string {
	return filepath.Join(lanSharesDir(), fmt.Sprintf("share-%d.json", pid))
}

// writeLANShare records this process's share of band at upstream, and returns the remover the
// share runs when it stops. A crashed share leaves a record its dead pid marks stale.
func writeLANShare(band, upstream, key string) (func(), error) {
	rec := lanShareRecord{PID: os.Getpid(), Band: band, ChatURL: node.NormalizeUpstream(upstream), Key: key}
	if err := writeLANShareRecord(rec); err != nil {
		return func() {}, err
	}
	return func() { _ = os.Remove(lanShareFile(rec.PID)) }, nil
}

// writeLANShareRecord writes rec 0600 in a 0700 directory: it can hold the upstream's key.
func writeLANShareRecord(rec lanShareRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return atomicWriteConfig(lanShareFile(rec.PID), b)
}

// lanShares lists the live shares on this machine; a record whose process is gone is removed,
// and a damaged one is skipped.
func lanShares() []lanShareRecord {
	files, _ := filepath.Glob(filepath.Join(lanSharesDir(), "share-*.json"))
	var out []lanShareRecord
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var rec lanShareRecord
		if json.Unmarshal(b, &rec) != nil || rec.PID <= 0 || rec.Band == "" || rec.ChatURL == "" {
			continue
		}
		if !processAlive(rec.PID) {
			_ = os.Remove(f)
			continue
		}
		out = append(out, rec)
	}
	return out
}
