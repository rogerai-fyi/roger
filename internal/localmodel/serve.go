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

func (h Home) servingPath() string { return filepath.Join(h.Dir, "serving.json") }

// alive is the seam a test uses for "is that supervisor still running".
var alive = onair.ProcessAlive

// Live is what this machine is serving, or false. A record whose supervisor has gone is stale:
// the runtime died with it (the kernel was asked to see to that), so believing the record would
// announce a band nobody answers.
func (h Home) Live() (Serving, bool) {
	b, err := os.ReadFile(h.servingPath())
	if err != nil {
		return Serving{}, false
	}
	var s Serving
	if json.Unmarshal(b, &s) != nil || s.Supervisor <= 0 || !alive(s.Supervisor) {
		return Serving{}, false
	}
	return s, true
}

func (h Home) writeServing(s Serving) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(h.Dir, 0o700); err != nil {
		return err
	}
	tmp := h.servingPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, h.servingPath())
}

// Release clears the record, but only if it is still this supervisor's: a switch writes the new
// record BEFORE the old supervisor is stopped, and the old one must not erase its successor.
func (h Home) Release(supervisor int) {
	b, err := os.ReadFile(h.servingPath())
	if err != nil {
		return
	}
	var s Serving
	if json.Unmarshal(b, &s) == nil && s.Supervisor == supervisor {
		_ = os.Remove(h.servingPath())
	}
	_ = os.Remove(h.keyPath(supervisor))
}

func (h Home) keyPath(supervisor int) string {
	return filepath.Join(h.Dir, "serving-"+strconv.Itoa(supervisor)+".key")
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
	keyFile := h.keyPath(me)
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
		Supervisor: me, RuntimePID: srv.PID, BaseURL: srv.BaseURL, Key: key, Band: o.Model.Band,
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
