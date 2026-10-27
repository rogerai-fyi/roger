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
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"rogerai.fm/roger/v6/internal/filelock"
	"rogerai.fm/roger/v6/internal/netbind"
	"rogerai.fm/roger/v6/internal/onair"
	"rogerai.fm/roger/v6/internal/provision"
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
	Build      string    `json:"build,omitempty"`   // cpu, vulkan, cuda or metal (local_models.feature §10)
	Adapter    string    `json:"adapter,omitempty"` // the LoRA file served on it, when one is
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

// procStart is the seam for when a pid's process started and whether it is a zombie; ok is false
// where that can't be read (then a live pid is believed, as before).
var procStart = onair.ProcessStart

// sameProcess reports whether the record's supervisor pid is still the process that wrote it. After
// a restart pids are reused: a process that started after the record was written, or a zombie, is
// not that serve's supervisor, and the record is stale.
func sameProcess(s Serving) bool {
	started, zombie, ok := procStart(s.Supervisor)
	if !ok {
		return true
	}
	if zombie {
		return false
	}
	return s.Started.IsZero() || !started.After(s.Started.Add(2*time.Second))
}

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
		if json.Unmarshal(b, &s) != nil || s.Supervisor <= 0 || !alive(s.Supervisor) || !sameProcess(s) {
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
	unlock, err := h.lockServing()
	if err == nil {
		defer unlock()
	}
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
	Adapter     *Adapter // a LoRA served beside the model, or nil (edge_device_runtime.feature §2)
	Exe         string   // the runtime, from runtime.Install
	Tag         string   // the runtime's version, recorded
	Ctx         int
	Port        int // zero picks a free loopback port
	Budget      Budget
	ReadyWithin time.Duration
	Env         map[string]string // the runtime's environment additions; tests use it
	Build       string            // what the runtime runs on, recorded with the serve
	Device      string            // one GPU to serve on ("CUDA2"), when the model fits on one; "" lets the runtime place it
}

// Args is the runtime's command line. Every flag a small machine needs is set explicitly rather
// than left to a default sized for a server, and the key goes in a FILE: a key on the command line
// is readable by every user on the machine through the process list.
func Args(o ServeOpts, port int, keyFile string) []string {
	args := []string{
		"-m", o.Model.Path,
		"--alias", o.Band(),
		"-c", strconv.Itoa(o.Ctx),
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--api-key-file", keyFile,
		"--parallel", "1",
		"--cache-ram", strconv.FormatInt(cacheRAMMiB(o.Budget), 10),
		"--no-webui",
		"--metrics", // its own token counters, behind the same key: how an idle timeout knows
	}
	if o.Device != "" {
		args = append(args, "--device", o.Device)
	}
	if o.Adapter != nil {
		args = append(args, "--lora", o.Adapter.Path)
	}
	return args
}

// Band is the name the serve answers to: the model's, and "+<adapter>" when a LoRA is on it, so
// a reader cannot mistake the adapted model for the base.
func (o ServeOpts) Band() string {
	if o.Adapter == nil {
		return o.Model.Band
	}
	return o.Model.Band + "+" + o.Adapter.Name()
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
	key, err := newKey()
	if err != nil {
		return nil, Serving{}, err
	}
	me := os.Getpid()
	if err := os.MkdirAll(h.Dir, 0o700); err != nil {
		return nil, Serving{}, err
	}
	// A free port is only free until someone binds it, so the port is claimed exclusively: a second
	// serve that picked the same one tries another rather than sharing it.
	port := o.Port
	var release func()
	for try := 0; release == nil; try++ {
		if o.Port == 0 {
			if port, err = netbind.SuggestPort(0); err != nil { // the runtime binds it; claimPort guards a race
				return nil, Serving{}, err
			}
		}
		if release, err = h.claimPort(port, o.Model); err != nil && (o.Port != 0 || try == 2) {
			return nil, Serving{}, err
		}
	}
	defer release()
	keyFile := h.keyPath(port)
	if err := os.WriteFile(keyFile, []byte(key+"\n"), 0o600); err != nil {
		return nil, Serving{}, err
	}
	srv, err := runtime.Start(ctx, runtime.Spec{
		Exe: o.Exe, Model: o.Model.Path, Band: o.Band(), Ctx: o.Ctx, Port: port, Key: key,
		Args: Args(o, port, keyFile), Env: o.Env, ReadyWithin: o.ReadyWithin,
	})
	if err != nil {
		_ = os.Remove(keyFile)
		return nil, Serving{}, err
	}
	s := Serving{
		Supervisor: me, RuntimePID: srv.PID, BaseURL: srv.BaseURL, Port: port, Key: key, Band: o.Band(),
		ID: o.Model.ID, File: o.Model.File, SHA256: o.Model.Sum(), Quant: o.Model.Quant, Ctx: o.Ctx,
		Runtime: o.Tag, Build: o.Build, Started: srv.Started,
	}
	if o.Adapter != nil {
		s.Adapter = o.Adapter.File
	}
	if err := h.writeServing(s); err != nil {
		_ = srv.Stop(context.Background())
		_ = os.Remove(keyFile)
		return nil, Serving{}, fmt.Errorf("serving started but could not be recorded, so it was stopped: %w", err)
	}
	if o.Model.Sum() == "" {
		go h.digestLater(o.Model, s) // a hand-placed file: say its digest once it is known
	}
	return srv, s, nil
}

