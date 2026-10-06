package main

// Executable spec: features/lan/launch_and_config.feature (@cli). The real run() path with an
// isolated config: the observable is the mode run() decided, read at the dispatch seam, plus
// `roger status`, the config file and the one-line note on stderr.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/lanmode"
)

type lanLaunchBDD struct {
	t        *testing.T
	out      string
	note     bytes.Buffer
	err      error
	decided  *lanmode.Mode
	sawTUI   bool
	dialed   bool
	statusLn string
}

func (s *lanLaunchBDD) reset() {
	useTempConfig(s.t)
	s.t.Setenv("ROGER_BROKER", "http://127.0.0.1:1")
	os.Unsetenv("ROGER_LAN")
	s.out, s.err, s.decided, s.sawTUI, s.dialed, s.statusLn = "", nil, nil, false, false, ""
	s.note.Reset()
	lanNoteOut = &s.note
	runDispatch = func(cfg config, args []string) error {
		m := cfg.lan
		s.decided = &m
		switch args[0] {
		case "status", "config", "lan":
			return dispatch(cfg, args) // the real surfaces this file pins
		}
		s.dialed = true // any other command would have dialed; the runner stops here
		return nil
	}
	launchTUIForTest = func(cfg config) error {
		m := cfg.lan
		s.decided, s.sawTUI = &m, true
		return nil
	}
}

func (s *lanLaunchBDD) restore() {
	runDispatch, launchTUIForTest, lanNoteOut = dispatch, nil, os.Stderr
}

func (s *lanLaunchBDD) run(line string) error {
	args := strings.Fields(line)[1:]
	s.decided, s.dialed = nil, false
	out, err := captureEdgeStdout(func() error { return run(args, loadConfig()) })
	s.out, s.err = out, err
	return nil
}

func (s *lanLaunchBDD) setEnv(v string) error { s.t.Setenv("ROGER_LAN", v); return nil }

func (s *lanLaunchBDD) noModeNoEnv() error { return nil }

func (s *lanLaunchBDD) configMode(v string) error {
	if v == "unset" {
		return nil
	}
	c := loadConfig()
	c.Mode = v
	return saveConfig(c)
}

func (s *lanLaunchBDD) envIs(v string) error {
	if v == "unset" {
		os.Unsetenv("ROGER_LAN")
		return nil
	}
	s.t.Setenv("ROGER_LAN", v)
	return nil
}

func (s *lanLaunchBDD) runsIn(want string) error {
	if s.err != nil {
		return fmt.Errorf("the command failed: %v", s.err)
	}
	if s.decided == nil {
		return fmt.Errorf("run() never reached dispatch")
	}
	got := "default"
	if s.decided.LAN {
		got = "lan"
	}
	if got != want {
		return fmt.Errorf("ran in %q mode, want %q", got, want)
	}
	return nil
}

func (s *lanLaunchBDD) runsLAN() error     { return s.runsIn("lan") }
func (s *lanLaunchBDD) runsDefault() error { return s.runsIn("default") }

func (s *lanLaunchBDD) nextRunsDefault(line string) error {
	if err := s.run(line); err != nil {
		return err
	}
	return s.runsDefault()
}

func (s *lanLaunchBDD) tuiOpens() error {
	if !s.sawTUI {
		return fmt.Errorf("the TUI did not open")
	}
	return nil
}

func (s *lanLaunchBDD) tuiBadge() error {
	if s.decided == nil || !s.decided.LAN {
		return fmt.Errorf("the TUI was opened outside LAN mode, so it cannot carry the LAN badge")
	}
	return nil // the badge itself is pinned by ui_visibility.feature (@tui)
}

func (s *lanLaunchBDD) exitsNonZero() error {
	if exitCode(s.err) == 0 {
		return fmt.Errorf("exited 0, output:\n%s", s.out)
	}
	return nil
}

