package tui

// operator_steps_pure_test.go - godog step definitions for the PURE half of the Guest
// Operators Phase 2 spec set (features/operator/detection|config_*|config_isolation
// .feature): they bind directly against internal/operator with a real filesystem
// (per-scenario sandbox dirs), injected Env seams, and zero mocks. The TUI-driving steps
// live in operator_steps_tui_test.go; the shared suite state is opBDD (declared there).

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/operator"
)

// --- detection env seam ----------------------------------------------------------------

// pureEnv builds an operator.Env over the scenario's configured LookPath / probe tables.
func (s *opBDD) pureEnv() operator.Env {
	return operator.Env{
		LookPath: func(bin string) (string, error) {
			if err, ok := s.lookErr[bin]; ok {
				return "", err
			}
			if p, ok := s.pathOf[bin]; ok {
				return p, nil
			}
			return "", errors.New("executable file not found in $PATH")
		},
		Probe: func(bin string) (string, error) {
			for name, p := range s.pathOf {
				if p != bin {
					continue
				}
				if s.probeBlocks[name] {
					time.Sleep(10 * time.Millisecond) // the bounded seam returns AT the deadline
					return "", errors.New("probe deadline exceeded")
				}
				if err, ok := s.probeErrs[name]; ok {
					return "", err
				}
				if out, ok := s.probeOuts[name]; ok {
					return out, nil
				}
				return "", errors.New("no probe response configured")
			}
			return "", errors.New("unknown bin")
		},
	}
}

func (s *opBDD) findDetection(name string) (operator.Detection, bool) {
	for _, d := range s.ds {
		if d.Guest.Name == name {
			return d, true
		}
	}
	return operator.Detection{}, false
}

func (s *opBDD) detectionNames() []string {
	var out []string
	for _, d := range s.ds {
		out = append(out, d.Guest.Name)
	}
	return out
}

// --- registry steps ----------------------------------------------------------------------

func (s *opBDD) theGuestOperatorRegistry() error { return nil } // Background anchor

func (s *opBDD) registryListsExactly(a, b, c string) error {
	var names []string
	for _, g := range operator.Registry() {
		names = append(names, g.Name)
	}
	if want := []string{a, b, c}; !reflect.DeepEqual(names, want) {
		return fmt.Errorf("registry = %v, want %v", names, want)
	}
	return nil
}

func (s *opBDD) noRegistryEntryNamed(a, b string) error {
	for _, g := range operator.Registry() {
		if g.Name == a || g.Name == b {
			return fmt.Errorf("registry lists excluded entry %q", g.Name)
		}
	}
	return nil
}

func (s *opBDD) everyEntryCarriesFields() error {
	for _, g := range operator.Registry() {
		if g.Name == "" || g.Bin == "" || g.Provider == "" || g.InstallHint == "" || g.KnownGood == "" {
			return fmt.Errorf("%s: missing field(s) in %+v", g.Name, g)
		}
	}
	return nil
}

func registryGuest(name string) (operator.Guest, error) {
	for _, g := range operator.Registry() {
		if g.Name == name {
			return g, nil
		}
	}
	return operator.Guest{}, fmt.Errorf("guest %q not in registry", name)
}

func (s *opBDD) entryUsesStrategyWithVersion(name, strategy, version string) error {
	g, err := registryGuest(name)
	if err != nil {
		return err
	}
	if g.Strategy != strategy {
		return fmt.Errorf("%s strategy = %q, want %q", name, g.Strategy, strategy)
	}
	if g.KnownGood != version {
		return fmt.Errorf("%s known-good = %q, want %q", name, g.KnownGood, version)
	}
	return nil
}

func (s *opBDD) aiderEnvFlagsNoConfig() error {
	g, err := registryGuest("aider")
	if err != nil {
		return err
	}
	if g.Strategy != operator.StrategyEnvFlags {
		return fmt.Errorf("aider strategy = %q, want %q", g.Strategy, operator.StrategyEnvFlags)
	}
	return nil
}

// --- LookPath / probe givens ---------------------------------------------------------------

func (s *opBDD) lookPathResolves(bin, path string) error {
	s.pathOf[bin] = path
	delete(s.lookErr, bin)
	return nil
}

func (s *opBDD) lookPathFails(bin, msg string) error {
	delete(s.pathOf, bin)
	s.lookErr[bin] = errors.New(msg)
	return nil
}

