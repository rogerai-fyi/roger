package audio

// audio_test.go covers the cross-platform, save-to-file-fallback player extracted from
// internal/tui/voice.go so BOTH the TUI voice preview AND `roger say` reuse ONE impl (no
// duplication). These are the SAME behaviors the TUI proved, moved with the code: the per-OS
// player resolution, the guaranteed darwin/windows built-ins, the linux candidate chain + order,
// and the graceful no-player fallback that saves the wav instead of crashing.

import (
	"os"
	"strings"
	"testing"
)

// With a system player resolved for the OS, Play runs it on the written temp file and reports
// played=true. The player command + args are injectable so this asserts the RIGHT player was
// invoked with the sample, WITHOUT actually playing audio.
func TestPlayRunsSystemPlayer(t *testing.T) {
	var ranName string
	var ranArgs []string
	env := Env{
		GOOS:     "linux",
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil }, // paplay found
		Run:      func(name string, args ...string) error { ranName = name; ranArgs = args; return nil },
	}
	path, played, err := env.Play([]byte("RIFFfake-wav"))
	if err != nil {
		t.Fatalf("Play: %v", err)
	}
	if !played {
		t.Fatal("expected played=true when a player is available")
	}
	if ranName != "paplay" || len(ranArgs) == 0 || ranArgs[len(ranArgs)-1] != path {
		t.Fatalf("player should be invoked on the sample file, ran %q %v (path %q)", ranName, ranArgs, path)
	}
	if _, err := os.Stat(path); err == nil {
		os.Remove(path)
	}
}

// darwin ALWAYS has a built-in player (afplay); windows ALWAYS has one (powershell SoundPlayer).
// Assert both resolve without any LookPath hits (they are guaranteed), and play the sample file.
func TestPlayGuaranteedPlayers(t *testing.T) {
	for _, tc := range []struct {
		goos     string
		wantName string
	}{
		{"darwin", "afplay"},
		{"windows", "powershell"},
	} {
		var ranName string
		var ranArgs []string
		env := Env{
			GOOS:     tc.goos,
			LookPath: func(string) (string, error) { return "", os.ErrNotExist }, // nothing on PATH
			Run:      func(name string, args ...string) error { ranName = name; ranArgs = args; return nil },
		}
		path, played, err := env.Play([]byte("RIFFwav"))
		if err != nil {
			t.Fatalf("%s Play: %v", tc.goos, err)
		}
		if !played {
			t.Fatalf("%s must have a guaranteed built-in player (played=false)", tc.goos)
		}
		if ranName != tc.wantName {
			t.Fatalf("%s should use %q, ran %q", tc.goos, tc.wantName, ranName)
		}
		joined := strings.Join(append([]string{ranName}, ranArgs...), " ")
		if !strings.Contains(joined, path) {
			t.Fatalf("%s invocation must reference the sample file %q, got %q", tc.goos, path, joined)
		}
		os.Remove(path)
	}
}

