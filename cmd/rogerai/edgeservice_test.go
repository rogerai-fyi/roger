package main

// features/edge/headless.feature (approved 2026-10-03): a member with no screen stays on its Edge,
// and comes back after a reboot.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type serviceRig struct {
	unit  string
	calls []string
	euid  int
	sysd  bool
	shown map[string]string // systemctl output by first argument
}

func newServiceRig(t *testing.T) *serviceRig {
	t.Helper()
	r := &serviceRig{unit: filepath.Join(t.TempDir(), "roger-edge.service"), euid: 1000, sysd: true, shown: map[string]string{}}
	oldUnit, oldRun, oldEUID, oldSysd, oldExe := edgeServiceUnitPath, edgeServiceRun, edgeServiceEUID, edgeServiceSystemd, edgeServiceExe
	t.Cleanup(func() {
		edgeServiceUnitPath, edgeServiceRun, edgeServiceEUID, edgeServiceSystemd, edgeServiceExe = oldUnit, oldRun, oldEUID, oldSysd, oldExe
	})
	edgeServiceUnitPath = r.unit
	edgeServiceRun = func(name string, args ...string) (string, error) {
		r.calls = append(r.calls, name+" "+strings.Join(args, " "))
		if len(args) > 0 {
			return r.shown[args[0]], nil
		}
		return "", nil
	}
	edgeServiceEUID = func() int { return r.euid }
	edgeServiceSystemd = func() bool { return r.sysd }
	edgeServiceExe = func() (string, error) { return "/usr/local/bin/roger", nil }
	return r
}

func TestAMachineThatIsNotEnrolledIsToldToEnrollFirst(t *testing.T) {
	offline(t)
	newServiceRig(t)
	out, code := edgeRun(t, "edge", "run")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, "roger edge enroll")
	out, code = edgeRun(t, "edge", "service", "install")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, "roger edge enroll", "a service for a machine on no Edge would only fail at boot")
}

func TestTheServiceIsShownAskedAndNeverEscalated(t *testing.T) {
	newFacedAuthority(t, "pi")
	r := newServiceRig(t)

	// A No writes nothing.
	edgeStdin = strings.NewReader("n\n")
	out, code := edgeRun(t, "edge", "service", "install")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "ExecStart=/usr/local/bin/roger edge run", "what runs is shown before anything is written")
	require.Contains(t, out, "Restart=on-failure")
	require.Contains(t, out, "WantedBy=multi-user.target", "it starts at boot")
	require.Contains(t, out, "nothing was written")
	_, err := os.Stat(r.unit)
	require.True(t, os.IsNotExist(err))

	// A yes, as an ordinary user: the unit is written beside this machine's config and the exact sudo
	// commands are printed - roger never runs sudo itself.
	edgeStdin = strings.NewReader("y\n")
	out, code = edgeRun(t, "edge", "service", "install")
	require.Equal(t, 0, code, out)
	staged := filepath.Join(filepath.Dir(configPath()), "roger-edge.service")
	b, err := os.ReadFile(staged)
	require.NoError(t, err)
	require.Contains(t, string(b), "ExecStart=/usr/local/bin/roger edge run")
	require.Contains(t, string(b), "After=network-online.target")
	require.Contains(t, out, "sudo install -m 0644 "+staged+" "+r.unit)
	require.Contains(t, out, "sudo systemctl daemon-reload")
	require.Contains(t, out, "sudo systemctl enable --now roger-edge")
	for _, c := range r.calls {
		require.NotContains(t, c, "sudo", "never runs sudo itself")
		require.NotContains(t, c, "enable", "and does not try what it may not do")
	}

	// As root (already privileged, not raised by roger): it installs and enables after the yes.
	r.euid, r.calls = 0, nil
	edgeStdin = strings.NewReader("y\n")
	out, code = edgeRun(t, "edge", "service", "install")
	require.Equal(t, 0, code, out)
	b, err = os.ReadFile(r.unit)
	require.NoError(t, err)
	require.Contains(t, string(b), "ExecStart=/usr/local/bin/roger edge run")
	require.Equal(t, []string{"systemctl daemon-reload", "systemctl enable --now roger-edge"}, r.calls)
	require.Contains(t, out, "roger now starts at boot")
}

