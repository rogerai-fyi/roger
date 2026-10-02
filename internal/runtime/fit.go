package runtime

// THE RUNTIME'S OWN FITNESS ANSWER (local_models.feature §4). llama.cpp fits a model to free device
// memory itself, and its release carries a tool that answers without loading anything:
// llama-fit-params. Once a file is on disk roger asks it and takes its answer, rather than
// re-deriving one; roger's own arithmetic is for BEFORE the file exists, when it cannot be asked.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// FitSpec is one question to the runtime: will this file run at this context, here, now.
type FitSpec struct {
	Exe   string // llama-fit-params, from the same verified tree as the server
	Model string
	Ctx   int
	Env   map[string]string
}

// DeviceUse is the runtime's own estimate for one device, in bytes.
type DeviceUse struct {
	Name                    string
	Model, Context, Compute int64
}

// Fitted is the runtime's answer at the context asked, and where it would fit when it does not.
type Fitted struct {
	Fits    bool
	Ctx     int         // the context the answer is AT
	MaxCtx  int         // when it does not fit: the context the runtime would choose itself, 0 for none
	Said    string      // when it does not fit: the runtime's reason, in its words
	Devices []DeviceUse // its breakdown at Ctx
}

// fitWithin bounds each question: the tool reads the header and does arithmetic, it loads nothing.
const fitWithin = 60 * time.Second

// FitExe is the fit tool that sits beside a server binary in the same tree.
func FitExe(serverExe string) string {
	return filepath.Join(filepath.Dir(serverExe), "llama-fit-params"+filepath.Ext(serverExe))
}

// Fit asks the runtime. An error means it could not answer - a missing tool, an unreadable file,
// an older build - and is never a "no".
func Fit(ctx context.Context, sp FitSpec) (Fitted, error) {
	if sp.Exe == "" || sp.Model == "" || sp.Ctx <= 0 {
		return Fitted{}, errors.New("a fit check needs the tool, the model and a context")
	}
	f := Fitted{Ctx: sp.Ctx}
	ctxArg := strconv.Itoa(sp.Ctx)

	out, _, err := runFit(ctx, sp, "-m", sp.Model, "-c", ctxArg, "-fitp", "on")
	if err != nil {
		return Fitted{}, err
	}
	if f.Devices, err = parseDevices(out); err != nil {
		return Fitted{}, err
	}

	out, said, err := runFit(ctx, sp, "-m", sp.Model, "-c", ctxArg)
	var exit *exec.ExitError
	switch {
	case err == nil:
		f.Fits = true
		return f, nil
	case !errors.As(err, &exit) || said == "":
		return Fitted{}, err // it did not say "no"; it failed to answer
	}
	f.Said = said
	// Unasked, the runtime chooses the longest context it can hold (never under its own floor).
	if out, _, err = runFit(ctx, sp, "-m", sp.Model); err == nil {
		f.MaxCtx = fittedCtx(out)
	}
	return f, nil
}

// runFit runs the tool once. It returns stdout, the runtime's "failed to fit" reason when it gave
// one, and the error.
func runFit(ctx context.Context, sp FitSpec, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, fitWithin)
	defer cancel()
	cmd := exec.CommandContext(ctx, sp.Exe, args...)
	cmd.Env = append(os.Environ(), envPairs(sp.Env)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	said := ""
	const marker = "failed to fit params to free device memory: "
	for _, l := range strings.Split(stderr.String(), "\n") {
		if i := strings.Index(l, marker); i >= 0 {
			said = strings.TrimSuffix(strings.TrimSpace(l[i+len(marker):]), ", abort")
		}
	}
	if err != nil && said == "" {
		last := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(last, "\n"); i >= 0 {
			last = last[i+1:]
		}
		return stdout.String(), "", fmt.Errorf("the runtime's fit check did not answer: %v: %s", err, last)
	}
	return stdout.String(), said, err
}

// parseDevices reads "<device> <model MiB> <context MiB> <compute MiB>" per line.
func parseDevices(out string) ([]DeviceUse, error) {
	var ds []DeviceUse
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		fs := strings.Fields(l)
		if len(fs) < 4 {
			continue
		}
		n := len(fs)
		var mib [3]int64
		ok := true
		for i := range mib {
			v, err := strconv.ParseInt(fs[n-3+i], 10, 64)
			ok = ok && err == nil
			mib[i] = v << 20
		}
		if ok {
			ds = append(ds, DeviceUse{Name: strings.Join(fs[:n-3], " "), Model: mib[0], Context: mib[1], Compute: mib[2]})
		}
	}
	if len(ds) == 0 {
		return nil, fmt.Errorf("the runtime's fit check printed no estimate: %q", strings.TrimSpace(out))
	}
	return ds, nil
}

// fittedCtx is the N of "-c N" in the arguments the runtime fitted.
func fittedCtx(out string) int {
	fs := strings.Fields(out)
	for i := 0; i+1 < len(fs); i++ {
		if fs[i] == "-c" {
			n, _ := strconv.Atoi(fs[i+1])
			return n
		}
	}
	return 0
}