func (s *lanLaunchBDD) errorNames(a, b string) error {
	if s.err == nil || !strings.Contains(s.err.Error(), a) || !strings.Contains(s.err.Error(), b) {
		return fmt.Errorf("error %v does not name %q and %q", s.err, a, b)
	}
	return nil
}

func (s *lanLaunchBDD) envErrorNames() error {
	return s.errorNames("ROGER_LAN", "true")
}

func (s *lanLaunchBDD) nothingDialed() error {
	if s.dialed {
		return fmt.Errorf("the command reached dispatch")
	}
	return nil
}

func (s *lanLaunchBDD) configRecords(v string) error {
	raw := readRawConfig(configPath())
	var got string
	_ = json.Unmarshal(raw["mode"], &got)
	if got != v {
		return fmt.Errorf("config mode = %q, want %q", got, v)
	}
	return nil
}

func (s *lanLaunchBDD) everyLaterLAN() error {
	if err := s.run("roger use qwen3-8b"); err != nil {
		return err
	}
	return s.runsLAN()
}

func (s *lanLaunchBDD) exitsNamingValues() error {
	if err := s.exitsNonZero(); err != nil {
		return err
	}
	return s.errorNames("lan", "default")
}

func (s *lanLaunchBDD) noModeSet() error {
	if _, ok := readRawConfig(configPath())["mode"]; ok {
		return fmt.Errorf("config still has a mode: %s", readRawConfig(configPath())["mode"])
	}
	return nil
}

func (s *lanLaunchBDD) precedence(result, source string) error {
	if err := s.runsIn(result); err != nil {
		return err
	}
	if string(s.decided.Source) != source {
		return fmt.Errorf("source = %q, want %q", s.decided.Source, source)
	}
	return nil
}

func (s *lanLaunchBDD) statusReports(result, source string) error {
	flag := ""
	if s.decided != nil && s.decided.Source == lanmode.SrcFlag {
		if s.decided.LAN {
			flag = " --lan"
		} else {
			flag = " --no-lan"
		}
	}
	if err := s.run("roger status" + flag); err != nil {
		return err
	}
	want := "mode: default (from " + source + ")"
	if result == "lan" {
		want = "mode: LAN (from " + source + ")"
	}
	if !strings.Contains(s.out, want) {
		return fmt.Errorf("status lacks %q:\n%s", want, s.out)
	}
	return nil
}

func (s *lanLaunchBDD) conflictError() error {
	if err := s.exitsNonZero(); err != nil {
		return err
	}
	return s.errorNames("--lan", "--no-lan")
}

func (s *lanLaunchBDD) oldReaderRewrites() error {
	delete(knownConfigKeys, "mode")
	defer func() { knownConfigKeys["mode"] = true }()
	c := loadConfig()
	c.Mode = "" // an older struct has no such field, so it marshals nothing for it
	c.Palette = "mono"
	return saveConfig(c)
}

func (s *lanLaunchBDD) modeKeyPresent() error { return s.configRecords("lan") }

func (s *lanLaunchBDD) statusLine(line string) error {
	if !strings.Contains(s.out, line) {
		return fmt.Errorf("no line %q in:\n%s", line, s.out)
	}
	return nil
}

func (s *lanLaunchBDD) listsSources() error {
	for _, w := range []string{"this machine", "Edge members", "Towers"} {
		if !strings.Contains(s.out, w) {
			return fmt.Errorf("status does not list %q:\n%s", w, s.out)
		}
	}
	return nil
}

func (s *lanLaunchBDD) statusJSON(mode, source string) error {
	var m map[string]any
	if err := json.Unmarshal([]byte(s.out), &m); err != nil {
		return fmt.Errorf("not JSON: %v\n%s", err, s.out)
	}
	if m["mode"] != mode || m["mode_source"] != source {
		return fmt.Errorf("mode=%v source=%v, want %s from %s", m["mode"], m["mode_source"], mode, source)
	}
	return nil
}