// With NO player available (linux, nothing on PATH), Play degrades: it writes the sample to a temp
// file and returns played=false + the path (so the caller tells the user where it is) — it must
// never crash.
func TestPlayNoPlayerSavesFile(t *testing.T) {
	env := Env{
		GOOS:     "linux",
		LookPath: func(string) (string, error) { return "", os.ErrNotExist }, // nothing found
		Run:      func(string, ...string) error { t.Fatal("Run must not be called with no player"); return nil },
	}
	path, played, err := env.Play([]byte("RIFFwav-bytes"))
	if err != nil {
		t.Fatalf("Play must not error on the no-player fallback: %v", err)
	}
	if played {
		t.Fatal("expected played=false with no player")
	}
	if path == "" {
		t.Fatal("the fallback must return the saved file path")
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil || string(data) != "RIFFwav-bytes" {
		t.Fatalf("the sample must be written to the fallback path; read=%q err=%v", data, rerr)
	}
	os.Remove(path)
}

// DefaultEnv wires the real OS + exec seams; its Run closure executes a subprocess (a harmless
// no-op command here, so no audio actually plays).
func TestDefaultEnvWiring(t *testing.T) {
	env := DefaultEnv()
	if env.GOOS == "" || env.LookPath == nil || env.Run == nil {
		t.Fatalf("DefaultEnv must wire GOOS + the exec seams, got %+v", env)
	}
	if env.GOOS != "windows" {
		_ = env.Run("true") // no-op; covers the exec.CommandContext closure
	}
}

// SystemPlayer degrades to save-to-file when no player resolves. To keep the test deterministic
// (never spawn a real audio player), drive the SAME code path through an injected Env with no
// player on PATH — asserting the wav is saved and reported, never played.
func TestSystemPlayerFallbackDeterministic(t *testing.T) {
	env := Env{
		GOOS:     "plan9", // no built-in + the linux chain won't match either
		LookPath: func(string) (string, error) { return "", os.ErrNotExist },
		Run:      func(string, ...string) error { t.Fatal("no player should run"); return nil },
	}
	path, played, err := env.Play([]byte("RIFFtiny-wav"))
	if err != nil || played || path == "" {
		t.Fatalf("no-player fallback should save + report the file; path=%q played=%v err=%v", path, played, err)
	}
	os.Remove(path)
}

// ResolvePlayer picks the right command per OS and returns none only when linux/other has nothing
// on PATH. darwin/windows always resolve (built-in); an unknown unix OS falls to the linux chain.
func TestResolvePlayer(t *testing.T) {
	found := func(string) (string, error) { return "/bin/x", nil }
	if name, _ := ResolvePlayer("darwin", found, "/f"); name != "afplay" {
		t.Errorf("darwin should use afplay, got %q", name)
	}
	if name, _ := ResolvePlayer("windows", func(string) (string, error) { return "", os.ErrNotExist }, "/f"); name != "powershell" {
		t.Errorf("windows should use the built-in powershell player, got %q", name)
	}
	if name, _ := ResolvePlayer("linux", found, "/f"); name == "" {
		t.Errorf("linux should find a player when one exists")
	}
	order := []string{}
	pref := func(name string) (string, error) {
		order = append(order, name)
		if name == "aplay" {
			return "/usr/bin/aplay", nil
		}
		return "", os.ErrNotExist
	}
	if name, _ := ResolvePlayer("linux", pref, "/f"); name != "aplay" {
		t.Errorf("linux should fall through to aplay when paplay is absent, got %q (probed %v)", name, order)
	}
	if name, _ := ResolvePlayer("plan9", func(string) (string, error) { return "", os.ErrNotExist }, "/f"); name != "" {
		t.Errorf("an unsupported OS with no player should yield none, got %q", name)
	}
}

// WriteTempWAV writes the bytes to a uniquely-named temp .wav and returns its path.
func TestWriteTempWAV(t *testing.T) {
	path, err := WriteTempWAV([]byte("RIFFxyz"))
	if err != nil {
		t.Fatalf("WriteTempWAV: %v", err)
	}
	defer os.Remove(path)
	if !strings.HasSuffix(path, ".wav") {
		t.Errorf("temp file should end in .wav, got %q", path)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "RIFFxyz" {
		t.Errorf("temp wav = %q, want the written bytes", data)
	}
}

// When the temp dir can't be written (a bogus TMPDIR), WriteTempWAV surfaces the CreateTemp error
// instead of a path — and Play propagates that same error rather than trying to run a player on a
// non-existent file. Pointing TMPDIR at a path under a regular FILE guarantees CreateTemp fails
// (you cannot create a child of a file) deterministically on every OS.
func TestWriteTempWAVCreateError(t *testing.T) {
	notADir := t.TempDir() + "/afile"
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", notADir+"/nope") // CreateTemp roots at $TMPDIR; a child of a file cannot exist
	if _, err := WriteTempWAV([]byte("RIFF")); err == nil {
		t.Fatal("WriteTempWAV must error when the temp dir is unwritable")
	}
	// Play must propagate the write failure (no path, not played) — it never runs a player on a
	// file that could not be written.
	env := Env{
		GOOS:     "linux",
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		Run: func(string, ...string) error {
			t.Fatal("Run must not be called when the temp write failed")
			return nil
		},
	}
	if path, played, err := env.Play([]byte("RIFF")); err == nil || played || path != "" {
		t.Fatalf("Play must propagate the temp-write error; path=%q played=%v err=%v", path, played, err)
	}
}

// SystemPlayer is the exported real player used by both the TUI and `roger say`; here we only assert
// it returns without panicking on tiny bytes (it resolves the host player or falls back to save —
// on a CI runner with no audio device it saves; either way no crash).
func TestSystemPlayerSmoke(t *testing.T) {
	path, _, err := SystemPlayer([]byte("RIFFsmoke"))
	if err != nil {
		// A player may fail to actually emit audio on a headless runner; that's fine — the contract
		// is "no crash + a path to fall back on", which the fallback covers. Only a nil path with an
		// error is a real problem.
		if path == "" {
			t.Fatalf("SystemPlayer errored with no fallback path: %v", err)
		}
	}
	if path != "" {
		os.Remove(path)
	}
}
