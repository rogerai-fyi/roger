package main

// THE RUNNER for features/models/quiet_output.feature (approved 2026-10-13): `roger model get` and
// `serve` in a few lines by default, today's full account with --verbose. A stand-in for the model
// hub serving GGUF files shaped like SmolLM2-135M (the real hub is never called), a real models
// home on disk, and this test binary as the runtime and its fit tool (TestMain).
//
// The byte-for-byte scenarios compare against goldens captured from the 915f5039 code itself
// (QUIET_GOLDEN_UPDATE=1 rewrites them; only ever do that on a build whose output is the reference).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/localmodel"
	"rogerai.fm/roger/v6/internal/provision"
	"rogerai.fm/roger/v6/internal/runtime"
)

const quietFeature = "../../features/models/quiet_output.feature"

const (
	smolQ4     = "SmolLM2-135M-Instruct-Q4_K_M.gguf"
	smolQ8     = "SmolLM2-135M-Instruct-Q8_0.gguf"
	smolQ4Size = 105_487_000 // 100.6 MiB, as the real file says
	smolQ8Size = 144_808_000 // 138.1 MiB
	smolQ4ID   = smolRepo + ":Q4_K_M"
	smolQ8ID   = smolRepo + ":Q8_0"
	qwenSmall  = "Qwen/Qwen3-0.6B-GGUF:Q8_0"
	// specDigest is the Q4_K_M file's digest as the real run printed it; the stand-in file has its own.
	specDigest = "2e8040ceae78"
)

// smolGGUF is a header with SmolLM2-135M's own numbers, so roger's arithmetic is the real run's.
func smolGGUF() []byte {
	return ggufOf([]ggufKV{
		{"general.architecture", "llama"},
		{"llama.block_count", uint32(30)},
		{"llama.context_length", uint32(8192)},
		{"llama.embedding_length", uint32(576)},
		{"llama.attention.head_count", uint32(9)},
		{"llama.attention.head_count_kv", uint32(3)},
		{"llama.vocab_size", uint32(49152)},
	}, 0)
}

// smolDigest is the sha256 of the header padded with zeros to size, the file the stand-ins hold.
func smolDigest(size int) string {
	h := sha256.New()
	head := smolGGUF()
	h.Write(head)
	zero := make([]byte, 1<<20)
	for left := size - len(head); left > 0; left -= len(zero) {
		h.Write(zero[:min(left, len(zero))])
	}
	return hex.EncodeToString(h.Sum(nil))
}

var (
	smolQ4Digest = sync.OnceValue(func() string { return smolDigest(smolQ4Size) })
	smolQ8Digest = sync.OnceValue(func() string { return smolDigest(smolQ8Size) })
	smolQ8Body   = sync.OnceValue(func() []byte {
		b := make([]byte, smolQ8Size)
		copy(b, smolGGUF())
		return b
	})
)

type quietBDD struct {
	t                    *testing.T
	r                    *modelRig
	hub                  *fakeHub
	root                 string // what <config> stands for: the models home is <config>/rogerai/local
	out                  string
	err                  error
	answers              string
	terminal             bool
	env                  map[string]string
	outs                 []string // outputs a scenario compares
	signalled            []int
	ports                map[string]string // a port a free one stood in for -> the spec's
	cmd                  string
	verdicts             []string
	ran                  []string // every command a "get or serve" step ran, with its output
	errLine              string
	failCmd, failureName string // the failure a scenario set up, and the command that meets it
	contradiction        bool   // §5: the cuda check says yes and the CPU check says no
}

func (s *quietBDD) reset() error {
	s.r = newModelRig(s.t, 16384)
	root, err := os.MkdirTemp("", "quiet-output-")
	if err != nil {
		return err
	}
	s.root = root
	home := localmodel.Home{Dir: filepath.Join(root, "rogerai", "local")}
	s.r.home = home
	modelHome = func() localmodel.Home { return home }
	s.out, s.err, s.answers, s.terminal, s.outs, s.signalled, s.cmd, s.verdicts, s.ran, s.errLine = "", nil, "n\n", false, nil, nil, "", nil, nil, ""
	s.env = map[string]string{"FAKE_LLAMA": "1", "FAKE_FIT": "yes", "FAKE_FIT_P": "CUDA0 101 180 349"}
	modelServeEnv = s.env
	modelFitExe = func(string) string { return os.Args[0] }
	modelOutTerminal = func() bool { return s.terminal }
	s.ports = map[string]string{}
	oldSig, oldIs, oldSpawn, oldWait, oldInvoke, oldIdle := edgeSignal, edgeProcIsRoger, modelSpawn, modelDetachWait, modelEdgeInvoke, modelIdleEvery
	s.t.Cleanup(func() {
		edgeSignal, edgeProcIsRoger, modelSpawn, modelDetachWait, modelEdgeInvoke, modelIdleEvery = oldSig, oldIs, oldSpawn, oldWait, oldInvoke, oldIdle
	})
	edgeSignal = func(pid int, _ syscall.Signal) error { s.signalled = append(s.signalled, pid); return nil }
	edgeProcIsRoger = func(int) (bool, bool) { return true, true }
	modelDetachWait = 5 * time.Second
	modelSpawn = s.spawn
	return nil
}

// spawn stands in for the background roger: it comes on the air with the record a real one writes.
func (s *quietBDD) spawn(args []string, _ string) error {
	h := modelHome()
	m, err := h.Find(args[2])
	if err != nil {
		return err
	}
	port, ctxLen := 0, 0
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--port":
			port, _ = strconv.Atoi(args[i+1])
		case "--ctx":
			ctxLen, _ = strconv.Atoi(args[i+1])
		}
	}
	b, _ := json.Marshal(localmodel.Serving{Supervisor: os.Getppid(), Port: port, Band: m.Band, ID: m.ID, File: m.File,
		SHA256: m.SHA256, Quant: m.Quant, Ctx: ctxLen, BaseURL: fmt.Sprintf("http://127.0.0.1:%d/v1", port), Started: time.Now()})
	return os.WriteFile(h.RecordOf(localmodel.Serving{Port: port}), b, 0o600)
}

// place puts a model on this disk the way a verified fetch leaves it: the file and its record.
func (s *quietBDD) place(repo, file, quant, license string, body func(p string) error, size int64, digest string) error {
	h := modelHome()
	p, err := h.PathFor(repo, file)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if err := body(p); err != nil {
		return err
	}
	rec, _ := json.Marshal(localmodel.Model{ID: repo + ":" + quant, Repo: repo, File: file, Bytes: size, SHA256: digest,
		Quant: quant, License: license, Band: localmodel.BandFor(repo), Fetched: time.Now()})
	return os.WriteFile(p+".roger.json", rec, 0o600)
}

// sparseSmol writes the header and leaves the rest of the file a hole: 100 MiB that costs nothing.
func sparseSmol(size int64) func(string) error {
	return func(p string) error {
		if err := os.WriteFile(p, smolGGUF(), 0o600); err != nil {
			return err
		}
		return os.Truncate(p, size)
	}
}

