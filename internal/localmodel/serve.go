package localmodel

// SERVING: starting the runtime on a model, and the one record that says what is running.
//
// The record is what lets `roger model stop`, `roger share` and the Edge all agree about what this
// machine serves without asking each other. It holds the runtime's key, so it is written 0600, and
// it names the roger that supervises the runtime, so a record left by a roger that died is seen
// to be stale rather than believed.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"rogerai.fm/roger/v6/internal/onair"
	"rogerai.fm/roger/v6/internal/runtime"
)

// Serving is what is running, said in full: "the model name" alone is never the whole answer,
// because two machines running one name may be running very different weights.
type Serving struct {
	Supervisor int       `json:"supervisor"` // the roger keeping it alive
	RuntimePID int       `json:"runtime_pid"`
	BaseURL    string    `json:"base_url"`
	Port       int       `json:"port,omitempty"` // names its record and key: unique while it runs
	Key        string    `json:"key"`
	Band       string    `json:"band"`
	ID         string    `json:"id"`
	File       string    `json:"file"`
	SHA256     string    `json:"sha256,omitempty"`
	Quant      string    `json:"quant,omitempty"`
	Ctx        int       `json:"ctx"`
	Runtime    string    `json:"runtime"`
	Started    time.Time `json:"started"`
}

// servingPath is the single record a roger before two-at-once kept. It is still read and released,
// so a serve started by an older roger is not lost across an upgrade.
func (h Home) servingPath() string { return filepath.Join(h.Dir, "serving.json") }

// recordPath is one serve's record, named by its port, which no other running serve can hold:
// two serves, two records.
func (h Home) recordPath(port int) string {
	return filepath.Join(h.Dir, "serving-"+strconv.Itoa(port)+".json")
}

// alive is the seam a test uses for "is that supervisor still running".
var alive = onair.ProcessAlive

// LiveAll is everything this machine is serving, in the order it went on the air. A record whose
// supervisor has gone is stale: the runtime died with it (the kernel was asked to see to that), so
// believing the record would announce a band nobody answers.
func (h Home) LiveAll() []Serving {
	paths, _ := filepath.Glob(filepath.Join(h.Dir, "serving-*.json"))
	paths = append(paths, h.servingPath())
	var out []Serving
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var s Serving
		if json.Unmarshal(b, &s) != nil || s.Supervisor <= 0 || !alive(s.Supervisor) {
			continue
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// Live is what went on the air last, or false: the one a switch replaces.
func (h Home) Live() (Serving, bool) {
	all := h.LiveAll()
	if len(all) == 0 {
		return Serving{}, false
	}
	return all[len(all)-1], true
}

func (h Home) writeServing(s Serving) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(h, h.recordPath(s.Port), b)
}

// Release clears one serve's record and key. Another serve's record is not its to touch: a switch
// writes the new record BEFORE the old supervisor is stopped, and the old one must not erase its
// successor.
func (h Home) Release(s Serving) {
	_ = os.Remove(h.recordPath(s.Port))
	if b, err := os.ReadFile(h.servingPath()); err == nil {
		var old Serving
		if json.Unmarshal(b, &old) == nil && old.Supervisor == s.Supervisor {
			_ = os.Remove(h.servingPath())
		}
	}
	_ = os.Remove(h.keyPath(s.Port))
}

func (h Home) keyPath(port int) string {
	return filepath.Join(h.Dir, "serving-"+strconv.Itoa(port)+".key")
}

// ServeOpts is one model to serve.
type ServeOpts struct {
	Model       Model
	Exe         string // the runtime, from runtime.Install
	Tag         string // the runtime's version, recorded
	Ctx         int
	Port        int // zero picks a free loopback port
	Budget      Budget
	ReadyWithin time.Duration
	Env         map[string]string // the runtime's environment additions; tests use it
}

// Args is the runtime's command line. Every flag a small machine needs is set explicitly rather
// than left to a default sized for a server, and the key goes in a FILE: a key on the command line
// is readable by every user on the machine through the process list.
func Args(o ServeOpts, port int, keyFile string) []string {
	return []string{
		"-m", o.Model.Path,
		"--alias", o.Model.Band,
		"-c", strconv.Itoa(o.Ctx),
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--api-key-file", keyFile,
		"--parallel", "1",
		"--cache-ram", strconv.FormatInt(cacheRAMMiB(o.Budget), 10),
		"--no-webui",
		"--metrics", // its own token counters, behind the same key: how an idle timeout knows
	}
}

// cacheRAMMiB sizes the prompt cache to the machine: the runtime's default is 8 GiB, which is the
// whole of a Pi.
func cacheRAMMiB(b Budget) int64 {
	m := b.Bytes >> 20 / 16
	if m < 256 {
		m = 256
	}
	if m > 8192 {
		m = 8192
	}
	return m
}

// Serve starts the runtime on a model and records it. The caller owns the returned server and
// must Stop it and Release the record; the kernel ends the runtime if the caller dies first.
func (h Home) Serve(ctx context.Context, o ServeOpts) (*runtime.Server, Serving, error) {
	if o.Ctx <= 0 {
		o.Ctx = DefaultCtx
	}
	port := o.Port
	if port == 0 {
		p, err := freePort()
		if err != nil {
			return nil, Serving{}, err
		}
		port = p
	}
	key, err := newKey()
	if err != nil {
		return nil, Serving{}, err
	}
	me := os.Getpid()
	if err := os.MkdirAll(h.Dir, 0o700); err != nil {
		return nil, Serving{}, err
	}
	keyFile := h.keyPath(port)
	if err := os.WriteFile(keyFile, []byte(key+"\n"), 0o600); err != nil {
		return nil, Serving{}, err
	}
	srv, err := runtime.Start(ctx, runtime.Spec{
		Exe: o.Exe, Model: o.Model.Path, Band: o.Model.Band, Ctx: o.Ctx, Port: port, Key: key,
		Args: Args(o, port, keyFile), Env: o.Env, ReadyWithin: o.ReadyWithin,
	})
	if err != nil {
		_ = os.Remove(keyFile)
		return nil, Serving{}, err
	}
	s := Serving{
		Supervisor: me, RuntimePID: srv.PID, BaseURL: srv.BaseURL, Port: port, Key: key, Band: o.Model.Band,
		ID: o.Model.ID, File: o.Model.File, SHA256: o.Model.SHA256, Quant: o.Model.Quant, Ctx: o.Ctx,
		Runtime: o.Tag, Started: srv.Started,
	}
	if err := h.writeServing(s); err != nil {
		_ = srv.Stop(context.Background())
		_ = os.Remove(keyFile)
		return nil, Serving{}, fmt.Errorf("serving started but could not be recorded, so it was stopped: %w", err)
	}
	return srv, s, nil
}

func newKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// RecordOf is where a serve's record (and so its key) is kept, for the owner to find.
func (h Home) RecordOf(s Serving) string { return h.recordPath(s.Port) }

// StillServing reports whether that serve is still on the air.
func (h Home) StillServing(s Serving) bool {
	for _, cur := range h.LiveAll() {
		if cur.Supervisor == s.Supervisor && cur.Port == s.Port {
			return true
		}
	}
	return false
}

// ServingModel is the serve of that model, if it is on the air.
func (h Home) ServingModel(m Model) (Serving, bool) {
	for _, s := range h.LiveAll() {
		if s.ID == m.ID && s.File == m.File {
			return s, true
		}
	}
	return Serving{}, false
}
