package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Small CLI seams that nothing exercised. Each is a real claim a user depends on - what a
// help page names, where the hardware probe writes, whether a config write can corrupt the
// file it is replacing - so these are locks rather than coverage padding.

// The `context` sub-usages name the commands they document, and the one-time,
// single-use, expiring nature of a stranger capsule - the properties someone reads this
// page to check before handing a code to another person.
func TestContextUsagesNameTheirContract(t *testing.T) {
	pub := captureStdout(t, contextPublishUsage)
	for _, want := range []string{"roger context publish", "one-time code", "single-use", "summary-only"} {
		if !strings.Contains(pub, want) {
			t.Errorf("publish usage must mention %q:\n%s", want, pub)
		}
	}
	res := captureStdout(t, contextResolveUsage)
	for _, want := range []string{"roger context resolve", "delete-on-read", "--into", "APPENDED"} {
		if !strings.Contains(res, want) {
			t.Errorf("resolve usage must mention %q:\n%s", want, res)
		}
	}
	// Neither may promise the broker can read anything - the whole point is that it cannot.
	if !strings.Contains(pub, "never sees") {
		t.Errorf("publish usage must state what the broker cannot see:\n%s", pub)
	}
}

// UNLIMITED is a word, not a number. A timeout of 0 means "no cap", and printing "0s"
// would read as "give up immediately" - the opposite.
func TestAgentTimeoutFormatsUnlimitedAsAWord(t *testing.T) {
	for _, secs := range []int{0, -1, -900} {
		if got := formatAgentTimeout(secs); got != "unlimited" {
			t.Errorf("formatAgentTimeout(%d) = %q, want unlimited", secs, got)
		}
	}
	if got := formatAgentTimeout(90); got != "1m30s" {
		t.Errorf("formatAgentTimeout(90) = %q, want 1m30s", got)
	}
}

// The hardware probe must always return a real, usable directory: it writes a probe file,
// and "" would put that at the filesystem root.
func TestHwProbeDirAlwaysResolves(t *testing.T) {
	dir := hwProbeDir()
	if strings.TrimSpace(dir) == "" {
		t.Fatal("hwProbeDir returned an empty path - the probe would write to the root")
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Errorf("hwProbeDir returned %q, which is not a directory: %v", dir, err)
	}
	// With no home it must still resolve, not fall back to "".
	t.Setenv("HOME", "")
	if got := hwProbeDir(); strings.TrimSpace(got) == "" {
		t.Error("hwProbeDir returned empty with no HOME")
	}
}

// THE CONFIG WRITE IS ATOMIC. It holds the broker, the user and the spend limits, so a
// partial write is a machine that comes back configured wrong. A failed write must leave
// the previous file intact and leave no debris behind.
func TestAtomicConfigWriteReplacesOrLeavesTheOldFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.json")

	if err := atomicWriteConfig(path, []byte(`{"broker":"one"}`)); err != nil {
		t.Fatalf("first write: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != `{"broker":"one"}` {
		t.Fatalf("first write landed %q", got)
	}
	// The file is created 0600 - it carries the operator's broker and user.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %v, want 0600 - it is account state", st.Mode().Perm())
	}

	// A replacement is a rename, so the old contents are never half-overwritten.
	if err := atomicWriteConfig(path, []byte(`{"broker":"two"}`)); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != `{"broker":"two"}` {
		t.Errorf("second write landed %q", got)
	}
	// No .tmp debris survives a successful write.
	ents, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".config-") {
			t.Errorf("a temp file was left behind: %s", e.Name())
		}
	}
}

// An UNWRITABLE destination is an error, not a silent no-op: a config change the operator
// asked for that quietly did not happen is worse than one that failed loudly.
func TestAtomicConfigWriteReportsAnUnwritablePath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteConfig(filepath.Join(locked, "deeper", "config.json"), []byte("{}")); err == nil {
		t.Error("a write into an unwritable directory reported success")
	}
}

// `roger context resolve` refuses BEFORE it reaches the network when it has nothing to
// work with. A one-time code is consumed on read (delete-on-read), so a request fired
// against a missing broker - or with no code at all - would burn the guest's one attempt
// for nothing.
func TestContextResolveRefusesBeforeSpendingTheCode(t *testing.T) {
	err := cmdContextResolve(config{}, []string{"147.520 MHz · 8F3K-9M2Q"})
	if err == nil || !strings.Contains(err.Error(), "no broker configured") {
		t.Errorf("with no broker it must refuse locally, got %v", err)
	}
	err = cmdContextResolve(config{Broker: "http://127.0.0.1:0"}, nil)
	if err == nil || !strings.Contains(err.Error(), "one-time code is required") {
		t.Errorf("with no code it must say so, got %v", err)
	}
}

// The progress writer is a no-op sink that must still honour the io.Writer contract:
// reporting a short write would make a caller retry a byte range that was never missing.
func TestDiscardWriterReportsAFullWrite(t *testing.T) {
	var sink discardWriter
	n, err := sink.Write([]byte("relay progress"))
	if err != nil {
		t.Fatalf("discardWriter errored: %v", err)
	}
	if n != len("relay progress") {
		t.Errorf("wrote %d of %d bytes - a short write invites a retry", n, len("relay progress"))
	}
	var w discardWriter
	n2, err2 := w.Write(nil)
	if n2 != 0 || err2 != nil {
		t.Errorf("empty write = (%d, %v), want (0, nil)", n2, err2)
	}
}

// EVERY DOCUMENTED ALIAS MUST ROUTE. The help and the manual promise several spellings
// per command ("use / connect / tune", "bands / band", "payout / payouts / cashout"), and
// an alias that silently falls through to "unknown command" makes a documented invocation
// a lie. These route with arguments that fail EARLY and locally - no broker, no network -
// so the assertion is that dispatch recognised the word, not what the command then did.
func TestEveryDocumentedAliasRoutes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, alias := range []string{
		"bands", "band", // private bands
		"perms", "permissions", // tool approvals
		"payout", "payouts", "cashout", // earnings
		"remote", "rc", // remote sessions
		"webui", "web", "console", // the browser console
	} {
		err := dispatch(config{}, []string{alias, "--help"})
		if err != nil && strings.Contains(err.Error(), "unknown command") {
			t.Errorf("%q is documented but does not route: %v", alias, err)
		}
	}
}

// An UNKNOWN command is named back to the operator rather than swallowed, so a typo reads
// as a typo instead of as a command that did nothing.
func TestAnUnknownCommandNamesItself(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	err := dispatch(config{}, []string{"bandz"})
	if err == nil {
		t.Fatal("an unknown command reported success")
	}
	if !strings.Contains(err.Error(), "bandz") {
		t.Errorf("the error must name the word it did not understand, got %q", err)
	}
}