func (s *opBDD) lookPathFailsForEverything() error {
	s.pathOf = map[string]string{}
	s.lookErr = map[string]error{}
	return nil
}

func (s *opBDD) lookPathResolvesEveryRegistryBinary() error {
	for _, g := range operator.Registry() {
		s.pathOf[g.Bin] = "/fake/" + g.Bin
	}
	return nil
}

func (s *opBDD) probeAnswers(guest, out string) error {
	s.probeOuts[guest] = out
	return nil
}

func (s *opBDD) probeFails(guest, msg string) error {
	s.probeErrs[guest] = errors.New(msg)
	return nil
}

func (s *opBDD) probeBlocksPastDeadline(guest string) error {
	s.probeBlocks[guest] = true
	return nil
}

// --- the scan --------------------------------------------------------------------------------

// deskScanned runs the sweep + detect exactly like the TUI's desk scan (operatorScanCmd):
// stale rogerai-operator-* leftovers are swept first, then the registry is detected.
func (s *opBDD) deskScanned() error {
	operator.SweepStale(s.scratchRoot, operatorStaleAge)
	s.ds = operator.Detect(s.pureEnv())
	return nil
}

func (s *opBDD) detectionsInOrder(a, b, c string) error {
	if want := []string{a, b, c}; !reflect.DeepEqual(s.detectionNames(), want) {
		return fmt.Errorf("detections = %v, want %v", s.detectionNames(), want)
	}
	return nil
}

func (s *opBDD) detectionsExactly(name string) error {
	if want := []string{name}; !reflect.DeepEqual(s.detectionNames(), want) {
		return fmt.Errorf("detections = %v, want %v", s.detectionNames(), want)
	}
	return nil
}

func (s *opBDD) detectionsEmpty() error {
	if len(s.ds) != 0 {
		return fmt.Errorf("detections = %v, want none", s.detectionNames())
	}
	return nil
}

func (s *opBDD) noErrorSurfaced() error { return nil } // Detect has no error path by design

func (s *opBDD) detectionsInclude(name string) error {
	if _, ok := s.findDetection(name); !ok {
		return fmt.Errorf("detections %v do not include %q", s.detectionNames(), name)
	}
	return nil
}

func (s *opBDD) detectionsDoNotInclude(name string) error {
	if _, ok := s.findDetection(name); ok {
		return fmt.Errorf("detections must not include %q", name)
	}
	return nil
}

func (s *opBDD) eachDetectionRecordsPath() error {
	for _, d := range s.ds {
		if d.Path == "" || d.Path != s.pathOf[d.Guest.Bin] {
			return fmt.Errorf("%s: path %q, want %q", d.Guest.Name, d.Path, s.pathOf[d.Guest.Bin])
		}
	}
	return nil
}

func (s *opBDD) detectionVersionVerified(name, version string) error {
	d, ok := s.findDetection(name)
	if !ok {
		return fmt.Errorf("%q not detected", name)
	}
	if d.Version != version || d.Unverified {
		return fmt.Errorf("%s: version=%q unverified=%v, want %q verified", name, d.Version, d.Unverified, version)
	}
	return nil
}

func (s *opBDD) detectionUnverifiedEmptyVersion(name string) error {
	d, ok := s.findDetection(name)
	if !ok {
		return fmt.Errorf("%q not detected", name)
	}
	if !d.Unverified || d.Version != "" {
		return fmt.Errorf("%s: version=%q unverified=%v, want unverified with empty version", name, d.Version, d.Unverified)
	}
	return nil
}

func (s *opBDD) detectionUnverified(name string) error {
	d, ok := s.findDetection(name)
	if !ok {
		return fmt.Errorf("%q not detected", name)
	}
	if !d.Unverified {
		return fmt.Errorf("%s must be unverified", name)
	}
	return nil
}

func (s *opBDD) detectionUnverifiedWithVersion(name, version string) error {
	d, ok := s.findDetection(name)
	if !ok {
		return fmt.Errorf("%q not detected", name)
	}
	if !d.Unverified || d.Version != version {
		return fmt.Errorf("%s: version=%q unverified=%v, want unverified with version %q", name, d.Version, d.Unverified, version)
	}
	return nil
}

