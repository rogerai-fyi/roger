package onair

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Share is a live share's local record: the band a share process has on air and where it
// serves it, so this machine's LAN relay and Edge face (in another roger process) can route the
// band to it as THIS machine's serve (features/packaging/one_binary.feature section 7.9). It is
// local and owner-only by design: it never rides the household or fleet records peers receive,
// because it carries the upstream's address and key.
type Share struct {
	PID     int    `json:"pid"`
	Band    string `json:"band"`
	ChatURL string `json:"chat_url"`
	Key     string `json:"key,omitempty"`
	// Modality is the band's kind ("" = chat, "tts" = a voice), and SpeechURL a voice's
	// text-to-speech URL, so this machine's `roger --lan say` can speak through it.
	Modality  string `json:"modality,omitempty"`
	SpeechURL string `json:"speech_url,omitempty"`
}

// SharesDir is where share records live, beside the on-air locks. It is the directory the
// previous release wrote them to, so records from before the upgrade are read where they are.
func SharesDir() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "rogerai", "lan-shares")
}

// ShareFile is the record of the share process pid.
func ShareFile(pid int) string {
	return filepath.Join(SharesDir(), fmt.Sprintf("share-%d.json", pid))
}

// WriteShare records s, 0600 in a 0700 directory (it can hold the upstream's key), and returns
// the remover the share runs when it stops. A crashed share leaves a record its dead pid marks
// stale, by the same rule that reclaims a stale on-air lock.
func WriteShare(s Share) (release func(), err error) {
	b, _ := json.Marshal(s) // a struct of strings and an int always marshals
	if err := os.MkdirAll(SharesDir(), 0o700); err != nil {
		return func() {}, err
	}
	// Written whole under a temporary name, then renamed: a reader never sees half a record.
	tmp := ShareFile(s.PID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return func() {}, err
	}
	if err := os.Rename(tmp, ShareFile(s.PID)); err != nil {
		_ = os.Remove(tmp)
		return func() {}, err
	}
	return func() { _ = os.Remove(ShareFile(s.PID)) }, nil
}

// Shares lists the live shares on this machine. A record whose process is gone is removed (the
// one staleness rule, ProcessAlive); a damaged one is skipped.
func Shares() []Share {
	files, _ := filepath.Glob(filepath.Join(SharesDir(), "share-*.json"))
	var out []Share
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var s Share
		if json.Unmarshal(b, &s) != nil || s.PID <= 0 || s.Band == "" || s.ChatURL == "" {
			continue
		}
		if !ProcessAlive(s.PID) {
			_ = os.Remove(f)
			continue
		}
		out = append(out, s)
	}
	return out
}