func (s *quietBDD) machine() error {
	modelHW = func() detect.LocalHW {
		gpus := make([]detect.LocalGPU, 4)
		for i := range gpus {
			gpus[i] = detect.LocalGPU{Model: "NVIDIA RTX PRO 6000 Blackwell", VRAMMiB: 48947}
		}
		return detect.LocalHW{VRAMKnown: true, VRAMTotalMiB: 4 * 48947, RAMKnown: true, RAMTotalMiB: 257000, GPUs: gpus}
	}
	s.freeDisk(3_435_973_837) // 3.2 GiB
	return nil
}

func (s *quietBDD) freeDisk(n int64) {
	modelFetcher = func() *provision.Fetcher {
		return &provision.Fetcher{FreeSpace: func(string) (int64, bool) { return n, true }, OnProgress: modelProgress}
	}
}

func (s *quietBDD) q4Here(ref string) error {
	if ref != smolQ4ID {
		return fmt.Errorf("the runner places %s, not %s", smolQ4ID, ref)
	}
	return s.place(smolRepo, smolQ4, "Q4_K_M", "apache-2.0", sparseSmol(smolQ4Size), smolQ4Size, smolQ4Digest())
}

// q8NotHere is the hub's repository: Q4_K_M and Q8_0 listed with their true sizes and digests. The
// Q8_0 bytes are only made whole when a scenario fetches them (fullQ8).
func (s *quietBDD) q8NotHere(ref string) error {
	if ref != smolQ8ID {
		return fmt.Errorf("the runner lists %s, not %s", smolQ8ID, ref)
	}
	s.hub = newFakeHub(s.t)
	s.hub.add(&hubRepo{id: smolRepo, downloads: 77000, license: "apache-2.0", pipeline: "text-generation",
		files: map[string]*hubFile{
			smolQ4: {body: smolGGUF(), listSize: smolQ4Size, listSHA: smolQ4Digest()},
			smolQ8: {body: smolGGUF(), listSize: smolQ8Size, listSHA: smolQ8Digest()},
		}})
	modelHubBase = s.hub.srv.URL
	return nil
}

func (s *quietBDD) fullQ8() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	f := s.hub.repo0().files[smolQ8]
	f.body = smolQ8Body()
}

// repo0 is the SmolLM2 repository (the caller holds the lock).
func (h *fakeHub) repo0() *hubRepo { return h.repos[0] }

func (s *quietBDD) cuda() error {
	devs := make([]runtime.Device, 4)
	for i := range devs {
		devs[i] = runtime.Device{ID: fmt.Sprintf("CUDA%d", i), Name: "NVIDIA RTX PRO 6000 Blackwell", TotalMiB: 48947, FreeMiB: 48000}
	}
	cudaExe := os.Args[0]
	modelRuntimeFor = func(_ context.Context, choice string) (modelRT, error) {
		if choice == "cpu" {
			return modelRT{Exe: os.Args[0], Tag: "b11146", Build: runtime.BuildCPU}, nil
		}
		return modelRT{Exe: cudaExe, Tag: "b11146", Build: runtime.BuildCUDA, Offloads: true, Devices: devs}, nil
	}
	return nil
}

// cudaCannotStart makes the cuda build's server a file that is not there; its fit tool still answers.
func (s *quietBDD) cudaCannotStart() {
	absent := filepath.Join(s.root, "cuda", "llama-server")
	prev := modelRuntimeFor
	modelRuntimeFor = func(ctx context.Context, choice string) (modelRT, error) {
		rt, err := prev(ctx, choice)
		if rt.Build == runtime.BuildCUDA {
			rt.Exe = absent
		}
		return rt, err
	}
}

// freePort is a port nobody holds now.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// exec runs a command line as the owner typed it, shell redirections included.
func (s *quietBDD) exec(line string) error {
	s.cmd = line
	pipe := false
	for _, tail := range []string{" < /dev/null | cat", " > fetch.log"} {
		if strings.HasSuffix(line, tail) {
			line, pipe = strings.TrimSuffix(line, tail), true
			if tail == " < /dev/null | cat" {
				s.answers = ""
			}
		}
	}
	if pipe {
		s.terminal = false
	}
	args := strings.Fields(strings.TrimPrefix(line, "roger model"))
	for i, a := range args {
		if a == "--port" && i+1 < len(args) {
			p, err := freePort()
			if err != nil {
				return err
			}
			s.ports[strconv.Itoa(p)] = args[i+1]
			args[i+1] = strconv.Itoa(p)
		}
		if a == "./"+smolQ4 {
			dir := filepath.Join(s.root, "elsewhere")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			if err := sparseSmol(smolQ4Size)(filepath.Join(dir, smolQ4)); err != nil {
				return err
			}
			args[i] = filepath.Join(dir, smolQ4)
		}
	}
	s.r.out.Reset()
	edgeStdin = strings.NewReader(s.answers)
	s.err = cmdModel(config{}, args)
	s.out = s.norm(s.r.out.String())
	s.errLine = ""
	if s.err != nil {
		s.errLine = s.norm("error: " + s.err.Error())
	}
	return nil
}

// norm says the output the way the spec's ground truth does: the spec's ports, <config>, and the
// real file's digest in place of the stand-in's.
func (s *quietBDD) norm(out string) string {
	for p, want := range s.ports {
		out = strings.ReplaceAll(out, "127.0.0.1:"+p, "127.0.0.1:"+want)
		out = strings.ReplaceAll(out, "serving-"+p+".json", "serving-"+want+".json")
		out = strings.ReplaceAll(out, "--port "+p, "--port "+want)
	}
	// A port nobody named is any free one: said as PORT, so two runs read the same.
	out = regexp.MustCompile(`127\.0\.0\.1:\d{4,5}/`).ReplaceAllStringFunc(out, func(m string) string {
		for _, want := range s.ports {
			if m == "127.0.0.1:"+want+"/" {
				return m
			}
		}
		return "127.0.0.1:PORT/"
	})
	out = regexp.MustCompile(`serving-\d{4,5}\.json`).ReplaceAllStringFunc(out, func(m string) string {
		for _, want := range s.ports {
			if m == "serving-"+want+".json" {
				return m
			}
		}
		return "serving-PORT.json"
	})
	out = strings.ReplaceAll(out, s.root, "<config>")
	out = strings.ReplaceAll(out, provision.ShortDigest(smolQ4Digest()), specDigest)
	out = regexp.MustCompile(`pid \d+`).ReplaceAllString(out, "pid N")
	out = regexp.MustCompile(`for \d+(\.\d+)?(ms|µs|ns|s)\b`).ReplaceAllString(out, "for T")
	return out
}

// full is what the owner saw: the output and, for a failure, main's error line.
func (s *quietBDD) full() string {
	if s.errLine == "" {
		return s.out
	}
	return s.out + s.errLine + "\n"
}

func lines(out string) []string { return strings.Split(strings.TrimRight(out, "\n"), "\n") }

// specText is the spec's wording with the hub's own name where the real output names it.
func specText(want string) string {
	return strings.ReplaceAll(want, "(the author's word, from the model hub)", "(the author's word, from HuggingFace)")
}