func (s *opBDD) scanCompletes() error { return nil } // Detect returned (a hung probe would fail the step timeout)

func (s *opBDD) rawVersionParsed(raw, guest string) error {
	raw = strings.ReplaceAll(raw, `\n`, "\n") // the outline table carries literal \n
	s.parsed, s.parsedOK = operator.ParseVersion(guest, raw)
	return nil
}

func (s *opBDD) parsedVersionIs(want string) error {
	if !s.parsedOK || s.parsed != want {
		return fmt.Errorf("parsed = (%q, %v), want %q", s.parsed, s.parsedOK, want)
	}
	return nil
}

// noFileWrittenAnywhere: the scenario's sandbox roots stayed empty/untouched (detection
// is read-only; aider materialization writes nothing).
func (s *opBDD) noFileWrittenAnywhere() error {
	if got := treeSnapshot(s.scratchRoot); len(got) != 0 {
		return fmt.Errorf("files appeared under the scratch root: %v", keysOf(got))
	}
	if s.workSnap != nil {
		if got := treeSnapshot(s.sess.Workdir); !reflect.DeepEqual(got, s.workSnap) {
			return fmt.Errorf("the workdir changed: %v", keysOf(got))
		}
	}
	// The sandbox home too: nothing may appear there beyond files a Given planted.
	if got, planted := treeSnapshot(s.home), len(s.realFiles); len(got) != planted {
		return fmt.Errorf("files appeared under the home sandbox: %v", keysOf(got))
	}
	return nil
}

func (s *opBDD) noRequestHitProxy() error {
	if n := s.proxyHits.Load(); n != 0 {
		return fmt.Errorf("%d request(s) hit the local proxy during a read-only scan", n)
	}
	return nil
}

// --- config materialization ------------------------------------------------------------------

func (s *opBDD) liveProxySession(base, key, model string) error {
	s.sess = operator.Session{BaseURL: base, SessionKey: key, Model: model,
		Workdir: s.workdir, ScratchRoot: s.scratchRoot}
	return nil
}

func (s *opBDD) aSessionScratchDir() error { return nil } // the per-scenario root exists

func (s *opBDD) materialize(name string) error {
	g, err := registryGuest(name)
	if err != nil {
		return err
	}
	s.prevLaunch = s.launch
	s.launch, s.cleanup, s.matErr = operator.Materialize(g, s.sess)
	if s.matErr != nil {
		return fmt.Errorf("materialize %s: %v", name, s.matErr)
	}
	s.matGuest = name
	return nil
}

func (s *opBDD) materializeAndExit(name string) error {
	if err := s.materialize(name); err != nil {
		return err
	}
	return s.cleanup()
}

func (s *opBDD) materializeAgain(name string) error { return s.materialize(name) }

func (s *opBDD) scratchContainsExactlyOneFile(name string) error {
	got := keysOf(treeSnapshot(s.launch.Dir))
	if !reflect.DeepEqual(got, []string{name}) {
		return fmt.Errorf("scratch dir files = %v, want exactly [%s]", got, name)
	}
	return nil
}

func (s *opBDD) scratchContains(rel string) error {
	if _, err := os.Stat(filepath.Join(s.launch.Dir, filepath.FromSlash(rel))); err != nil {
		return fmt.Errorf("scratch dir lacks %s: %v", rel, err)
	}
	return nil
}

