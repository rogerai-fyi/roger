// Package runtime supervises the inference server roger did not write.
//
// features/serving/local_models.feature rules that roger owns a runtime rather than writing one
// or linking one in. This is that ownership: start it, prove it answers, keep its words, stop it
// and give the memory back. The runtime is a program holding gigabytes and listening on a port,
// so everything that can go wrong with it is an operational failure an owner will blame roger
// for, and each one has a named answer here.
//
// FOUR RULES, each of which is a way this goes wrong when it is not followed:
//
//  1. NOTHING IS ANNOUNCED UNTIL IT REALLY ANSWERS. Not when the process starts - a process that
//     started is not a model that loaded. It has to pass its own health check AND list the band it
//     was asked for. "roger says it is serving" must mean the model answers.
//  2. A RUNTIME THAT DIES IS SAID TO HAVE DIED. Waiting out a thirty-second readiness deadline
//     for a process that exited two hundred milliseconds ago is the worst of both: slow AND
//     uninformative. The wait watches for the exit as well as the answer.
//  3. IT IS ROGER'S CHILD AND DIES WITH ROGER. This is the opposite of the detached agents
//     elsewhere in the tree: an agent should outlive the shell that started it, and an inference
//     runtime must not outlive the roger that owns it, or it holds the port and the memory with
//     nobody left to stop it. On Linux the kernel is asked to do it, so it holds even if roger is
//     killed outright.
//  4. STOPPING GIVES THE MEMORY BACK, even when the runtime will not go quietly. Ask politely,
//     wait, then insist - and say which of the two happened, because "it stopped" and "it had to
//     be killed" are different facts about somebody's machine.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// The ways starting can fail, as values: each has a different next step for whoever reads it.
var (
	ErrNotReady   = errors.New("the runtime did not answer in time")
	ErrExited     = errors.New("the runtime exited instead of serving")
	ErrWrongModel = errors.New("the runtime is serving a different model than it was asked for")
)

// MaxLogLines is how much of the runtime's own output is kept. Enough to explain a failure, few
// enough that a roger running for weeks beside a chatty runtime does not grow without limit.
const MaxLogLines = 200

// defaultReady is how long a runtime gets to load a model and answer. Loading gigabytes from a
// slow card is legitimately slow, so this is generous; a process that has DIED is noticed at once
// regardless, which is what makes a generous deadline affordable.
const defaultReady = 5 * time.Minute

// pollEvery is how often readiness is checked. Frequent enough to feel immediate, rare enough not
// to spin against a server that is busy loading.
const pollEvery = 250 * time.Millisecond

// Spec is one runtime to start. Args and Env are built by the caller so this package stays
// testable against any server that speaks the same shape, and so the flag names of a particular
// runtime live where that runtime is described rather than in the supervisor.
type Spec struct {
	Exe         string   // the runtime binary, already fetched and verified
	Model       string   // the weights on disk
	Band        string   // the model id it must advertise, and the one the Edge will route on
	Ctx         int      // the context it should serve at
	Port        int      // the loopback port. Zero is refused: a caller must choose, and know.
	Args        []string // the runtime's own flags
	Env         map[string]string
	ReadyWithin time.Duration
}

// Server is a running runtime, and what the rest of roger needs to use it.
type Server struct {
	BaseURL string // the OpenAI-compatible base, which is what internal/detect and the face consume
	Band    string
	PID     int
	Started time.Time

	cmd      *exec.Cmd
	mu       sync.Mutex
	lines    []string
	insisted bool
	stopped  bool
	waited   chan struct{}
	exitErr  error
}

