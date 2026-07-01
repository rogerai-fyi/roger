// Package audio is the shared, cross-platform, save-to-file-fallback WAV player. It is the ONE
// implementation used by BOTH the TUI voice preview (internal/tui) AND the one-shot `roger say`
// CLI command (cmd/rogerai) — extracted here so neither duplicates the per-OS resolution + the
// graceful no-player fallback.
//
// SHELL-OUT ONLY (no in-process oto/beep): roger ships CGO_ENABLED=0 static across linux/darwin/
// windows × amd64/arm64, so an in-process audio lib would break the cross-compiled release build.
// WAV (not mp3) is the interchange format because it is universally + trivially playable with no
// lame/ffmpeg: darwin (afplay) and windows (.NET SoundPlayer via powershell) both play it built-in,
// guaranteed; linux tries a small candidate chain and, failing that, saves the file so the caller
// can point the user at it (it NEVER crashes and, via the run timeout, NEVER blocks indefinitely).
package audio

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// PlayerFn plays a WAV sample and reports the fallback save path (when it could not play, so the
// caller can tell the user where the file is), whether it played, and any error. This is the seam
// both surfaces inject in tests (a stub records the bytes / returns a path) so no real audio device
// is needed.
type PlayerFn func(wav []byte) (savedPath string, played bool, err error)

// PlayTimeout bounds a playback so a wedged player can never block the caller indefinitely (a few
// seconds of speech + slack). On timeout the sample is already on disk (the user can replay it).
const PlayTimeout = 20 * time.Second

// Env is the runtime environment for the real player, with the OS + exec seams injectable so the
// per-OS resolution + fallback are unit-testable without spawning a process.
type Env struct {
	GOOS     string
	LookPath func(string) (string, error)            // exec.LookPath
	Run      func(name string, args ...string) error // start + wait (bounded)
}

// SystemPlayer is the real player: it resolves a CLI audio player for the host OS and plays the
// sample, falling back to saving the wav when none exists. This is the default PlayerFn both
// surfaces wire when not stubbed.
func SystemPlayer(wav []byte) (string, bool, error) {
	return DefaultEnv().Play(wav)
}

// DefaultEnv wires the real OS + exec seams (runtime.GOOS, exec.LookPath, a bounded
// exec.CommandContext).
func DefaultEnv() Env {
	return Env{
		GOOS:     runtime.GOOS,
		LookPath: exec.LookPath,
		Run: func(name string, args ...string) error {
			ctx, cancel := context.WithTimeout(context.Background(), PlayTimeout)
			defer cancel()
			return exec.CommandContext(ctx, name, args...).Run()
		},
	}
}

// Play writes the sample to a temp .wav and runs the resolved system player on it. With NO player
// available (only possible on linux/other) it degrades gracefully: the file is left on disk and
// (path, played=false) is returned so the caller surfaces the path. On a player error the path is
// still returned (the sample is on disk to retry).
func (e Env) Play(wav []byte) (string, bool, error) {
	path, err := WriteTempWAV(wav)
	if err != nil {
		return "", false, err
	}
	name, args := ResolvePlayer(e.GOOS, e.LookPath, path)
	if name == "" {
		return path, false, nil // no player: saved for the user, no crash
	}
	if err := e.Run(name, args...); err != nil {
		return path, false, err
	}
	return path, true, nil
}

// ResolvePlayer returns the player command + full args to play `file` on goos, or ("",nil) when
// linux/other has NOTHING on PATH (-> the save-to-file fallback). darwin + windows always resolve
// to a GUARANTEED built-in player (afplay / .NET SoundPlayer via powershell), so they never hit the
// fallback. lookPath is injected so the linux chain is testable without a real PATH.
func ResolvePlayer(goos string, lookPath func(string) (string, error), file string) (string, []string) {
	switch goos {
	case "darwin":
		// afplay ships with macOS — always present, plays wav natively.
		return "afplay", []string{file}
	case "windows":
		// The built-in .NET SoundPlayer plays wav SYNCHRONOUSLY (blocks until done, no duration
		// math, no external deps) — always present on Windows. Args are split (never a raw string).
		ps := fmt.Sprintf("(New-Object System.Media.SoundPlayer '%s').PlaySync()", file)
		return "powershell", []string{"-NoProfile", "-Command", ps}
	default:
		// linux (and any other unix): first on PATH wins, then degrade.
		for _, p := range linuxPlayers {
			if _, err := lookPath(p.cmd); err == nil {
				return p.cmd, append(append([]string{}, p.flags...), file)
			}
		}
		return "", nil
	}
}

// linuxPlayers is the ordered candidate chain for linux/other (first available wins), with each
// player's quiet / no-video / auto-exit flags so playback runs once and returns. paplay/aplay/play
// are common and play wav directly; mpv/ffplay are heavier but ubiquitous fallbacks.
var linuxPlayers = []struct {
	cmd   string
	flags []string
}{
	{"paplay", nil},
	{"aplay", []string{"-q"}},
	{"play", []string{"-q"}}, // sox
	{"mpv", []string{"--no-video", "--really-quiet"}},
	{"ffplay", []string{"-nodisp", "-autoexit", "-loglevel", "quiet"}},
}

// WriteTempWAV writes the sample bytes to a uniquely-named temp .wav and returns its path.
func WriteTempWAV(wav []byte) (string, error) {
	f, err := os.CreateTemp("", "rogerai-voice-*.wav")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(wav); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
}