func TestTheServiceIsSaidAndRemovable(t *testing.T) {
	newFacedAuthority(t, "pi")
	r := newServiceRig(t)
	r.shown["is-enabled"] = "enabled\n"
	r.shown["show"] = "ActiveState=active\nActiveEnterTimestamp=Fri 2026-10-03 08:00:00 UTC\n"
	out, code := edgeRun(t, "edge", "service")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "starts at boot: yes")
	require.Contains(t, out, "running: yes, since Fri 2026-10-03 08:00:00 UTC")

	r.shown["is-enabled"] = "disabled\n"
	r.shown["show"] = "ActiveState=inactive\nActiveEnterTimestamp=\n"
	out, _ = edgeRun(t, "edge", "service")
	require.Contains(t, out, "starts at boot: no")
	require.Contains(t, out, "running: no")
	require.Contains(t, out, "roger edge service install")

	edgeStdin = strings.NewReader("n\n")
	out, _ = edgeRun(t, "edge", "service", "remove")
	require.Contains(t, out, "left as it is")
	edgeStdin = strings.NewReader("y\n")
	out, code = edgeRun(t, "edge", "service", "remove")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "sudo systemctl disable --now roger-edge")
	require.Contains(t, out, "sudo rm "+r.unit)
}

func TestAPlatformWithoutSystemdSaysSo(t *testing.T) {
	newFacedAuthority(t, "pi")
	r := newServiceRig(t)
	r.sysd = false
	out, code := edgeRun(t, "edge", "service", "install")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "starting at boot is not built for this platform yet")
	require.Contains(t, out, "roger edge run")
	require.Empty(t, r.calls)
}

// A roger kept in a non-default place (XDG_CONFIG_HOME) is started by the service against that same
// place, or it would boot as a machine on no Edge.
func TestTheServiceRunsAgainstTheSameConfig(t *testing.T) {
	unit, err := edgeServiceUnitFor("/usr/local/bin/roger", "pi", "/srv/roger-config")
	require.NoError(t, err)
	require.Contains(t, unit, "Environment=XDG_CONFIG_HOME=/srv/roger-config\n")
	unit, err = edgeServiceUnitFor("/usr/local/bin/roger", "pi", "")
	require.NoError(t, err)
	require.NotContains(t, unit, "XDG_CONFIG_HOME", "the default needs nothing")
}

// Pre-push audit (2026-10-26): an empty User= is root to systemd, and a value with a newline in it
// writes lines of its own into the unit. The unit is refused rather than written.
func TestAUnitThatWouldNotSayWhatItMeansIsRefused(t *testing.T) {
	for name, c := range map[string][3]string{
		"no user":              {"/usr/local/bin/roger", "", ""},
		"a user with a line":   {"/usr/local/bin/roger", "pi\nExecStartPre=/bin/sh", ""},
		"a spaced user":        {"/usr/local/bin/roger", "p i", ""},
		"a path with a line":   {"/usr/local/bin/roger\nUser=root", "pi", ""},
		"a config with a line": {"/usr/local/bin/roger", "pi", "/srv\nUser=root"},
	} {
		_, err := edgeServiceUnitFor(c[0], c[1], c[2])
		require.Error(t, err, name)
	}
}

// Pre-push audit (2026-10-26): under sudo, roger read root's config and said "not on an Edge yet",
// while the unit named the sudo caller as its user. The service's user is the one whose config was
// read; under sudo, roger says to run it as that user instead.
func TestUnderSudoTheServiceSaysToRunWithoutIt(t *testing.T) {
	offline(t)
	r := newServiceRig(t)
	r.euid = 0
	t.Setenv("SUDO_USER", "pi")
	out, code := edgeRun(t, "edge", "service", "install")
	require.NotEqual(t, 0, code)
	require.Contains(t, out, "without sudo")
	require.Contains(t, out, "as pi")
}