// Start launches the runtime and returns only once it really answers for the band asked for.
// Every failure path stops what it started: a supervisor that leaves a process behind on the way
// out is worse than one that never started it.
func Start(ctx context.Context, sp Spec) (*Server, error) {
	if sp.Exe == "" || sp.Model == "" || sp.Band == "" {
		return nil, errors.New("a runtime needs a binary, a model and the band it will serve")
	}
	if sp.Port <= 0 {
		return nil, errors.New("a runtime needs a port chosen by its caller, so the caller knows it")
	}
	if fi, err := os.Stat(sp.Model); err != nil || fi.Size() <= 0 {
		return nil, fmt.Errorf("the model file is not there to serve: %s", sp.Model)
	}
	if _, err := os.Stat(sp.Exe); err != nil {
		return nil, fmt.Errorf("the runtime binary is not there: %s", sp.Exe)
	}

	cmd := exec.Command(sp.Exe, sp.Args...)
	cmd.Env = append(os.Environ(), envPairs(sp.Env)...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	// RULE 3. Ask the kernel to end it with us where the kernel can, so a roger that is killed
	// outright does not leave gigabytes resident with nobody to stop it.
	dieWithParent(cmd)

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &Server{
		BaseURL: "http://127.0.0.1:" + strconv.Itoa(sp.Port) + "/v1",
		Band:    sp.Band,
		PID:     cmd.Process.Pid,
		Started: time.Now(),
		cmd:     cmd,
		waited:  make(chan struct{}),
	}
	go s.drain(out)
	go s.drain(errPipe)
	// REAP IT. An unwaited child is a zombie for the life of the parent, and the exit status is
	// the one useful fact when a runtime refuses to start.
	go func() {
		s.exitErr = cmd.Wait()
		close(s.waited)
	}()

	if err := s.waitReady(ctx, sp); err != nil {
		_ = s.Stop(context.Background())
		return nil, err
	}
	return s, nil
}

// waitReady returns when the runtime answers for the right band, or says why it never will.
func (s *Server) waitReady(ctx context.Context, sp Spec) error {
	within := sp.ReadyWithin
	if within <= 0 {
		within = defaultReady
	}
	deadline := time.Now().Add(within)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.waited:
			// RULE 2: it is gone. Say that, with its status, rather than waiting out a deadline
			// for an answer that can no longer come.
			return fmt.Errorf("%w: %v%s", ErrExited, s.exitErr, s.tailForError())
		default:
		}
		if healthy(ctx, s.BaseURL) {
			served, err := modelsListed(ctx, s.BaseURL)
			if err == nil {
				for _, id := range served {
					if id == s.Band {
						return nil
					}
				}
				// RULE 1's subtle half: healthy, answering, and serving the WRONG weights.
				// Routing a turn to it would send somebody's work to a model they did not choose.
				return fmt.Errorf("%w: it lists %v, not %q", ErrWrongModel, served, s.Band)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w after %s%s", ErrNotReady, within, s.tailForError())
		}
		select {
		case <-time.After(pollEvery):
		case <-ctx.Done():
			return ctx.Err()
		case <-s.waited:
			return fmt.Errorf("%w: %v%s", ErrExited, s.exitErr, s.tailForError())
		}
	}
}

// Stop ends the runtime and does not return until the memory is actually back. Polite first,
// because a runtime may want to flush; insistent second, because a machine the owner asked to
// free must actually be freed.
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	already := s.stopped
	s.stopped = true
	s.mu.Unlock()
	if already || s.cmd == nil || s.cmd.Process == nil {
		return nil // stopping something already stopped is what the caller wanted
	}
	_ = terminate(s.cmd.Process)
	grace := 5 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		if until := time.Until(dl) / 2; until > 0 && until < grace {
			grace = until
		}
	}
	select {
	case <-s.waited:
		return nil
	case <-time.After(grace):
	}
	// It ignored the ask. Insist, and record that we had to, because that is a different fact
	// about this runtime and worth saying when it happens twice.
	s.mu.Lock()
	s.insisted = true
	s.mu.Unlock()
	_ = s.cmd.Process.Kill()
	select {
	case <-s.waited:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("the runtime (pid %d) would not stop", s.PID)
	}
}

// Insisted reports whether stopping needed more than a polite ask.
func (s *Server) Insisted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.insisted
}

// Note records a line about this runtime, so the supervisor's own observations sit in the same
// place as the runtime's output and a failure reads as one story.
func (s *Server) Note(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, line)
	if over := len(s.lines) - MaxLogLines; over > 0 {
		// Drop the OLDEST. When a runtime fails, the last thing it said is the useful part.
		s.lines = append([]string(nil), s.lines[over:]...)
	}
}

// Tail is the newest n lines, oldest first, for showing beside a failure.
func (s *Server) Tail(n int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 || n > len(s.lines) {
		n = len(s.lines)
	}
	return append([]string(nil), s.lines[len(s.lines)-n:]...)
}

// tailForError puts the runtime's last words into the error, because "it did not start" with
// nothing else is the least useful sentence a supervisor can produce.
func (s *Server) tailForError() string {
	last := s.Tail(3)
	if len(last) == 0 {
		return ""
	}
	return " - it said: " + last[len(last)-1]
}

func (s *Server) drain(r io.Reader) {
	buf := make([]byte, 4096)
	acc := ""
	for {
		n, err := r.Read(buf)
		if n > 0 {
			acc += string(buf[:n])
			for {
				i := indexByte(acc, '\n')
				if i < 0 {
					break
				}
				s.Note(acc[:i])
				acc = acc[i+1:]
			}
		}
		if err != nil {
			if acc != "" {
				s.Note(acc)
			}
			return
		}
	}
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// healthy asks the runtime's own health endpoint. A 200 is the only yes.
func healthy(ctx context.Context, base string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, trimV1(base)+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode == http.StatusOK
}

// modelsListed is what the runtime says it serves. This is deliberately the SAME check the Edge's
// serving probe makes over the wire, so a band cannot be proven by one and denied by the other.
func modelsListed(ctx context.Context, base string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models: status %d", resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		out = append(out, m.ID)
	}
	return out, nil
}

func trimV1(base string) string {
	if len(base) > 3 && base[len(base)-3:] == "/v1" {
		return base[:len(base)-3]
	}
	return base
}

func envPairs(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}