func (s *quietBDD) outputIsExactly(doc *godog.DocString) error {
	want := strings.TrimSpace(specText(doc.Content))
	got := strings.TrimRight(s.full(), "\n")
	if got != want {
		return fmt.Errorf("output differs\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	return nil
}

func (s *quietBDD) outputIsExactlyLine(want string) error {
	return s.outputIsExactly(&godog.DocString{Content: want})
}

// fitLine is the first line that is a verdict.
var fitLineRE = regexp.MustCompile(`^(fits|tight|will not fit|cannot say)`)

func fitLine(out string) string {
	for _, l := range lines(out) {
		if fitLineRE.MatchString(strings.TrimSpace(l)) {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// ---------- goldens: today's output, byte for byte ----------------------------------------------

var goldenNameRE = regexp.MustCompile(`[^a-z0-9]+`)

func goldenPath(key string) string {
	return filepath.Join("testdata", "quiet_output", strings.Trim(goldenNameRE.ReplaceAllString(strings.ToLower(key), "-"), "-")+".golden")
}

// golden compares got with the golden for key, or writes it under QUIET_GOLDEN_UPDATE=1.
func golden(key, got string) error {
	p := goldenPath(key)
	if os.Getenv("QUIET_GOLDEN_UPDATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, []byte(got), 0o644)
	}
	want, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if string(want) != got {
		return fmt.Errorf("not byte for byte what 915f5039 printed (%s)\n--- got ---\n%s\n--- want ---\n%s", p, got, want)
	}
	return nil
}

// ---------- the steps ----------------------------------------------------------------------------

func (s *quietBDD) runsAndAnswers(cmd, ans string) error {
	s.answers = ans + "\n"
	if ans == "y" {
		s.fullQ8()
	}
	return s.exec(cmd)
}

func (s *quietBDD) runs(cmd string) error {
	if strings.Contains(cmd, "--yes") {
		s.fullQ8()
	}
	return s.exec(cmd)
}

// ownerRuns is every "the owner runs" step, routed by what the command is.
func (s *quietBDD) ownerRuns(cmd string) error {
	switch {
	case strings.Contains(cmd, "--on garage"):
		return s.memberVerbose(cmd)
	case strings.HasSuffix(cmd, " --verbose"):
		return s.runsVerbose(cmd)
	case cmd == "roger model status":
		return s.runsStatus()
	case strings.Contains(cmd, "--ctx 9999999"):
		return s.runsRefused(cmd)
	}
	return s.runs(cmd)
}

func (s *quietBDD) runsInTerminal(cmd string) error {
	s.terminal = true
	return s.runs(cmd)
}

func (s *quietBDD) nothingWritten() error {
	all, err := modelHome().List()
	if err != nil {
		return err
	}
	if len(all) != 1 || all[0].ID != smolQ4ID {
		return fmt.Errorf("the models on disk changed: %v", all)
	}
	parts, _ := filepath.Glob(filepath.Join(modelHome().Models(), "*", "*", "*.part"))
	if len(parts) > 0 {
		return fmt.Errorf("a partial file was written: %v", parts)
	}
	return nil
}

func (s *quietBDD) printedBefore(a, b, c string) error {
	out := s.full()
	ia, ib, ic := strings.Index(out, a), strings.Index(out, b), strings.Index(out, c)
	if ia < 0 || ib < 0 || ic < 0 || ia > ic || ib > ic {
		return fmt.Errorf("%q and %q are not both before %q:\n%s", a, b, c, out)
	}
	return nil
}

func (s *quietBDD) hubNoLicence() error {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.hub.repo0().license = ""
	return nil
}

func (s *quietBDD) getItAnswersN() error {
	return s.runsAndAnswers("roger model get "+smolQ8ID, "n")
}

func (s *quietBDD) line1Says(want string) error {
	if l := lines(s.out)[0]; !strings.Contains(l, specText(want)) {
		return fmt.Errorf("line 1 is %q, not saying %q\n%s", l, want, s.full())
	}
	return nil
}

func (s *quietBDD) twoLinesThenOneProgressLine() error {
	ls := strings.SplitN(s.out, "\n", 3)
	if len(ls) < 3 {
		return fmt.Errorf("fewer than two lines:\n%q", s.out)
	}
	rest := strings.SplitN(ls[2], "\n", 2) // the progress line, then the last line
	if !strings.Contains(rest[0], "fetch it?") && !strings.Contains(rest[0], "\r") {
		return fmt.Errorf("the third line is not one progress line redrawn in place: %q", rest[0])
	}
	if !strings.Contains(rest[0], "\r") || !strings.Contains(rest[0], "of 138.1 MiB") {
		return fmt.Errorf("no progress line redrawn in place after the two lines: %q", rest[0])
	}
	return nil
}

func (s *quietBDD) lastLineIs(want string) error {
	ls := lines(s.full())
	last := ls[len(ls)-1]
	if strings.Contains(want, "<why>") {
		prefix := strings.TrimSuffix(want, "<why>")
		if !strings.HasPrefix(last, prefix) || len(last) == len(prefix) {
			return fmt.Errorf("the last line is %q, not %q", last, want)
		}
		return nil
	}
	want = strings.ReplaceAll(want, "<first 12 hex>", provision.ShortDigest(smolQ8Digest()))
	if last != want {
		return fmt.Errorf("the last line is %q, not %q", last, want)
	}
	return nil
}

func (s *quietBDD) fetchPastAFiftieth() error {
	s.terminal = true
	s.r.out.Reset()
	modelProgress(provision.Progress{Done: smolQ8Size / 2, Total: smolQ8Size, Rate: 20 << 20, Remaining: 4 * time.Second})
	modelProgressDone()
	s.out = s.r.out.String()
	return nil
}

func (s *quietBDD) progressAsToday() error {
	for _, want := range []string{"69.0 MiB of 138.1 MiB", "20.0 MiB/s", "4s left"} {
		if !strings.Contains(s.out, want) {
			return fmt.Errorf("the progress line does not say %q: %q", want, s.out)
		}
	}
	return nil
}

func (s *quietBDD) budgetRemovesOne() error {
	return modelHome().SetSettings(localmodel.Settings{DiskBudget: smolQ4Size + smolQ8Size/2})
}

func (s *quietBDD) roomLineBeforeQuestion() error {
	out := s.full()
	i, q := strings.Index(out, smolQ4ID), strings.Index(out, "fetch it? [y/N]")
	room := strings.Index(out, "remov")
	if i < 0 || q < 0 || room < 0 || room > q {
		return fmt.Errorf("no room line naming %s before the question:\n%s", smolQ4ID, out)
	}
	return nil
}

func (s *quietBDD) runsAndCtrlC(cmd string) error { return s.exec(cmd) }

func (s *quietBDD) getThenServeLines() error {
	out := s.full()
	order := []string{smolQ8ID + " · 138.1 MiB", "fits at 8K context: needs", "fetch it? [y/N]", "of 138.1 MiB",
		`"smollm2-135m-instruct" · ` + smolQ8ID, "fits at 8K context by llama.cpp's own check", "on the air locally at", "ctrl-c or `roger model stop` ends it"}
	at := 0
	for _, w := range order {
		i := strings.Index(out[at:], w)
		if i < 0 {
			return fmt.Errorf("%q is not where it should be:\n%s", w, out)
		}
		at += i + len(w)
	}
	return nil
}

func (s *quietBDD) noFetchedLine() error {
	if strings.Contains(s.out, "fetched and verified") {
		return fmt.Errorf("the fetched line was printed separately:\n%s", s.out)
	}
	if !strings.Contains(s.out, "sha256 "+provision.ShortDigest(smolQ8Digest())) {
		return fmt.Errorf("serve's line 3 does not carry the digest:\n%s", s.out)
	}
	return nil
}

// beingServed puts a small qwen3-0.6b on this disk and on the air, served by another roger.
func (s *quietBDD) beingServed(band string) error {
	if band != "qwen3-0.6b" {
		return fmt.Errorf("the runner serves qwen3-0.6b, not %s", band)
	}
	body := modelGGUF(4000)
	if err := s.place("Qwen/Qwen3-0.6B-GGUF", "Qwen3-0.6B-Q8_0.gguf", "Q8_0", "apache-2.0",
		func(p string) error { return os.WriteFile(p, body, 0o600) }, int64(len(body)), hex.EncodeToString(sha256Sum(body))); err != nil {
		return err
	}
	b, _ := json.Marshal(localmodel.Serving{Supervisor: os.Getppid(), Port: 7101, Band: band, ID: qwenSmall,
		File: "Qwen3-0.6B-Q8_0.gguf", Ctx: 4096, BaseURL: "http://127.0.0.1:7101/v1", Started: time.Now()})
	return os.WriteFile(modelHome().RecordOf(localmodel.Serving{Port: 7101}), b, 0o600)
}

func sha256Sum(b []byte) []byte { h := sha256.Sum256(b); return h[:] }

func (s *quietBDD) servesWithout(band string) error {
	return s.exec("roger model serve " + band)
}

func (s *quietBDD) servesWith(band string) error {
	return s.exec("roger model serve " + band + " --add")
}

func (s *quietBDD) switchedFollowsLine3() error {
	ls := lines(s.out)
	for i, l := range ls {
		if strings.HasPrefix(l, "on the air locally") {
			if i+1 < len(ls) && strings.HasPrefix(ls[i+1], "switched from "+qwenSmall+": ") {
				return nil
			}
			return fmt.Errorf("line 3 is not followed by the switched line:\n%s", s.out)
		}
	}
	return fmt.Errorf("no line 3:\n%s", s.full())
}

func (s *quietBDD) oldStoppedAfter() error {
	if len(s.signalled) != 1 || s.signalled[0] != os.Getppid() {
		return fmt.Errorf("the old one was not stopped once the new one answered: %v", s.signalled)
	}
	return nil
}

func (s *quietBDD) line3Reads(want string) error {
	ls := lines(s.out)
	prefix := strings.TrimSuffix(want, "...")
	for _, l := range ls {
		if strings.HasPrefix(l, prefix) {
			return nil
		}
	}
	return fmt.Errorf("no line reads %q:\n%s", want, s.full())
}

func (s *quietBDD) idleSet() error {
	b, _ := json.Marshal(map[string]any{"idle_after": 1})
	return os.WriteFile(filepath.Join(modelHome().Dir, "settings.json"), b, 0o600)
}

func (s *quietBDD) idleServe() error {
	modelIdleEvery = 20 * time.Millisecond
	modelUntilDone = func(ctx context.Context) {
		select {
		case <-ctx.Done():
		case <-time.After(30 * time.Second):
		}
	}
	return s.exec("roger model serve smollm2-135m-instruct")
}

func (s *quietBDD) cudaCannotStartAuto() error {
	s.cudaCannotStart()
	return nil
}

func (s *quietBDD) aLineIsPrinted(want string) error {
	prefix, _, _ := strings.Cut(want, "<its last words>")
	_, suffix, _ := strings.Cut(want, "<its last words>")
	for _, l := range lines(s.out) {
		if strings.HasPrefix(l, prefix) && strings.HasSuffix(l, suffix) && len(l) > len(prefix)+len(suffix) {
			return nil
		}
	}
	return fmt.Errorf("no line %q:\n%s", want, s.full())
}

func (s *quietBDD) fitFollowsIsCPU() error {
	out := s.out
	fb := strings.Index(out, "serving with the CPU build instead")
	if fb < 0 {
		return fmt.Errorf("no fallback line:\n%s", out)
	}
	if fl := fitLine(out[fb:]); fl == "" || !strings.Contains(fl, "the cpu build") {
		return fmt.Errorf("the fit line after the fallback does not name the cpu build: %q\n%s", fl, out)
	}
	return nil
}

func (s *quietBDD) fourLinesName(build string) error {
	if !strings.Contains(s.out, "· "+build) {
		return fmt.Errorf("the lines do not name %q:\n%s", build, s.out)
	}
	return nil
}

func (s *quietBDD) runtimeNote() error {
	prev := modelRuntimeFor
	modelRuntimeFor = func(ctx context.Context, choice string) (modelRT, error) {
		rt, err := prev(ctx, choice)
		rt.Notes = []string{"the vulkan build was passed over: it lists no GPU it can use"}
		return rt, err
	}
	return nil
}

func (s *quietBDD) notesBeforeLine1() error {
	n, l1 := strings.Index(s.out, "the vulkan build was passed over"), strings.Index(s.out, `"smollm2-135m-instruct" · `)
	if n < 0 || l1 < 0 || n > l1 {
		return fmt.Errorf("the note is not before line 1:\n%s", s.full())
	}
	return nil
}

func (s *quietBDD) fitLineContains(want string) error {
	if fl := fitLine(s.out); !strings.Contains(fl, want) {
		return fmt.Errorf("the fit line %q does not contain %q\n%s", fl, want, s.full())
	}
	return nil
}

func (s *quietBDD) servesAModelHere() error {
	if err := s.exec("roger model serve smollm2-135m-instruct"); err != nil {
		return err
	}
	s.verdicts = append(s.verdicts, fitLine(s.out))
	modelFitExe = func(string) string { return filepath.Join(s.root, "no-fit-tool") }
	if err := s.exec("roger model serve smollm2-135m-instruct"); err != nil {
		return err
	}
	s.verdicts = append(s.verdicts, fitLine(s.out))
	return nil
}

func (s *quietBDD) fitByRuntime() error {
	if !strings.Contains(s.verdicts[0], "by llama.cpp's own check") {
		return fmt.Errorf("the runtime answered, and the fit line is %q", s.verdicts[0])
	}
	return nil
}

func (s *quietBDD) fitByEstimate() error {
	if !strings.Contains(s.verdicts[1], "(roger's estimate - the runtime's own check could not run: ") {
		return fmt.Errorf("the runtime could not answer, and the fit line is %q", s.verdicts[1])
	}
	return nil
}

func (s *quietBDD) getAndServe() error {
	for _, cmd := range []string{"roger model get " + smolQ8ID, "roger model serve smollm2-135m-instruct"} {
		s.answers = "n\n"
		if err := s.exec(cmd); err != nil {
			return err
		}
		s.ran = append(s.ran, s.full())
	}
	return nil
}

func (s *quietBDD) fitStatesTotal() error {
	get, serve := fitLine(s.ran[0]), fitLine(s.ran[1])
	if !regexp.MustCompile(`needs \d+(\.\d+)? \S+ of 191\.2 GiB video memory across 4 GPUs`).MatchString(get) {
		return fmt.Errorf("roger's estimate does not state the total and the budget: %q", get)
	}
	if !regexp.MustCompile(`needs \d+(\.\d+)? \S+`).MatchString(serve) {
		return fmt.Errorf("the runtime's verdict does not state the total: %q", serve)
	}
	return nil
}

// verdictHere sizes the machine so the Q8_0 file's verdict is the one named (it needs about 1.4 GiB).
func (s *quietBDD) verdictHere(v string) error {
	one := func(vram int) func() detect.LocalHW {
		return func() detect.LocalHW {
			return detect.LocalHW{VRAMKnown: true, VRAMTotalMiB: vram, GPUs: []detect.LocalGPU{{Model: "NVIDIA RTX 4090", VRAMMiB: vram}}}
		}
	}
	switch v {
	case "fits":
	case "tight":
		modelHW = one(1600)
	case "will not fit":
		modelHW = one(1024)
	case "cannot say":
		modelHW = func() detect.LocalHW { return detect.LocalHW{} }
	default:
		return fmt.Errorf("no verdict %q", v)
	}
	return nil
}

var sumRE = regexp.MustCompile(`weights .* = `)

func (s *quietBDD) sumLine(how string) error {
	has := sumRE.MatchString(s.out)
	switch how {
	case "is not printed":
		if has {
			return fmt.Errorf("the sum is printed for a comfortable fit:\n%s", s.out)
		}
	case "is printed":
		if !has {
			return fmt.Errorf("the sum is not printed:\n%s", s.out)
		}
	case "is replaced by the missing reading, named":
		if has || !strings.Contains(fitLine(s.out), "cannot say - this machine's memory could not be read") {
			return fmt.Errorf("the missing reading is not named in place of the sum:\n%s", s.out)
		}
	default:
		return fmt.Errorf("no such sum expectation %q", how)
	}
	return nil
}

func (s *quietBDD) noSpeed() error {
	for _, out := range s.ran {
		if strings.Contains(out, "tok/s") {
			return fmt.Errorf("a speed is stated:\n%s", out)
		}
	}
	return nil
}

func (s *quietBDD) statusStatesSpeed() error {
	var st string
	modelUntilDone = func(context.Context) { st, _ = s.r.run(s.t, "status") }
	if err := s.exec("roger model serve smollm2-135m-instruct"); err != nil {
		return err
	}
	if !strings.Contains(st, "speed    ") {
		return fmt.Errorf("status does not state the speed:\n%s", st)
	}
	return nil
}

func (s *quietBDD) statusDuringServe() error {
	var st string
	modelUntilDone = func(context.Context) { st, _ = s.r.run(s.t, "status") }
	if err := s.exec("roger model serve smollm2-135m-instruct"); err != nil {
		return err
	}
	s.outs = append(s.outs, st)
	return nil
}

func (s *quietBDD) runsStatus() error {
	if len(s.outs) > 0 {
		return nil // the status was read while it served
	}
	return s.exec("roger model status")
}

func (s *quietBDD) statusInFull() error {
	st := s.outs[0]
	for _, want := range []string{`serving "smollm2-135m-instruct"`, "model    " + smolQ4ID, "file     " + smolQ4, "quant    Q4_K_M",
		"sha256   " + smolQ4Digest(), "context  8192", "runtime  llama.cpp b11146, the cuda build, pid", "address  http://127.0.0.1:", "speed    ", "idle"} {
		if !strings.Contains(st, want) {
			return fmt.Errorf("status does not say %q:\n%s", want, st)
		}
	}
	return nil
}

func (s *quietBDD) refusedWithArithmetic() error {
	if s.err == nil {
		return fmt.Errorf("it was not refused:\n%s", s.out)
	}
	first := lines(s.err.Error())[0]
	for _, want := range []string{"9999999", "will not fit", "by llama.cpp's own check", "fits at 8K"} {
		if !strings.Contains(first, want) {
			return fmt.Errorf("the refusal %q does not say %q", first, want)
		}
	}
	return nil
}

func (s *quietBDD) errLineN(n int) string {
	ls := lines(s.err.Error())
	if n >= len(ls) {
		return ""
	}
	return strings.TrimSpace(ls[n])
}

func (s *quietBDD) nextIsSum() error {
	if l := s.errLineN(1); !strings.Contains(l, " = ") || !strings.HasPrefix(l, "model ") {
		return fmt.Errorf("the line after the refusal is not the sum: %q", l)
	}
	return nil
}

func (s *quietBDD) nextIsCommand(want string) error {
	if l := s.errLineN(2); l != want {
		return fmt.Errorf("the line after the sum is %q, not %q", l, want)
	}
	return nil
}

func (s *quietBDD) notNeedVerbose() error {
	if strings.Contains(s.cmd, "--verbose") {
		return errors.New("the command asked for --verbose")
	}
	return nil
}

// refuseAt9999999 is the runtime's own no at that context, with the context it would choose.
func (s *quietBDD) refuseAt9999999() {
	s.env["FAKE_FIT"] = "no"
	s.env["FAKE_FIT_MAX"] = "8192"
	s.env["FAKE_FIT_P"] = "CUDA0 101 415000 349"
}

func (s *quietBDD) runsRefused(cmd string) error {
	if strings.Contains(cmd, "--ctx 9999999") && s.env["FAKE_FIT"] == "yes" && !s.contradiction {
		s.refuseAt9999999()
	}
	return s.exec(cmd)
}

// ---------- failures, byte for byte ---------------------------------------------------------------

func (s *quietBDD) failure(name string) error {
	s.failureName = name
	switch name {
	case "no room on the disk":
		s.freeDisk(10 << 20)
		s.failCmd = "roger model get " + smolQ8ID + " --yes"
	case "no network":
		modelHubBase = "http://127.0.0.1:1"
		s.failCmd = "roger model get " + smolQ8ID
	case "the index refuses the request":
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
		s.t.Cleanup(srv.Close)
		modelHubBase = srv.URL
		s.failCmd = "roger model get " + smolQ8ID
	case "a gated model with no token":
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Error-Code", "GatedRepo")
			w.WriteHeader(http.StatusUnauthorized)
		}))
		s.t.Cleanup(srv.Close)
		modelHubBase = srv.URL
		s.failCmd = "roger model get acme/Locked-GGUF:Q4_K_M"
	case "a file whose digest does not match":
		s.hub.mu.Lock()
		s.hub.repo0().files[smolQ8].listSHA = strings.Repeat("0", 64)
		s.hub.mu.Unlock()
		s.fullQ8()
		s.failCmd = "roger model get " + smolQ8ID + " --yes"
	case "an explicit gpu choice whose build cannot start the model":
		s.cudaCannotStart()
		if err := modelHome().SetSettings(localmodel.Settings{Runtime: "gpu"}); err != nil {
			return err
		}
		s.failCmd = "roger model serve smollm2-135m-instruct"
	case "a new model that fails to start while another is serving":
		if err := s.beingServed("qwen3-0.6b"); err != nil {
			return err
		}
		absent := filepath.Join(s.root, "absent", "llama-server")
		modelRuntimeFor = func(context.Context, string) (modelRT, error) {
			return modelRT{Exe: absent, Tag: "b11146", Build: runtime.BuildCPU}, nil
		}
		s.failCmd = "roger model serve smollm2-135m-instruct"
	case "two models that do not fit together":
		if err := s.beingServed("qwen3-0.6b"); err != nil {
			return err
		}
		modelHW = func() detect.LocalHW {
			return detect.LocalHW{VRAMKnown: true, VRAMTotalMiB: 2048, GPUs: []detect.LocalGPU{{Model: "NVIDIA RTX 4090", VRAMMiB: 2048}}}
		}
		s.failCmd = "roger model serve smollm2-135m-instruct --add"
	case "an embedding model asked to serve chat":
		p := filepath.Join(s.root, "embed.gguf")
		body := ggufOf([]ggufKV{{"general.architecture", "bert"}, {"bert.block_count", uint32(12)},
			{"bert.pooling_type", uint32(1)}, {"bert.embedding_length", uint32(384)}}, 1000)
		if err := os.WriteFile(p, body, 0o600); err != nil {
			return err
		}
		s.failCmd = "roger model serve " + p
	case "a runtime that cannot be fetched or verified":
		modelRuntimeFor = func(context.Context, string) (modelRT, error) {
			return modelRT{}, errors.New("the runtime archive's sha256 is not the one pinned in this build")
		}
		s.failCmd = "roger model serve smollm2-135m-instruct"
	default:
		return fmt.Errorf("no such failure %q", name)
	}
	return nil
}

func (s *quietBDD) runsGetOrServe() error {
	if s.failCmd != "" {
		return s.exec(s.failCmd)
	}
	return s.getAndServe()
}

func (s *quietBDD) errorAsToday() error {
	if s.err == nil {
		return fmt.Errorf("%s did not fail:\n%s", s.failCmd, s.out)
	}
	return golden("failure "+s.failureName, s.errLine+"\n")
}

func (s *quietBDD) declinesAFetch() error { return s.runsAndAnswers("roger model get "+smolQ8ID, "n") }

func (s *quietBDD) exitStatus(n int) error {
	got := 0
	if s.err != nil {
		got = exitCode(s.err)
	}
	if got != n {
		return fmt.Errorf("exit status %d, not %d (%v)", got, n, s.err)
	}
	return nil
}

func (s *quietBDD) outputEnds(want string) error {
	if !strings.HasSuffix(strings.TrimRight(s.full(), "\n"), want) {
		return fmt.Errorf("the output does not end %q:\n%s", want, s.full())
	}
	return nil
}

func (s *quietBDD) aServeFails() error { return s.exec("roger model serve no-such-model-xyz") }

// ---------- §5 the contradiction --------------------------------------------------------------

func (s *quietBDD) cudaSaysFits() error {
	s.contradiction = true
	s.env["FAKE_FIT_P"] = "CUDA0 101 415000 349"
	return nil
}

func (s *quietBDD) cudaFailsCPUWillNot() error {
	s.cudaCannotStart()
	prev := modelRuntimeFor
	modelRuntimeFor = func(ctx context.Context, choice string) (modelRT, error) {
		if choice == "cpu" {
			s.env["FAKE_FIT"], s.env["FAKE_FIT_MAX"] = "no", "8192" // the CPU build's own check says no
		}
		return prev(ctx, choice)
	}
	return nil
}

var verdictLineRE = regexp.MustCompile(`(^|: )(fits|will not fit) at \S+ context`)

func (s *quietBDD) noUncontradictedFits() error {
	ls := lines(s.full())
	for i, l := range ls {
		if strings.Contains(l, "fits at 9999999 context") && !strings.Contains(l, "will not fit") {
			later := strings.Join(ls[i+1:], "\n")
			if !strings.Contains(later, "the cuda build could not start it") {
				return fmt.Errorf("%q stands with no later line saying the cuda build could not start it:\n%s", l, s.full())
			}
		}
	}
	return nil
}

// namesBuild reports whether verdict line i says whose check it is: in itself, or on the start
// line that follows it.
func namesBuild(ls []string, i int) bool {
	if strings.Contains(strings.ToLower(ls[i]), " build") {
		return true
	}
	return i+1 < len(ls) && strings.HasPrefix(ls[i+1], "starting llama.cpp") && strings.Contains(ls[i+1], " build")
}

func (s *quietBDD) notEndingUntold() error {
	ls := lines(s.full())
	last := ls[len(ls)-1]
	if strings.Contains(last, "will not fit") && !namesBuild(ls, len(ls)-1) {
		return fmt.Errorf("it ends with a will-not-fit error that does not say whose check it is:\n%s", s.full())
	}
	return nil
}

func (s *quietBDD) eachVerdictNamesBuild() error {
	ls := lines(s.full())
	n := 0
	for i, l := range ls {
		if verdictLineRE.MatchString(strings.TrimPrefix(l, "error: ")) {
			n++
			if !namesBuild(ls, i) {
				return fmt.Errorf("the verdict %q does not name the build whose check it is:\n%s", l, s.full())
			}
		}
	}
	if n == 0 {
		return fmt.Errorf("no verdict was printed:\n%s", s.full())
	}
	return nil
}

// ---------- §6 --verbose ------------------------------------------------------------------------

func (s *quietBDD) runsVerbose(cmd string) error {
	if strings.Contains(cmd, "--yes") {
		s.fullQ8()
	}
	if os.Getenv("QUIET_GOLDEN_UPDATE") == "1" {
		cmd = strings.TrimSuffix(cmd, " --verbose") // the reference build has no flag: its default is the full account
	}
	return s.exec(cmd)
}

func (s *quietBDD) byteForByte915(cmd string) error {
	return golden("verbose "+cmd, s.full())
}

func (s *quietBDD) sameAs(cmd string) error {
	got := s.full()
	if err := s.exec(cmd); err != nil {
		return err
	}
	if s.full() != got {
		return fmt.Errorf("not the same as %q\n--- got ---\n%s\n--- want ---\n%s", cmd, got, s.full())
	}
	return nil
}

func (s *quietBDD) neverNeedsValue() error {
	if strings.Contains(s.full(), "--verbose needs a value") {
		return errors.New(s.full())
	}
	return nil
}

func (s *quietBDD) memberVerbose(cmd string) error {
	modelEdgeInvoke = func(_ config, node string, task edge.ModelTask) (edge.ModelResult, string, error) {
		return edge.ModelResult{Says: smolQ4ID + ": fits at 8K context by llama.cpp's own check: needs 630.0 MiB, leaving its margin free - starting; it serves once proven"}, node, nil
	}
	return s.exec(cmd)
}

func (s *quietBDD) memberFullAccount() error {
	want := "garage: " + smolQ4ID + ": fits at 8K context by llama.cpp's own check: needs 630.0 MiB, leaving its margin free - starting; it serves once proven"
	if !strings.Contains(s.full(), want) {
		return fmt.Errorf("the member's answer is not its full account:\n%s", s.full())
	}
	return nil
}

func (s *quietBDD) foregroundServe() error { return s.exec("roger model serve smollm2-135m-instruct") }

func (s *quietBDD) line4Ends(want string) error {
	ls := lines(s.out)
	if len(ls) < 4 || !strings.HasSuffix(ls[3], want) {
		return fmt.Errorf("line 4 does not end %q:\n%s", want, s.full())
	}
	return nil
}

// ---------- §7 terminals, pipes, NO_COLOR -------------------------------------------------------

func (s *quietBDD) sameAsTerminal() error {
	piped := s.full()
	s.terminal, s.answers = true, "n\n"
	if err := s.exec("roger model get " + smolQ8ID); err != nil {
		return err
	}
	if s.full() != piped {
		return fmt.Errorf("a pipe is not the same as a terminal\n--- pipe ---\n%s\n--- terminal ---\n%s", piped, s.full())
	}
	s.out, s.errLine = piped, ""
	return nil
}

func (s *quietBDD) answeredNo() error {
	if err := s.outputEnds("not fetched. Nothing changed."); err != nil {
		return err
	}
	return s.nothingWritten()
}

func (s *quietBDD) logNoCR() error {
	if strings.Contains(s.out, "\r") {
		return fmt.Errorf("the log has carriage returns: %q", s.out)
	}
	return nil
}

var oneProgressRE = regexp.MustCompile(`(?m)^.*138\.1 MiB of 138\.1 MiB.*/s.*took .*$`)

func (s *quietBDD) logOneProgressLine() error {
	if n := len(oneProgressRE.FindAllString(s.out, -1)); n != 1 {
		return fmt.Errorf("%d lines with the bytes, the rate and the time it took, not 1: %q", n, s.out)
	}
	if strings.Count(s.out, " of 138.1 MiB") != 1 {
		return fmt.Errorf("more than one progress line: %q", s.out)
	}
	return nil
}

func (s *quietBDD) redrawnInPlace() error {
	i := strings.Index(s.out, "\r")
	if i < 0 {
		return fmt.Errorf("the progress is not redrawn in place: %q", s.out)
	}
	seg := s.out[i:]
	end := strings.Index(seg, "\n")
	if end < 0 || !strings.Contains(seg[:end], "138.1 MiB of 138.1 MiB") {
		return fmt.Errorf("the finished progress line does not stay on screen: %q", s.out)
	}
	return nil
}

func (s *quietBDD) noColorPairs() error {
	cmds := []string{"roger model get " + smolQ8ID, "roger model serve smollm2-135m-instruct", "roger model serve smollm2-135m-instruct --detach --port 18932"}
	for _, nc := range []string{"", "1"} {
		s.t.Setenv("NO_COLOR", nc)
		for _, c := range cmds {
			// Each pair starts from nothing on the air: the background serve's record is gone.
			recs, _ := filepath.Glob(filepath.Join(modelHome().Dir, "serving-*.json"))
			for _, r := range recs {
				_ = os.Remove(r)
			}
			s.answers = "n\n"
			if err := s.exec(c); err != nil {
				return err
			}
			s.outs = append(s.outs, s.full())
		}
	}
	return nil
}

func (s *quietBDD) pairsSame() error {
	n := len(s.outs) / 2
	for i := range n {
		if s.outs[i] != s.outs[n+i] {
			return fmt.Errorf("NO_COLOR changed the output\n--- without ---\n%s\n--- with ---\n%s", s.outs[i], s.outs[n+i])
		}
	}
	return nil
}

func (s *quietBDD) noANSI() error {
	for _, o := range s.outs {
		if strings.Contains(o, "\x1b") {
			return fmt.Errorf("an ANSI escape: %q", o)
		}
	}
	return nil
}

func (s *quietBDD) runsInColumns(cmd string, cols int) error {
	s.t.Setenv("COLUMNS", strconv.Itoa(cols))
	s.terminal = true
	return s.runs(cmd)
}

func (s *quietBDD) printedWhole() error {
	if strings.Contains(s.out, "…") {
		return fmt.Errorf("a line was cut:\n%s", s.out)
	}
	first := lines(s.out)[0]
	for _, w := range []string{"licence apache-2.0", "MiB"} {
		if !strings.Contains(first, w) {
			return fmt.Errorf("line 1 is not whole: %q", first)
		}
	}
	return nil
}

// ---------- §8 not applied ----------------------------------------------------------------------

func (s *quietBDD) asToday() error { return golden("today "+s.cmd, s.full()) }

func (s *quietBDD) fromTheScreens() error {
	m, err := modelHome().Find("smollm2-135m-instruct")
	if err != nil {
		return err
	}
	_, says, err := modelPlan(context.Background(), m, modelBudgetFor(true), 0, os.Args[0])
	if err != nil {
		return err
	}
	s.out = says
	return nil
}

func (s *quietBDD) screensUnchanged() error {
	return golden("screens plan says", s.out+"\n")
}

// ---------- wiring ------------------------------------------------------------------------------

func TestQuietOutputFeature(t *testing.T) {
	s := &quietBDD{t: t}
	selection := quietSelection(t)
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.failCmd, s.failureName, s.contradiction = "", "", false
				return ctx, s.reset()
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				_ = os.RemoveAll(s.root)
				return ctx, nil
			})
			sc.Step(`^a machine with 191\.2 GiB of video memory across 4 GPUs and 3\.2 GiB free on the model disk$`, s.machine)
			sc.Step(`^"([^"]+)" \(100\.6 MiB, licence apache-2\.0\) is on this machine$`, s.q4Here)
			sc.Step(`^"([^"]+)" \(138\.1 MiB\) is not$`, s.q8NotHere)
			sc.Step(`^the pinned runtime is installed and resolves to the cuda build$`, s.cuda)

			sc.Step(`^the owner runs "([^"]+)" and answers (y|n)$`, s.runsAndAnswers)
			sc.Step(`^the owner runs "([^"]+)" in a terminal$`, s.runsInTerminal)
			sc.Step(`^the owner runs "([^"]+)" and presses ctrl-c once it is on the air$`, s.runsAndCtrlC)
			sc.Step(`^the owner runs "([^"]+)" in a terminal (\d+) columns wide$`, s.runsInColumns)
			sc.Step(`^the owner runs "roger model get" for it and answers n$`, s.getItAnswersN)
			sc.Step(`^the owner runs "([^"]+)"$`, s.ownerRuns)
			sc.Step(`^the output is exactly:$`, s.outputIsExactly)
			sc.Step(`^the output is exactly "([^"]+)"$`, s.outputIsExactlyLine)
			sc.Step(`^nothing is written to the model disk$`, s.nothingWritten)
			sc.Step(`^"([^"]+)" and "([^"]+)" are printed before "([^"]+)"$`, s.printedBefore)
			sc.Step(`^a hub model whose index entry carries no licence$`, s.hubNoLicence)
			sc.Step(`^line 1 says "(.+)"$`, s.line1Says)
			sc.Step(`^the two lines are followed by one progress line that redraws in place$`, s.twoLinesThenOneProgressLine)
			sc.Step(`^the last line is "(.+)"(?:, as today)?$`, s.lastLineIs)
			sc.Step(`^a fetch that has moved more than a fiftieth of the file$`, s.fetchPastAFiftieth)
			sc.Step(`^the progress line says the bytes done of the total, the rate and the time left, as today$`, s.progressAsToday)
			sc.Step(`^the disk budget means 1 model must be removed to make room$`, s.budgetRemovesOne)
			sc.Step(`^the room line naming what would be removed is printed before the question, as today$`, s.roomLineBeforeQuestion)

			sc.Step(`^get's two lines and the question come first, then the progress line, then serve's four lines$`, s.getThenServeLines)
			sc.Step(`^the "fetched and verified" line is not printed separately, because the sha256 is on serve's line 3$`, s.noFetchedLine)
			sc.Step(`^"([^"]+)" is being served$`, func(band string) error {
				if band == "smollm2-135m-instruct" {
					return s.statusDuringServe()
				}
				return s.beingServed(band)
			})
			sc.Step(`^the owner serves "([^"]+)" without --add$`, s.servesWithout)
			sc.Step(`^the owner serves "([^"]+)" with --add$`, s.servesWith)
			sc.Step(`^one line "switched from <the old id>: <what the stop said>" follows line 3$`, s.switchedFollowsLine3)
			sc.Step(`^the old one is stopped only after the new one answers, as today$`, s.oldStoppedAfter)
			sc.Step(`^line 3 reads "(.+)"$`, s.line3Reads)
			sc.Step(`^an idle timeout of 10 minutes is set$`, s.idleSet)
			sc.Step(`^the serve has had no turn for 10 minutes$`, s.idleServe)
			sc.Step(`^the cuda build cannot start the model and the runtime choice is auto$`, s.cudaCannotStartAuto)
			sc.Step(`^a line "(.+)" is printed$`, s.aLineIsPrinted)
			sc.Step(`^the fit line that follows is the CPU build's own verdict$`, s.fitFollowsIsCPU)
			sc.Step(`^the four lines name "([^"]+)"$`, s.fourLinesName)
			sc.Step(`^the runtime resolves with a note \(for example a build that was settled earlier\)$`, s.runtimeNote)
			sc.Step(`^each note is printed before line 1, as today$`, s.notesBeforeLine1)

			sc.Step(`^the fit line contains "([^"]+)"$`, s.fitLineContains)
			sc.Step(`^the owner serves a model already on this machine$`, s.servesAModelHere)
			sc.Step(`^the fit line says "by llama\.cpp's own check" when the runtime answered$`, s.fitByRuntime)
			sc.Step(`^says "\(roger's estimate - the runtime's own check could not run: <why>\)" when it did not$`, s.fitByEstimate)
			sc.Step(`^the owner runs get or serve with the verdict "fits"$`, s.getAndServe)
			sc.Step(`^the fit line states "needs <total>", and for roger's own estimate also "of <budget> <pool>"$`, s.fitStatesTotal)
			sc.Step(`^a model whose verdict here is (.+)$`, s.verdictHere)
			sc.Step(`^the term-by-term sum line (.+)$`, s.sumLine)
			sc.Step(`^the owner runs get or serve$`, s.runsGetOrServe)
			sc.Step(`^neither of the four lines states a speed$`, s.noSpeed)
			sc.Step("^`roger model status` still states the speed, as today$", s.statusStatesSpeed)
			sc.Step(`^the output names the model, file, quant, full sha256, context, runtime with build and pid, address, speed and idle timeout, as today$`, s.statusInFull)

			sc.Step(`^the error names the context, says it will not fit by the check that refused it, and says the context that fits$`, s.refusedWithArithmetic)
			sc.Step(`^the next line is the term-by-term sum$`, s.nextIsSum)
			sc.Step(`^the next line is the exact command that would serve it: "([^"]+)"$`, s.nextIsCommand)
			sc.Step(`^--verbose is not needed to see any of it$`, s.notNeedVerbose)
			sc.Step(`^(no room on the disk|no network|the index refuses the request|a gated model with no token|a file whose digest does not match|an explicit gpu choice whose build cannot start the model|a new model that fails to start while another is serving|two models that do not fit together|an embedding model asked to serve chat|a runtime that cannot be fetched or verified)$`, s.failure)
			sc.Step(`^the error is byte for byte today's, with every line it has today$`, s.errorAsToday)
			sc.Step(`^the owner declines a fetch$`, s.declinesAFetch)
			sc.Step(`^the exit status is (\d+)(?: and the output ends "(.+)")?$`, func(n int, ends string) error {
				if err := s.exitStatus(n); err != nil || ends == "" {
					return err
				}
				return s.outputEnds(ends)
			})
			sc.Step(`^a serve fails$`, s.aServeFails)

			sc.Step(`^the cuda build's own check says a model fits at 9999999 context$`, s.cudaSaysFits)
			sc.Step(`^the cuda build then fails to load it and the CPU build's check says it will not fit$`, s.cudaFailsCPUWillNot)
			sc.Step(`^no line says "fits at 9999999 context" without a later line saying the cuda build could not start it$`, s.noUncontradictedFits)
			sc.Step(`^the command does not end with a "fits" line and a "will not fit" error from checks it does not tell apart$`, s.notEndingUntold)
			sc.Step(`^each verdict printed names the build whose check it is$`, s.eachVerdictNamesBuild)

			sc.Step(`^the output is byte for byte what the 915f5039 build prints for "([^"]+)"$`, s.byteForByte915)
			sc.Step(`^it is the same as "([^"]+)"$`, s.sameAs)
			sc.Step(`^it never says "--verbose needs a value"$`, s.neverNeedsValue)
			sc.Step(`^the member's answer is its full account$`, s.memberFullAccount)
			sc.Step(`^the owner runs a foreground serve$`, s.foregroundServe)
			sc.Step(`^line 4 ends "([^"]+)"$`, s.line4Ends)

			sc.Step(`^the output is the same two lines and the question as in a terminal$`, s.sameAsTerminal)
			sc.Step(`^the question is answered no, because there is no one to answer it, as today$`, s.answeredNo)
			sc.Step(`^fetch\.log contains no carriage return$`, s.logNoCR)
			sc.Step(`^it contains one line with the bytes fetched, the rate and the time it took$`, s.logOneProgressLine)
			sc.Step(`^the progress is one line redrawn in place, and it stays on screen when the fetch ends$`, s.redrawnInPlace)
			sc.Step(`^the owner runs get, serve and serve --detach with NO_COLOR set and without it$`, s.noColorPairs)
			sc.Step(`^each pair of outputs is byte for byte the same$`, s.pairsSame)
			sc.Step(`^neither contains an ANSI escape$`, s.noANSI)
			sc.Step(`^every quiet line is printed whole, wrapping as the terminal wraps it, never cut by roger$`, s.printedWhole)

			sc.Step(`^its output is byte for byte today's$`, s.asToday)
			sc.Step(`^the owner fetches or serves from SHARE in the TUI or from the console$`, s.fromTheScreens)
			sc.Step(`^what those screens show is unchanged$`, s.screensUnchanged)
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: selection},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("quiet_output.feature scenarios failed")
	}
}

// quietCatalogueScenario is the one scenario this runner does not select, and why: `roger model
// get` has no source but the model hub today - there is no catalogue entry it fetches - so there is
// nothing to tell apart yet. It is named, not silently dropped, and the founder has the question.
const quietCatalogueScenario = "a model from our own catalogue is told apart from the author's word without colour"

// quietSelection is every Scenario line of the feature but quietCatalogueScenario.
func quietSelection(t *testing.T) []string {
	b, err := os.ReadFile(quietFeature)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	skipped := false
	for i, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "Scenario:") && !strings.HasPrefix(l, "Scenario Outline:") {
			continue
		}
		if strings.Contains(l, quietCatalogueScenario) {
			skipped = true
			continue
		}
		out = append(out, quietFeature+":"+strconv.Itoa(i+1))
	}
	if !skipped {
		t.Fatal("the catalogue scenario is gone from the feature: select every scenario again")
	}
	return out
}