func (s *opBDD) fileEqualsGolden(rel string, doc *godog.DocString) error {
	b, err := os.ReadFile(filepath.Join(s.launch.Dir, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	want := doc.Content
	if got := string(b); got != want && got != want+"\n" {
		return fmt.Errorf("golden mismatch for %s:\n--- want ---\n%s\n--- got ---\n%s", rel, want, got)
	}
	return nil
}

func (s *opBDD) argvIsExactly(want string) error {
	if got := strings.Join(s.launch.Argv, " "); got != want {
		return fmt.Errorf("argv = %q, want %q", got, want)
	}
	return nil
}

func (s *opBDD) argvPins(frag string) error {
	// TUI scenarios (a plate-accepted exec) pin the REAL child command's argv; pure
	// materialization scenarios pin the Launch argv.
	if len(s.execCmds) > 0 {
		last := s.execCmds[len(s.execCmds)-1]
		if got := strings.Join(last.Args, " "); !strings.Contains(got, frag) {
			return fmt.Errorf("child argv %q does not pin %q", got, frag)
		}
		return nil
	}
	if got := strings.Join(s.launch.Argv, " "); !strings.Contains(got, frag) {
		return fmt.Errorf("argv %q does not pin %q", got, frag)
	}
	return nil
}

func (s *opBDD) argvContains(frag string) error { return s.argvPins(frag) }

func (s *opBDD) envAdditionsExactly(table *godog.Table) error {
	var want []string
	for _, row := range table.Rows {
		if len(row.Cells) != 2 {
			return fmt.Errorf("env table rows need 2 cells")
		}
		v := strings.ReplaceAll(row.Cells[1].Value, "<scratch>", s.launch.Dir)
		want = append(want, row.Cells[0].Value+"="+v)
	}
	got := append([]string(nil), s.launch.Env...)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("env additions = %v, want %v", s.launch.Env, want)
	}
	return nil
}

// parentEnvInherited: the additions are ONLY additions - composing over a parent env
// keeps every non-overridden parent var byte-identical.
func (s *opBDD) parentEnvInherited() error {
	parent := []string{"HOME=/keep/home", "PATH=/keep/path", "LANG=C.UTF-8"}
	composed := operator.ComposeEnv(parent, s.launch.Env)
	for _, kv := range parent {
		if !containsStr(composed, kv) {
			return fmt.Errorf("parent var %q lost in composition %v", kv, composed)
		}
	}
	if len(composed) != len(parent)+len(s.launch.Env) {
		return fmt.Errorf("composition len = %d, want parent+additions only", len(composed))
	}
	return nil
}

func (s *opBDD) noScratchFileContains(secret string) error {
	var offenders []string
	_ = filepath.Walk(s.scratchRoot, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			if b, e := os.ReadFile(p); e == nil && strings.Contains(string(b), secret) {
				offenders = append(offenders, p)
			}
		}
		return nil
	})
	if len(offenders) != 0 {
		return fmt.Errorf("the session key is on disk: %v", offenders)
	}
	return nil
}

func (s *opBDD) workdirHasProjectConfig(model string) error {
	p := filepath.Join(s.sess.Workdir, "opencode.json")
	body := []byte(`{"model":"` + model + `"}`)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		return err
	}
	s.projectCfg = body
	return nil
}

func (s *opBDD) projectConfigNotModified() error {
	b, err := os.ReadFile(filepath.Join(s.sess.Workdir, "opencode.json"))
	if err != nil || !reflect.DeepEqual(b, s.projectCfg) {
		return fmt.Errorf("the user's project opencode.json was modified (err=%v)", err)
	}
	return nil
}

func (s *opBDD) bandRetuned(model string) error {
	s.sess.Model = model
	return nil
}