func (s *lanLaunchBDD) firstLineNames() error {
	n := s.note.String()
	if !strings.Contains(n, "LAN mode") || !strings.Contains(n, "roger config unset mode") {
		return fmt.Errorf("the note does not name LAN mode and how to turn it off: %q", n)
	}
	if strings.Count(n, "\n") != 1 {
		return fmt.Errorf("the note is not one line: %q", n)
	}
	return nil
}

func (s *lanLaunchBDD) piped() error {
	s.note.Reset()
	return s.run("roger use qwen3-8b")
}

func (s *lanLaunchBDD) noteOnStderrOnly() error {
	if strings.Contains(s.out, "LAN mode") {
		return fmt.Errorf("the note reached stdout: %q", s.out)
	}
	return s.firstLineNames()
}

func TestLANLaunchBDD(t *testing.T) {
	s := &lanLaunchBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.reset(); return ctx, nil })
			sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				s.restore()
				return ctx, nil
			})
			sc.Step(`^a machine with no mode set in config and no ROGER_LAN in the environment$`, s.noModeNoEnv)
			sc.Step(`^they run "([^"]*)"$`, s.run)
			sc.Step(`^they run "([^"]*)" in a terminal$`, s.run)
			sc.Step(`^the command runs in LAN mode$`, s.runsLAN)
			sc.Step(`^the command runs in the default mode$`, s.runsDefault)
			sc.Step(`^the next "([^"]*)" without the flag runs in the default mode$`, s.nextRunsDefault)
			sc.Step(`^the TUI opens$`, s.tuiOpens)
			sc.Step(`^its header carries the LAN badge$`, s.tuiBadge)
			sc.Step(`^ROGER_LAN is set to "([^"]*)"$`, s.setEnv)
			sc.Step(`^the config has mode "([^"]*)"$`, s.configMode)
			sc.Step(`^it exits non-zero$`, s.exitsNonZero)
			sc.Step(`^the error names ROGER_LAN and the accepted values$`, s.envErrorNames)
			sc.Step(`^nothing was dialed$`, s.nothingDialed)
			sc.Step(`^the config file records mode "([^"]*)"$`, s.configRecords)
			sc.Step(`^every later command runs in LAN mode until it is changed$`, s.everyLaterLAN)
			sc.Step(`^the config has no mode set$`, s.noModeSet)
			sc.Step(`^it exits non-zero naming the accepted values$`, s.exitsNamingValues)
			sc.Step(`^the config still has no mode set$`, s.noModeSet)
			sc.Step(`^ROGER_LAN is "([^"]*)"$`, s.envIs)
			sc.Step(`^the command runs in "([^"]*)" mode$`, s.runsIn)
			sc.Step(`^`+"`roger status`"+` in that same environment and flags reports "([^"]*)" from "([^"]*)"$`, s.statusReports)
			sc.Step(`^it exits non-zero naming the conflict$`, s.conflictError)
			sc.Step(`^the next command runs in the default mode$`, func() error {
				if err := s.run("roger use qwen3-8b"); err != nil {
					return err
				}
				return s.runsDefault()
			})
			sc.Step(`^a config written by this version with mode "([^"]*)"$`, s.configMode)
			sc.Step(`^a field-preserving older-version reader rewrites the config$`, s.oldReaderRewrites)
			sc.Step(`^the mode key is still present$`, s.modeKeyPresent)
			sc.Step(`^the output has a line reading "([^"]*)"$`, s.statusLine)
			sc.Step(`^it lists the local sources it will use: Towers, Edge members, this machine$`, s.listsSources)
			sc.Step(`^the JSON has "mode" equal to "([^"]*)" and "mode_source" equal to "([^"]*)"$`, s.statusJSON)
			sc.Step(`^the first line printed names LAN mode and how to turn it off$`, s.firstLineNames)
			sc.Step(`^the output is piped$`, s.piped)
			sc.Step(`^that note goes to stderr and stdout carries only the command's output$`, s.noteOnStderrOnly)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@cli",
			Paths: []string{"../../features/lan/launch_and_config.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("launch_and_config.feature failed")
	}
}
