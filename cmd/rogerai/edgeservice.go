package main

// A MEMBER WITH NO SCREEN (features/edge/headless.feature, approved 2026-10-03): `roger edge run`
// keeps this machine on its Edge headless, in the open; `roger edge service` makes it start at boot
// through systemd. roger shows the service before writing it, asks, and never raises its own
// privileges: as an ordinary user it stages the unit and prints the exact sudo commands.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

const edgeServiceName = "roger-edge"

// The seams a test replaces: where the unit goes, how systemctl is run, who we are, whether systemd
// runs this machine, and where this roger is.
var (
	edgeServiceUnitPath = "/etc/systemd/system/" + edgeServiceName + ".service"
	edgeServiceRun      = func(name string, args ...string) (string, error) {
		out, err := exec.Command(name, args...).CombinedOutput()
		return string(out), err
	}
	edgeServiceEUID    = os.Geteuid
	edgeServiceSystemd = func() bool {
		fi, err := os.Stat("/run/systemd/system")
		return err == nil && fi.IsDir()
	}
	edgeServiceExe = func() (string, error) {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(exe)
	}
)

// edgeNotEnrolledErr is the refusal for a machine on no Edge, naming how to join one.
func edgeNotEnrolledErr() error {
	return errors.New("this machine is not on an Edge yet - join one first: " +
		"`roger edge enroll <name> --authority http://<authority address>` (or `roger edge setup`)")
}

func edgeEnrolledHere() bool {
	_, _, enrolled, err := edgeIdentityStore().LoadIdentity()
	return err == nil && enrolled
}

// cmdEdgeRun keeps this machine on its Edge with no screen, until it is told to stop.
func cmdEdgeRun(cfg config, args []string) error {
	leaf, _ := edgeLeaf("run")
	if _, err := parseEdgeArgv(leaf, args); err != nil {
		return err
	}
	if !edgeEnrolledHere() {
		return edgeNotEnrolledErr()
	}
	return runEdgeAgent(cfg)
}

// cmdEdgeService shows, installs or removes the boot service.
func cmdEdgeService(cfg config, args []string) error {
	leaf, _ := edgeLeaf("service")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	verb := ""
	if len(argv.pos) > 0 {
		verb = argv.pos[0]
	}
	if !edgeServiceSystemd() {
		fmt.Println("starting at boot is not built for this platform yet (roger knows systemd, and this machine does not run it).")
		fmt.Println("  `roger edge run` still keeps this machine on its Edge with no screen, for as long as it runs.")
		return nil
	}
	switch verb {
	case "":
		return edgeServiceStatus()
	case "install":
		return edgeServiceInstall()
	case "remove":
		return edgeServiceRemove()
	}
	return usagef("usage: roger edge service [install | remove]")
}

// edgeServiceUser is who the service runs as: the owner whose config holds this machine's Edge, even
// when the command itself was run with sudo.
func edgeServiceUser() string {
	if u := os.Getenv("SUDO_USER"); u != "" && edgeServiceEUID() == 0 {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return os.Getenv("USER")
}

// edgeServiceUnit is the unit roger installs, against this run's config location.
func edgeServiceUnit(exe, who string) string {
	return edgeServiceUnitFor(exe, who, os.Getenv("XDG_CONFIG_HOME"))
}

// edgeServiceUnitFor is the unit for one config location: a non-default one is carried, or the
// service would boot roger as a machine on no Edge.
func edgeServiceUnitFor(exe, who, xdg string) string {
	env := ""
	if xdg != "" {
		env = "Environment=XDG_CONFIG_HOME=" + xdg + "\n"
	}
	return fmt.Sprintf(`[Unit]
Description=roger - keeps this machine on its Edge
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=%s
%sExecStart=%s edge run
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, who, env, exe)
}

func edgeServiceInstall() error {
	if !edgeEnrolledHere() {
		return fmt.Errorf("%w; a service for a machine on no Edge would only fail at every boot", edgeNotEnrolledErr())
	}
	exe, err := edgeServiceExe()
	if err != nil {
		return err
	}
	unit := edgeServiceUnit(exe, edgeServiceUser())
	fmt.Printf("roger will install this as %s - it starts roger at boot and restarts it if it stops:\n\n", edgeServiceUnitPath)
	for _, l := range strings.Split(strings.TrimRight(unit, "\n"), "\n") {
		fmt.Println("    " + l)
	}
	fmt.Print("\ninstall it? [y/N]: ")
	if !edgeConfirmed() {
		fmt.Println("nothing was written.")
		return nil
	}
	if edgeServiceEUID() != 0 {
		// Never raised by roger: the unit is staged beside this machine's config, and the owner runs
		// the privileged steps themselves, seeing exactly what they are.
		staged := filepath.Join(filepath.Dir(configPath()), edgeServiceName+".service")
		if err := os.WriteFile(staged, []byte(unit), 0o644); err != nil {
			return err
		}
		fmt.Println("the unit is written to " + staged + ". Installing it needs root - run:")
		fmt.Printf("  sudo install -m 0644 %s %s\n", staged, edgeServiceUnitPath)
		fmt.Println("  sudo systemctl daemon-reload")
		fmt.Println("  sudo systemctl enable --now " + edgeServiceName)
		fmt.Println("then `roger edge service` says whether it is running.")
		return nil
	}
	if err := os.WriteFile(edgeServiceUnitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", edgeServiceName}} {
		if out, err := edgeServiceRun("systemctl", args...); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(out))
		}
	}
	fmt.Println("roger now starts at boot, and is running. `roger edge service` says how it is.")
	return nil
}

func edgeServiceStatus() error {
	enabled, _ := edgeServiceRun("systemctl", "is-enabled", edgeServiceName)
	shown, _ := edgeServiceRun("systemctl", "show", edgeServiceName, "-p", "ActiveState", "-p", "ActiveEnterTimestamp")
	prop := map[string]string{}
	for _, l := range strings.Split(shown, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			prop[k] = v
		}
	}
	boot := "no"
	if strings.TrimSpace(enabled) == "enabled" {
		boot = "yes"
	}
	fmt.Println("starts at boot: " + boot)
	if prop["ActiveState"] == "active" {
		since := ""
		if ts := prop["ActiveEnterTimestamp"]; ts != "" {
			since = ", since " + ts
		}
		fmt.Println("running: yes" + since)
	} else {
		fmt.Println("running: no")
	}
	if boot == "no" {
		fmt.Println("  `roger edge service install` makes roger start at boot.")
	}
	return nil
}

func edgeServiceRemove() error {
	fmt.Printf("stop roger's boot service and remove %s? [y/N]: ", edgeServiceUnitPath)
	if !edgeConfirmed() {
		fmt.Println("left as it is.")
		return nil
	}
	if edgeServiceEUID() != 0 {
		fmt.Println("removing it needs root - run:")
		fmt.Println("  sudo systemctl disable --now " + edgeServiceName)
		fmt.Println("  sudo rm " + edgeServiceUnitPath)
		fmt.Println("  sudo systemctl daemon-reload")
		return nil
	}
	if out, err := edgeServiceRun("systemctl", "disable", "--now", edgeServiceName); err != nil {
		return fmt.Errorf("systemctl disable: %v: %s", err, strings.TrimSpace(out))
	}
	if err := os.Remove(edgeServiceUnitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	_, _ = edgeServiceRun("systemctl", "daemon-reload")
	fmt.Println("roger no longer starts at boot.")
	return nil
}