func (s *opBDD) fileWiresModel(rel, model string) error {
	b, err := os.ReadFile(filepath.Join(s.launch.Dir, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	if !strings.Contains(string(b), model) {
		return fmt.Errorf("%s does not wire model %q:\n%s", rel, model, b)
	}
	return nil
}

func (s *opBDD) guestExitsCleanly() error { return s.cleanup() } // the return callback runs
func (s *opBDD) guestCrashesExit1() error { return s.cleanup() } // …for EVERY child outcome

func (s *opBDD) scratchDirGone() error {
	if s.launch.Dir == "" {
		return nil
	}
	if _, err := os.Stat(s.launch.Dir); !os.IsNotExist(err) {
		return fmt.Errorf("scratch dir %s still exists (err=%v)", s.launch.Dir, err)
	}
	return nil
}

// homePath resolves a "~/…" spec path into the scenario's sandboxed fake home.
func (s *opBDD) homePath(spec string) string {
	return filepath.Join(s.home, strings.TrimPrefix(spec, "~/"))
}

func (s *opBDD) userHasRealFile(spec string) error {
	p := s.homePath(spec)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	body := []byte("# the user's real config - " + spec + "\n")
	if err := os.WriteFile(p, body, 0o644); err != nil {
		return err
	}
	if s.realFiles == nil {
		s.realFiles = map[string][]byte{}
	}
	s.realFiles[spec] = body
	return nil
}

func (s *opBDD) realFileByteIdentical(spec string) error {
	b, err := os.ReadFile(s.homePath(spec))
	if err != nil || !reflect.DeepEqual(b, s.realFiles[spec]) {
		return fmt.Errorf("%s changed (err=%v)", spec, err)
	}
	return nil
}

func (s *opBDD) nothingWrittenUnder(spec string) error {
	dir := s.homePath(spec)
	got := keysOf(treeSnapshot(dir))
	var want []string
	for k := range s.realFiles {
		if rel, ok := strings.CutPrefix(k, strings.TrimSuffix(spec, "/")+"/"); ok {
			want = append(want, filepath.FromSlash(rel))
		} else if filepath.Dir(s.homePath(k)) == dir {
			want = append(want, filepath.Base(k))
		}
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("files under %s = %v, want only the pre-existing %v", spec, got, want)
	}
	return nil
}

func (s *opBDD) hermesConfigHasKeyedProviders() error {
	b, err := os.ReadFile(filepath.Join(s.launch.Dir, "hermes-home", "config.yaml"))
	if err != nil {
		return err
	}
	if !strings.Contains(string(b), "providers:") || !strings.Contains(string(b), "api_key: ${"+operator.SessionKeyEnv+"}") {
		return fmt.Errorf("hermes config must use the keyed providers schema with an env api_key reference:\n%s", b)
	}
	return nil
}

// --- aider-only steps --------------------------------------------------------------------------

func (s *opBDD) noScratchDirForSession() error {
	if s.launch.Dir != "" {
		return fmt.Errorf("aider must create no scratch dir, got %s", s.launch.Dir)
	}
	if got := scratchDirsUnder(s.scratchRoot); len(got) != 0 {
		return fmt.Errorf("scratch dirs exist: %v", got)
	}
	return nil
}

func (s *opBDD) parentEnvSets(key, val string) error {
	s.parentEnv = append([]string{"HOME=/keep/home", "PATH=/keep/path"}, key+"="+val)
	return nil
}

func (s *opBDD) childEnvSets(key, val string) error {
	parent := s.parentEnv
	if parent == nil {
		parent = []string{"HOME=/keep/home"}
	}
	composed := operator.ComposeEnv(parent, s.launch.Env)
	var got []string
	for _, kv := range composed {
		if strings.HasPrefix(kv, key+"=") {
			got = append(got, strings.TrimPrefix(kv, key+"="))
		}
	}
	if len(got) != 1 || got[0] != val {
		return fmt.Errorf("child env %s = %v, want exactly [%s] (override, not merge)", key, got, val)
	}
	return nil
}

// --- isolation steps -----------------------------------------------------------------------------

func (s *opBDD) sentinelSnapshot() error {
	s.homeSnap = treeSnapshot(s.home)
	s.workSnap = treeSnapshot(s.sess.Workdir)
	return nil
}

func (s *opBDD) everyWrittenPathUnderScratch() error {
	if s.launch.Dir != "" && !strings.HasPrefix(s.launch.Dir, s.scratchRoot) {
		return fmt.Errorf("scratch dir %s escaped the root %s", s.launch.Dir, s.scratchRoot)
	}
	if err := s.homeMatchesSentinel(); err != nil {
		return err
	}
	return s.workdirMatchesSentinel()
}

func (s *opBDD) homeMatchesSentinel() error {
	if got := treeSnapshot(s.home); !reflect.DeepEqual(got, s.homeSnap) {
		return fmt.Errorf("the user's home changed: %v", keysOf(got))
	}
	return nil
}

func (s *opBDD) workdirMatchesSentinel() error {
	if got := treeSnapshot(s.sess.Workdir); !reflect.DeepEqual(got, s.workSnap) {
		return fmt.Errorf("the launch workdir changed: %v", keysOf(got))
	}
	return nil
}

func (s *opBDD) scratchDirMode0700() error {
	info, err := os.Stat(s.launch.Dir)
	if err != nil {
		return err
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		return fmt.Errorf("scratch dir mode = %o, want 0700", perm)
	}
	return nil
}

func (s *opBDD) childWorkdirIsLaunchWorkdir() error {
	// TUI scenarios (a plate-accepted exec) verify the REAL child command's cwd is the
	// scenario's confirmed workdir; pure scenarios compose the command directly.
	if s.tm != nil {
		if len(s.execCmds) == 0 {
			s.fireExec()
		}
		if len(s.execCmds) == 0 {
			return fmt.Errorf("no exec was issued (transcript: %s)", s.view())
		}
		last := s.execCmds[len(s.execCmds)-1]
		if last.Dir != s.launchWorkdir {
			return fmt.Errorf("child dir = %q, want the confirmed workdir %q", last.Dir, s.launchWorkdir)
		}
		return nil
	}
	c := operator.Command(s.launch, "/fake/bin", s.sess.Workdir, []string{"HOME=/keep"})
	if c.Dir != s.sess.Workdir {
		return fmt.Errorf("child dir = %q, want the launch workdir %q", c.Dir, s.sess.Workdir)
	}
	return nil
}

func (s *opBDD) childWorkdirNotScratch() error {
	c := operator.Command(s.launch, "/fake/bin", s.sess.Workdir, []string{"HOME=/keep"})
	if s.launch.Dir != "" && c.Dir == s.launch.Dir {
		return fmt.Errorf("child dir must never be the scratch dir")
	}
	return nil
}

func (s *opBDD) noOperatorScratchRemains() error {
	if got := scratchDirsUnder(s.scratchRoot); len(got) != 0 {
		return fmt.Errorf("rogerai-operator scratch dirs remain: %v", got)
	}
	return nil
}

func (s *opBDD) guestRemovedFilesInScratch() error {
	return os.RemoveAll(filepath.Join(s.launch.Dir, "opencode.json"))
}

func (s *opBDD) cleanupStillSucceeds() error {
	if err := s.cleanup(); err != nil {
		return fmt.Errorf("cleanup: %v", err)
	}
	return nil
}

func (s *opBDD) leftoverScratchDirOld(name string) error {
	p := filepath.Join(s.scratchRoot, name)
	if err := os.Mkdir(p, 0o700); err != nil {
		return err
	}
	old := time.Now().Add(-48 * time.Hour)
	return os.Chtimes(p, old, old)
}

func (s *opBDD) freshScratchDir(name string) error {
	return os.Mkdir(filepath.Join(s.scratchRoot, name), 0o700)
}

func (s *opBDD) namedDirRemoved(name string) error {
	if _, err := os.Stat(filepath.Join(s.scratchRoot, name)); !os.IsNotExist(err) {
		return fmt.Errorf("%s should be swept (err=%v)", name, err)
	}
	return nil
}

func (s *opBDD) namedDirUntouched(name string) error {
	if _, err := os.Stat(filepath.Join(s.scratchRoot, name)); err != nil {
		return fmt.Errorf("%s should be untouched: %v", name, err)
	}
	return nil
}

func (s *opBDD) secondScratchDiffers() error {
	if s.prevLaunch.Dir == "" || s.launch.Dir == "" || s.prevLaunch.Dir == s.launch.Dir {
		return fmt.Errorf("scratch dirs must differ: first=%q second=%q", s.prevLaunch.Dir, s.launch.Dir)
	}
	return nil
}

func (s *opBDD) onlySecondScratchExists() error {
	got := scratchDirsUnder(s.scratchRoot)
	if len(got) != 1 || got[0] != s.launch.Dir {
		return fmt.Errorf("scratch dirs = %v, want only %s", got, s.launch.Dir)
	}
	return nil
}

func (s *opBDD) childEnvContainsSessionKey() error {
	for _, kv := range s.launch.Env {
		if strings.HasPrefix(kv, operator.SessionKeyEnv+"=") {
			return nil
		}
	}
	return fmt.Errorf("launch env %v lacks %s", s.launch.Env, operator.SessionKeyEnv)
}

func (s *opBDD) budgetCappedAtDefault() error {
	if client.DefaultSessionBudget != 2.00 {
		return fmt.Errorf("DefaultSessionBudget = %v, want the founder-ruled $2.00", client.DefaultSessionBudget)
	}
	return nil
}

// --- small shared helpers --------------------------------------------------------------------

// treeSnapshot maps every regular file under root (relative path) to its content.
func treeSnapshot(root string) map[string]string {
	out := map[string]string{}
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			rel, _ := filepath.Rel(root, p)
			b, _ := os.ReadFile(p)
			out[rel] = string(b)
		}
		return nil
	})
	return out
}

func keysOf(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func scratchDirsUnder(root string) []string {
	matches, _ := filepath.Glob(filepath.Join(root, "rogerai-operator-*"))
	sort.Strings(matches)
	return matches
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