// digestLater computes the digest of a file nobody checked, after it is on the air so the owner
// is not kept waiting, and records it on the serve and, for a file in the models directory,
// beside the file (edge_device_runtime.feature §1 "the digest of a hand-placed file is computed
// once and remembered"). It is a DIGEST, not a check: the file stays one placed by hand, which
// the disk budget never evicts. A file elsewhere (a USB stick, a download folder) gets no record
// written beside it and is digested again next time. Nothing is written for a serve that has
// since left the air: the record is re-read and must still be this serve's.
func (h Home) digestLater(m Model, s Serving) {
	defer digestDone()
	sum, err := provision.FileSum(m.Path)
	if err != nil {
		return
	}
	if within(h.Models(), m.Path) {
		m.Digest = sum
		_ = h.record(m)
	}
	unlock, err := h.lockServing() // a Release in flight runs before or after, never between
	if err != nil {
		return
	}
	defer unlock()
	if cur, ok := h.readServing(s.Port); ok && cur.Supervisor == s.Supervisor && cur.Started.Equal(s.Started) {
		digestHold()
		cur.SHA256 = sum
		_ = h.writeServing(cur)
	}
}

// lockServing serialises the two writers that read a serve record before changing it: the
// background digest and Release. It is one advisory OS lock on a dedicated file, released on
// exit, crashed or not. It creates no directory: a home that does not exist has no record to
// guard, and the caller goes on without the lock.
func (h Home) lockServing() (func(), error) {
	f, err := os.OpenFile(filepath.Join(h.Dir, "serving.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := filelock.Lock(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = filelock.Unlock(f); _ = f.Close() }, nil
}

// readServing is one port's record as it is on disk now, whoever wrote it.
func (h Home) readServing(port int) (Serving, bool) {
	b, err := os.ReadFile(h.recordPath(port))
	if err != nil {
		return Serving{}, false
	}
	var s Serving
	if json.Unmarshal(b, &s) != nil {
		return Serving{}, false
	}
	return s, true
}

// digestDone is signalled after each background digest; tests wait on it.
var digestDone = func() {}

// digestHold runs between the digest's read of the serve record and its write; tests use it.
var digestHold = func() {}

// portHeld reports whether a live serve, or one still loading, holds port.
func (h Home) portHeld(port int) bool {
	for _, s := range h.LiveAll() {
		if s.Port == port {
			return true
		}
	}
	var l loading
	b, err := os.ReadFile(h.loadingPath(port))
	return err == nil && json.Unmarshal(b, &l) == nil && alive(l.PID)
}

func newKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// RecordOf is where a serve's record (and so its key) is kept, for the owner to find.
func (h Home) RecordOf(s Serving) string { return h.recordPath(s.Port) }

// loading is a serve that has started its runtime but is not yet proven, so it has no record: a
// disk budget must not remove the file it is loading.
type loading struct {
	ID  string `json:"id"`
	PID int    `json:"pid"`
}

func (h Home) loadingPath(port int) string {
	return filepath.Join(h.Dir, "serving-"+strconv.Itoa(port)+".loading")
}

// claimPort makes port this serve's until release: the claim is an exclusive file naming the model
// and this roger, so two serves that picked the same port cannot share it (or each other's key), and
// a disk budget sees the model as being loaded. A claim left by a roger that died is stale and taken.
func (h Home) claimPort(port int, m Model) (release func(), err error) {
	for _, s := range h.LiveAll() {
		if s.Port == port {
			return nil, fmt.Errorf("port %d is already serving %s", port, s.Band)
		}
	}
	p := h.loadingPath(port)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil && os.IsExist(err) && !h.portHeld(port) {
		_ = os.Remove(p)
		f, err = os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	}
	if err != nil {
		return nil, fmt.Errorf("port %d is already claimed by another serve: %w", port, err)
	}
	b, _ := json.Marshal(loading{ID: m.ID, PID: os.Getpid()})
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(p)
		return nil, werr
	}
	return func() { _ = os.Remove(p) }, nil
}

// loadingIDs is every model a live roger is loading right now.
func (h Home) loadingIDs() []string {
	paths, _ := filepath.Glob(filepath.Join(h.Dir, "serving-*.loading"))
	var out []string
	for _, p := range paths {
		var l loading
		if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &l) == nil && l.ID != "" && alive(l.PID) {
			out = append(out, l.ID)
		}
	}
	return out
}

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
