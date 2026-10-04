package main

// ROGER SERVES A MODEL ITSELF (features/serving/local_models.feature).
//
//	roger model                         what is on this disk, and what is being served
//	roger model search [words]          look in the wider world (HuggingFace) - an action, never automatic
//	roger model files <owner/repo>      every quantisation in a repo, judged against THIS machine
//	roger model get <owner/repo>[:Q]    fetch one, verified; the suggested quant when none is named
//	roger model serve <name|repo|path>  serve it here, with no other inference software installed
//	                  [--detach]        ...and keep serving after this terminal closes
//	roger model status                  what is being served, in full
//	roger model stop                    stop serving
//	roger model rm <name>               delete a model from disk
//
// Every step is internal/localmodel; this file only talks to the owner.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"rogerai.fm/roger/v6/internal/catalog"
	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/hfhub"
	"rogerai.fm/roger/v6/internal/localmodel"
	"rogerai.fm/roger/v6/internal/provision"
	"rogerai.fm/roger/v6/internal/runtime"
)

// The seams a test replaces: where things live, what the machine has, where the index is, how the
// runtime arrives, and how a running serve is told to finish.
var (
	modelOut  io.Writer = os.Stdout
	modelHome           = func() localmodel.Home {
		return localmodel.Home{Dir: filepath.Join(filepath.Dir(configPath()), "local")}
	}
	modelHW        = detectLocalHW
	modelHubBase   = hfhub.DefaultBase
	modelFetcher   = func() *provision.Fetcher { return &provision.Fetcher{OnProgress: modelProgress} }
	modelUntilDone = func(ctx context.Context) {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(sig)
		select {
		case <-sig:
		case <-ctx.Done():
		}
	}
	modelStopWait                   = 10 * time.Second
	modelServeEnv map[string]string // the runtime's environment additions; a test's fake runtime reads it
	// modelSpawn starts `roger <args>` detached, its output in logPath, so a serve outlives the
	// terminal (or the TUI, or the Edge request) that asked for it.
	modelSpawn = func(args []string, logPath string) error {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		defer log.Close()
		c := exec.Command(exe, args...)
		c.Stdout, c.Stderr = log, log
		if err := edgeDetachFn(c); err != nil {
			return err
		}
		go func() { _ = c.Wait() }() // reaped, so it is never a zombie for this process's life
		return nil
	}
	modelDetachWait = 2 * time.Minute
)

// A runtime roger started is found by detection with its key, so `roger share` and the TUI pick
// it up like any other local server - the dispatch ladder needs no special case.
func init() {
	detect.Served = func() []detect.Endpoint {
		var out []detect.Endpoint
		for _, s := range modelHome().LiveAll() {
			out = append(out, detect.Endpoint{Name: "roger", Base: s.BaseURL, Key: s.Key})
		}
		return out
	}
}

func modelHub() *hfhub.Client {
	tok := os.Getenv("HF_TOKEN")
	if tok == "" {
		tok = os.Getenv("HUGGING_FACE_HUB_TOKEN")
	}
	return &hfhub.Client{Base: modelHubBase, Token: tok}
}

type modelArgv struct {
	pos    []string
	ctx    int
	port   int
	limit  int
	yes    bool
	detach bool
	add    bool   // serve beside what is on the air, rather than switching (local_models.feature §5)
	on     string // a member of the Edge: the same command, on that machine (remote_model.feature)
}

func parseModelArgv(args []string) (modelArgv, error) {
	var a modelArgv
	for i := 0; i < len(args); i++ {
		s := args[i]
		if s == "--yes" || s == "-y" {
			a.yes = true
			continue
		}
		if s == "--detach" || s == "-d" {
			a.detach = true
			continue
		}
		if s == "--add" {
			a.add = true
			continue
		}
		if !strings.HasPrefix(s, "--") {
			a.pos = append(a.pos, s)
			continue
		}
		name, val, has := strings.Cut(strings.TrimPrefix(s, "--"), "=")
		if !has {
			if i+1 >= len(args) {
				return a, fmt.Errorf("--%s needs a value", name)
			}
			i++
			val = args[i]
		}
		if name == "on" {
			a.on = strings.TrimSpace(val)
			continue
		}
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return a, fmt.Errorf("--%s takes a positive number, not %q", name, val)
		}
		switch name {
		case "ctx":
			a.ctx = n
		case "port":
			a.port = n
		case "limit":
			a.limit = n
		default:
			return a, fmt.Errorf("unknown flag --%s", name)
		}
	}
	return a, nil
}

func cmdModel(cfg config, args []string) error {
	verb := "list"
	if len(args) > 0 {
		verb, args = args[0], args[1:]
	}
	a, err := parseModelArgv(args)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if a.on != "" {
		return modelOnNode(cfg, verb, a)
	}
	switch verb {
	case "list", "ls":
		return modelList()
	case "search":
		return modelSearch(ctx, strings.Join(a.pos, " "), a.limit)
	case "files", "fit":
		if len(a.pos) != 1 {
			return errors.New("usage: roger model files <owner/repo> [--ctx N]")
		}
		return modelFiles(ctx, a.pos[0], a.ctx)
	case "get", "pull":
		if len(a.pos) != 1 {
			return errors.New("usage: roger model get <owner/repo>[:QUANT] [--ctx N] [--yes]")
		}
		_, err := modelGet(ctx, a.pos[0], a.ctx, a.yes)
		if errors.Is(err, errModelDeclined) {
			return nil // the owner said no, which is an answer, not a failure
		}
		return err
	case "serve":
		if len(a.pos) != 1 {
			return errors.New("usage: roger model serve <name | owner/repo[:QUANT] | file.gguf> [--ctx N] [--port N] [--detach] [--add]")
		}
		return modelServe(ctx, a)
	case "status":
		return modelStatus()
	case "idle":
		return modelIdle(a.pos)
	case "budget":
		return modelBudgetCmd(a.pos)
	case "runtime":
		return modelRuntimeCmd(a.pos)
	case "stop":
		return modelStop(a.pos)
	case "rm", "remove":
		if len(a.pos) != 1 {
			return errors.New("usage: roger model rm <name> [--yes]")
		}
		return modelRemove(a.pos[0], a.yes)
	}
	return fmt.Errorf("unknown: roger model %s (list, search, files, get, serve, status, stop, rm, idle, budget, runtime)", verb)
}

func modelList() error {
	h := modelHome()
	all, err := h.List()
	if err != nil {
		return err
	}
	lives := h.LiveAll()
	if len(all) == 0 {
		fmt.Fprintf(modelOut, "no models on this machine yet (%s).\n", h.Models())
		fmt.Fprintln(modelOut, "  roger model search <words>   look for one     roger model serve file.gguf   serve one you already have")
		return nil
	}
	used, n, _ := h.Usage()
	budget := ""
	if s, err := h.Settings(); err == nil && s.DiskBudget > 0 {
		budget = " of a " + provision.BytesWord(s.DiskBudget) + " budget"
	}
	fmt.Fprintf(modelOut, "%d models, %s%s, in %s\n\n", n, provision.BytesWord(used), budget, h.Models())
	for _, m := range all {
		mark := " "
		if _, on := h.ServingModel(m); on {
			mark = "▶"
		}
		check := "checked"
		if !m.Verified() {
			check = "found  " // dropped in by hand: served on the owner's word
		}
		fmt.Fprintf(modelOut, "%s %-44s %-8s %9s  %s\n", mark, m.ID, modelDash(m.Quant), provision.BytesWord(m.Bytes), check)
	}
	if len(lives) > 0 {
		fmt.Fprintln(modelOut)
	}
	for _, live := range lives {
		fmt.Fprintf(modelOut, "▶ serving %s as %q since %s ago\n", live.ID, live.Band, edge.Age(time.Since(live.Started)))
	}
	return nil
}

func modelDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func modelSearch(ctx context.Context, text string, limit int) error {
	ms, err := modelHub().Search(ctx, hfhub.Query{Text: text, Limit: limit})
	if err != nil {
		return modelSourceErr(err)
	}
	fmt.Fprintf(modelOut, "%d repos from HuggingFace - what their authors published, not RogerAI's word\n\n", len(ms))
	for _, m := range ms {
		gate := ""
		if m.Gated {
			gate = "  GATED: needs accepted access + HF_TOKEN"
		}
		fmt.Fprintf(modelOut, "  %-52s %9s downloads  %s%s\n", m.Repo, countWord(m.Downloads), modelDash(m.License), gate)
	}
	if len(ms) > 0 {
		fmt.Fprintf(modelOut, "\nroger model files %s   - see every quantisation judged against this machine\n", ms[0].Repo)
	}
	return nil
}

func countWord(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return strconv.Itoa(n)
}

// modelRank is one repository judged against this machine: one header read for every quant.
func modelRank(ctx context.Context, repo string, ctxLen int) ([]localmodel.Choice, localmodel.Budget, error) {
	hub := modelHub()
	files, err := hub.Files(ctx, repo)
	if err != nil {
		return nil, localmodel.Budget{}, modelSourceErr(err)
	}
	b := modelBudget()
	var shape catalog.Shape
	for _, f := range files {
		if localmodel.Unservable(f.Path) != "" {
			continue
		}
		s, err := hub.ShapeOf(ctx, repo, f.Path, f.Bytes)
		if errors.Is(err, detect.ErrNotGenerative) {
			return nil, b, fmt.Errorf("%s is an embedding model, not one you serve chat from", repo)
		}
		if err == nil {
			shape = s
		}
		break // the architecture is the same in every quant; the smallest answers for all
	}
	return localmodel.Rank(files, shape, b, ctxLen), b, nil
}

func modelFiles(ctx context.Context, repo string, ctxLen int) error {
	cs, b, err := modelRank(ctx, repo, ctxLen)
	if err != nil {
		return err
	}
	if len(cs) == 0 {
		return fmt.Errorf("%s has no GGUF weights, so there is nothing here this runtime can serve", repo)
	}
	modelBudgetLine(b)
	pick := localmodel.Suggest(cs)
	labels := localmodel.Labels(cs)
	w := 8
	for _, l := range labels {
		w = max(w, len(l))
	}
	for i, c := range cs {
		mark := " "
		if i == pick {
			mark = "*"
		}
		verdict := c.Verdict.Short()
		if c.Verdict.PaceLine() != "" && c.Verdict.Pace.Short() != "" {
			verdict += "  " + c.Verdict.Pace.Short() // a second claim, in its own column
		}
		if c.Unservable != "" {
			verdict = "not servable: " + c.Unservable
		}
		fmt.Fprintf(modelOut, "%s %-*s %10s  %s\n", mark, w, modelDash(labels[i]), provision.BytesWord(c.File.Bytes), verdict)
	}
	if pick >= 0 {
		fmt.Fprintf(modelOut, "\n* suggested. roger model get %s:%s\n", repo, labels[pick])
	} else {
		fmt.Fprintln(modelOut, "\nnothing here fits comfortably; you can still fetch one by name on your own word.")
	}
	return nil
}

func modelBudgetLine(b localmodel.Budget) {
	if b.Known() {
		fmt.Fprintf(modelOut, "this machine: %s of %s\n", provision.BytesWord(b.Bytes), b.Pool)
		if b.Note != "" {
			fmt.Fprintf(modelOut, "  (%s)\n", b.Note)
		}
		if b.BW.BytesPerSec > 0 {
			fmt.Fprintf(modelOut, "memory speed: %s\n\n", b.BW.From)
		} else {
			fmt.Fprintf(modelOut, "memory speed: cannot say - %s\n\n", b.BW.Why)
		}
	} else {
		fmt.Fprintf(modelOut, "this machine: cannot say - %s\n\n", b.Why)
	}
}

func modelGet(ctx context.Context, ref string, ctxLen int, yes bool) (localmodel.Model, error) {
	repo, want, _ := strings.Cut(ref, ":")
	cs, b, err := modelRank(ctx, repo, ctxLen)
	if err != nil {
		return localmodel.Model{}, err
	}
	var c localmodel.Choice
	if want == "" {
		i := localmodel.Suggest(cs)
		if i < 0 {
			return localmodel.Model{}, fmt.Errorf("nothing in %s fits this machine comfortably - `roger model files %s` shows each, and naming one fetches it anyway", repo, repo)
		}
		c = cs[i]
	} else {
		files := make([]hfhub.File, len(cs))
		for i := range cs {
			files[i] = cs[i].File
		}
		f, err := localmodel.Pick(files, want)
		if err != nil {
			return localmodel.Model{}, err
		}
		for _, x := range cs {
			if x.File.Path == f.Path {
				c = x
			}
		}
	}
	license := modelLicense(ctx, repo)
	modelBudgetLine(b)
	fmt.Fprintf(modelOut, "%s  %s  %s\n", repo, c.File.Path, provision.BytesWord(c.File.Bytes))
	fmt.Fprintf(modelOut, "  %s\n", c.Verdict.Line())
	if s := c.Verdict.Sum(); s != "" {
		fmt.Fprintf(modelOut, "  %s\n", s)
	}
	fmt.Fprintf(modelOut, "  licence: %s (as its author published it; roger records it and does not referee it)\n", modelDash(license))
	fmt.Fprintln(modelOut, "  source: HuggingFace - the author's word, checked against the digest the index publishes")
	plan, err := modelHome().RoomFor(c.File.Bytes)
	if err != nil {
		return localmodel.Model{}, err
	}
	if line := localmodel.RoomLine(plan); line != "" {
		fmt.Fprintf(modelOut, "  %s\n", line)
	}
	if !yes {
		fmt.Fprint(modelOut, "fetch it? [y/N]: ")
		if !edgeConfirmed() {
			fmt.Fprintln(modelOut, "not fetched. Nothing changed.")
			return localmodel.Model{}, errModelDeclined
		}
	}
	m, err := modelHome().Fetch(ctx, modelHub(), *modelFetcher(), repo, c.File, license)
	modelProgressDone()
	if errors.Is(err, localmodel.ErrBusy) {
		if j, ok := modelHome().Job(); ok {
			return localmodel.Model{}, fmt.Errorf("%w (%s, %s of %s) - wait for it, or `roger model status`", err, j.File,
				provision.BytesWord(j.Done), provision.BytesWord(j.Total))
		}
	}
	if err != nil {
		return localmodel.Model{}, modelFetchErr(err)
	}
	fmt.Fprintf(modelOut, "fetched and verified: %s (sha256 %s). roger model serve %s\n", m.ID, provision.ShortDigest(m.SHA256), m.ID)
	return m, nil
}

var errModelDeclined = errors.New("declined")

// modelLicense is what the index says the licence is. Shown and recorded; it stops nothing.
func modelLicense(ctx context.Context, repo string) string {
	ms, err := modelHub().Search(ctx, hfhub.Query{Text: repo, Limit: 5})
	if err != nil {
		return ""
	}
	for _, m := range ms {
		if strings.EqualFold(m.Repo, repo) {
			return m.License
		}
	}
	return ""
}

// modelResolve turns what the owner typed into a model on this disk: a path, a name roger knows,
// or a repository it fetches first (with the same consent as `get`).
func modelResolve(ctx context.Context, ref string, ctxLen int, yes bool) (localmodel.Model, error) {
	if strings.HasSuffix(strings.ToLower(ref), ".gguf") {
		if _, err := os.Stat(ref); err == nil {
			return localmodel.FromPath(ref)
		}
	}
	h := modelHome()
	m, err := h.Find(ref)
	if err == nil || !errors.Is(err, localmodel.ErrUnknownModel) || !strings.Contains(ref, "/") {
		return m, err
	}
	fmt.Fprintf(modelOut, "%s is not on this machine yet.\n", ref)
	return modelGet(ctx, ref, ctxLen, yes)
}

func modelServe(ctx context.Context, a modelArgv) error {
	m, err := modelResolve(ctx, a.pos[0], a.ctx, a.yes)
	if err != nil {
		if errors.Is(err, errModelDeclined) {
			return nil
		}
		return err
	}
	// The runtime first: serving needs it anyway, and once it is here its own fit check answers. The
	// build it resolves to decides the pool (local_models.feature §10).
	rt, err := modelRuntime(ctx)
	if err != nil {
		return modelRuntimeErr(err)
	}
	for _, n := range rt.Notes {
		fmt.Fprintln(modelOut, n)
	}
	b := modelBudgetFor(rt.Offloads)
	h := modelHome()
	var beside []localmodel.Serving
	if a.add {
		if beside, err = modelBeside(h, m); err != nil {
			return err
		}
	}
	ctxLen, says, err := modelPlan(ctx, m, b, a.ctx, rt.Exe, beside...)
	if err != nil {
		return err
	}
	fmt.Fprintln(modelOut, says)
	prev, hadPrev := h.Live()
	if a.add {
		hadPrev = false // added beside, nothing is switched away
	}
	if a.detach {
		return modelServeDetached(h, m, ctxLen, a.port, prev, hadPrev, a.add)
	}
	fmt.Fprintf(modelOut, "starting llama.cpp %s on %s at %d context...\n", rt.Word(), m.File, ctxLen)
	dev := ""
	if rt.Offloads && rt.Build != runtime.BuildMetal {
		if dev = modelPickDevice(rt.Devices, modelNeed(m, b, ctxLen)); dev != "" {
			fmt.Fprintf(modelOut, "  on one card, %s: it fits there, and one card answers faster than a split\n", dev)
		}
	}
	srv, s, err := h.Serve(ctx, localmodel.ServeOpts{Model: m, Exe: rt.Exe, Tag: rt.Word(), Ctx: ctxLen, Port: a.port, Budget: b, Env: modelServeEnv, Device: dev, Build: string(rt.Build)})
	if err != nil && rt.Build != runtime.BuildCPU && rt.Build != runtime.BuildMetal {
		// A GPU build that cannot start the model is never a silent fallback (§10): auto says so and
		// serves with the CPU build, judged again in system memory; an explicit choice stops here.
		failed := fmt.Errorf("the %s build could not start it: %v", rt.Build, err)
		if modelRuntimeChoice() != "auto" {
			return fmt.Errorf("%v\n  nothing was served; `roger model runtime cpu` would serve it with the CPU build", failed)
		}
		fmt.Fprintf(modelOut, "%v - serving with the CPU build instead\n", failed)
		if rt, err = modelRuntimeFor(ctx, "cpu"); err != nil {
			return modelRuntimeErr(err)
		}
		for _, n := range rt.Notes {
			fmt.Fprintln(modelOut, n)
		}
		// The screens say what serves under the owner's choice, and why the GPU build did not.
		if cpu, _, perr := runtime.Plan(modelHost(), "cpu"); perr == nil {
			modelRecordSettled(modelSettled{Choice: "auto", Platform: cpu[0].Platform, Tag: cpu[0].Tag,
				Notes: append([]string{failed.Error()}, rt.Notes...)})
		}
		b = modelBudgetFor(false)
		if ctxLen, says, err = modelPlan(ctx, m, b, a.ctx, rt.Exe, beside...); err != nil {
			return err
		}
		fmt.Fprintln(modelOut, says)
		fmt.Fprintf(modelOut, "starting llama.cpp %s on %s at %d context...\n", rt.Word(), m.File, ctxLen)
		srv, s, err = h.Serve(ctx, localmodel.ServeOpts{Model: m, Exe: rt.Exe, Tag: rt.Word(), Ctx: ctxLen, Port: a.port, Budget: b, Env: modelServeEnv, Build: string(rt.Build)})
	}
	if err != nil {
		if hadPrev {
			return fmt.Errorf("%v\n  %s is still serving - nothing was switched", err, prev.ID)
		}
		return err
	}
	defer h.Release(s)
	defer func() { _ = srv.Stop(context.Background()) }()
	// The new one answers; only now is the old one stopped.
	if hadPrev && prev.Supervisor != os.Getpid() {
		says, _ := modelSignalStop(prev)
		fmt.Fprintf(modelOut, "switched from %s: %s\n", prev.ID, says)
	}
	where := "on the air locally"
	if len(beside) > 0 {
		bands := make([]string, len(beside))
		for i, o := range beside {
			bands[i] = o.Band
		}
		where += " beside " + strings.Join(bands, ", ")
	}
	fmt.Fprintf(modelOut, "%s: %q - %s, %s, sha256 %s, %d context\n", where, s.Band, s.ID, modelDash(s.Quant),
		modelDash(provision.ShortDigest(s.SHA256)), s.Ctx)
	fmt.Fprintf(modelOut, "  %s (key in %s)\n", s.BaseURL, h.RecordOf(s))
	fmt.Fprintln(modelOut, "  roger share picks it up like any local server. ctrl-c or `roger model stop` ends it.")
	h.ClearStop(s.Band)                // serving this band again: its last stop is history
	_ = h.MarkServed(m.ID, time.Now()) // what a disk budget's "least recently served" counts
	// ...and again when it ends, so a model served for a month is not counted as used a month ago.
	defer func() { _ = h.MarkServed(m.ID, time.Now()) }()
	waitCtx, endWait := context.WithCancel(ctx)
	defer endWait()
	idleWhy := make(chan string, 1)
	// Always watched: the owner can set or turn off the timeout while this serve runs, and it hears.
	go func() {
		why := modelWatchIdle(waitCtx, h, s)
		idleWhy <- why
		if why != "" {
			endWait()
		}
	}()
	modelUntilDone(waitCtx)
	select {
	case why := <-idleWhy:
		if why != "" {
			fmt.Fprintf(modelOut, "stopped: %s\n", why)
			return nil
		}
	default:
	}
	fmt.Fprintf(modelOut, "stopping %s.\n", s.Band)
	return nil
}

// modelServeDetached hands the serve to a roger of its own and waits until it is really answering.
// Everything that can be refused - the fit, the runtime, the consent to fetch - was already settled
// here, in front of the owner, so the background has nothing left to ask.
func modelServeDetached(h localmodel.Home, m localmodel.Model, ctxLen, port int, prev localmodel.Serving, hadPrev, add bool) error {
	ref := m.Path
	if _, err := h.Find(m.ID); err == nil {
		ref = m.ID
	}
	args := []string{"model", "serve", ref, "--yes", "--ctx", strconv.Itoa(ctxLen)}
	if port > 0 {
		args = append(args, "--port", strconv.Itoa(port))
	}
	if add {
		args = append(args, "--add")
	}
	logPath := filepath.Join(h.Dir, "serve.log")
	if err := os.MkdirAll(h.Dir, 0o700); err != nil {
		return err
	}
	if err := modelSpawn(args, logPath); err != nil {
		return fmt.Errorf("could not start serving in the background: %w", err)
	}
	fmt.Fprintf(modelOut, "starting %s in the background...\n", m.ID)
	deadline := time.Now().Add(modelDetachWait)
	for time.Now().Before(deadline) {
		if s, ok := h.ServingModel(m); ok && (!hadPrev || s.Supervisor != prev.Supervisor) {
			where := "on the air locally"
			if add {
				var bands []string
				for _, o := range h.LiveAll() {
					if o.Port != s.Port {
						bands = append(bands, o.Band)
					}
				}
				if len(bands) > 0 {
					where += " beside " + strings.Join(bands, ", ")
				}
			}
			fmt.Fprintf(modelOut, "%s: %q - %s, %d context, at %s\n", where, s.Band, s.ID, s.Ctx, s.BaseURL)
			fmt.Fprintln(modelOut, "  it keeps serving after this terminal closes. `roger model status` · `roger model stop`")
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	tail := ""
	if b, err := os.ReadFile(logPath); err == nil {
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(lines) > 6 {
			lines = lines[len(lines)-6:]
		}
		tail = "\n  " + strings.Join(lines, "\n  ")
	}
	still := ""
	if hadPrev {
		still = fmt.Sprintf("\n  %s is still serving - nothing was switched", prev.ID)
	}
	return fmt.Errorf("it did not come on the air within %s (log: %s)%s%s", modelDetachWait, logPath, tail, still)
}

// modelFitExe is the seam for where the runtime's fit tool sits beside its server.
var modelFitExe = runtime.FitExe

// modelInstalledExe is the server of the build the owner's choice predicts, when it is already on
// this machine, else "".
func modelInstalledExe() string {
	a, _, err := modelPredict()
	if err != nil {
		return ""
	}
	exe, ok := runtime.InstalledBuild(a, modelHome().Runtime())
	if !ok {
		return "" // not here yet: roger's estimate stands, and says so
	}
	return exe
}

// modelRuntimeErr says why there is no runtime, and what the machine can still do.
func modelRuntimeErr(err error) error {
	if errors.Is(err, runtime.ErrMissingLibrary) {
		return fmt.Errorf("local serving needs a system package first: %v", err)
	}
	if errors.Is(err, runtime.ErrNoRuntime) {
		return fmt.Errorf("local serving is not available on this machine: %v.\n"+
			"  it can still join an Edge, relay, and be monitored and controlled from the authority", err)
	}
	return fmt.Errorf("could not get the runtime: %w", err)
}

// modelPlan settles the context a model will serve at, or refuses with the arithmetic - never
// silently reducing what the owner asked for. says is the line to show the owner either way. With
// the runtime here (serverExe), its own fit check answers and roger's estimate steps aside.
//
// beside is what stays on the air next to it (serve --add). The runtime's check already counts it,
// because it judges what is free now; roger's estimate adds their needs to this one.
func modelPlan(ctx context.Context, m localmodel.Model, b localmodel.Budget, want int, serverExe string, beside ...localmodel.Serving) (ctxLen int, says string, err error) {
	shape, err := detect.ReadShape(m.Path)
	switch {
	case errors.Is(err, detect.ErrNotGenerative):
		return 0, "", fmt.Errorf("%s is an embedding model, not one you serve chat from", m.ID)
	case err != nil:
		return want, fmt.Sprintf("%s: cannot judge the fit (%v); serving on your word.", m.ID, err), nil
	}
	v := localmodel.Judge(shape, b, want)
	if serverExe == "" {
		v = v.Settled(runtime.Fitted{}, errors.New("the runtime is not on this machine yet"))
	} else {
		v = v.Settled(runtime.Fit(ctx, runtime.FitSpec{Exe: modelFitExe(serverExe), Model: m.Path, Ctx: v.Ctx, Env: modelServeEnv}))
	}
	if len(beside) > 0 {
		bands := make([]string, len(beside))
		need := v.Need.Total
		for i, s := range beside {
			bands[i] = s.Band
			o, err := modelHome().Find(s.ID)
			if err != nil {
				return 0, "", fmt.Errorf("roger cannot judge %s, which is on the air, so it cannot say whether %s fits beside it: %v", s.ID, m.ID, err)
			}
			sh, err := detect.ReadShape(o.Path)
			if err != nil {
				return 0, "", fmt.Errorf("roger cannot judge %s, which is on the air, so it cannot say whether %s fits beside it: %v", s.ID, m.ID, err)
			}
			need += localmodel.Judge(sh, b, s.Ctx).Need.Total
		}
		together := strings.Join(bands, ", ")
		// roger's own sum applies even when the runtime says yes: the runtime judges free memory, and
		// the weights of what is already on the air are mapped pages it may not count as used.
		switch {
		case v.Fit == catalog.FitWontFit, b.Known() && need > b.Bytes:
			msg := fmt.Sprintf("%s and %s do not fit together with %s: ", m.ID, together, together)
			if v.Fit == catalog.FitWontFit {
				msg += v.Line()
			} else {
				msg += fmt.Sprintf("together they need %s of %s %s (roger's estimate)", localmodel.BytesWord(need),
					localmodel.BytesWord(b.Bytes), b.Pool)
			}
			return 0, "", fmt.Errorf("%s\n  to serve it instead of %s: roger model serve %s", msg, beside[len(beside)-1].Band, m.ID)
		}
	}
	if v.Fit == catalog.FitWontFit {
		msg := v.Line()
		if s := v.Sum(); s != "" {
			msg += "\n  " + s
		}
		if v.MaxCtx > 0 {
			msg += fmt.Sprintf("\n  roger model serve %s --ctx %d", m.ID, v.MaxCtx)
		}
		if s, ok := modelHome().Live(); ok && v.ByRuntime && s.ID != m.ID {
			msg += fmt.Sprintf("\n  %s is being served and holds memory the check counted as in use; "+
				"a switch runs both until the new one answers. `roger model stop` first to replace it outright", s.ID)
		}
		return 0, "", errors.New(msg)
	}
	return v.Ctx, fmt.Sprintf("%s: %s", m.ID, v.Line()), nil
}

// modelIdleEvery is how often a serve reads the runtime's counters when an idle timeout is set.
var modelIdleEvery = time.Minute

// modelIdleMin is the shortest timeout an owner may set: shorter than a reading apart is noise.
const modelIdleMin = time.Minute

// modelIdle shows or sets the idle timeout (local_models.feature §5): off by default.
func modelIdle(pos []string) error { return modelPrint(modelIdleSays(pos)) }

// modelPrint is a setting's sentence on the terminal; every surface shares the sentence itself.
func modelPrint(says string, err error) error {
	if err != nil {
		return err
	}
	fmt.Fprintln(modelOut, says)
	return nil
}

// modelIdleSays shows or sets the idle timeout and says what it is now.
func modelIdleSays(pos []string) (string, error) {
	h := modelHome()
	s, err := h.Settings()
	if err != nil {
		return "", err
	}
	if len(pos) == 1 {
		switch v := strings.ToLower(pos[0]); v {
		case "off", "0", "never":
			s.IdleAfter = 0
		default:
			d, err := time.ParseDuration(v)
			if err != nil || d < modelIdleMin {
				return "", fmt.Errorf("an idle timeout is a duration of at least %s (30m, 2h) or off, not %q", modelIdleMin, pos[0])
			}
			s.IdleAfter = d
		}
		if err := h.SetSettings(s); err != nil {
			return "", err
		}
	} else if len(pos) > 1 {
		return "", errors.New("usage: roger model idle [<duration> | off]")
	}
	return modelIdleLine(s), nil
}

// modelBudgetCmd shows or sets the disk budget for models (local_models.feature §6): none by default.
func modelBudgetCmd(pos []string) error { return modelPrint(modelBudgetSays(pos)) }

// modelBudgetSays shows or sets the disk budget and says what it is now - and, for a budget below
// what is already on disk, what the next fetch would remove: setting one removes nothing by itself
// (local_models.feature §11).
func modelBudgetSays(pos []string) (string, error) {
	h := modelHome()
	s, err := h.Settings()
	if err != nil {
		return "", err
	}
	switch {
	case len(pos) > 1:
		return "", errors.New("usage: roger model budget [<size, like 20G> | off]")
	case len(pos) == 1 && strings.EqualFold(pos[0], "off"):
		s.DiskBudget = 0
	case len(pos) == 1:
		n, ok := parseByteSize(pos[0])
		if !ok || n < 1<<20 {
			return "", fmt.Errorf("a budget is a size of at least 1M (500M, 20G, 1T) or off, not %q", pos[0])
		}
		s.DiskBudget = n
	}
	if len(pos) == 1 {
		if err := h.SetSettings(s); err != nil {
			return "", err
		}
	}
	says := modelBudgetWord(s)
	if used, _, _ := h.Usage(); s.DiskBudget > 0 && used > s.DiskBudget {
		says += fmt.Sprintf("\n  %s of models is on disk, past it; nothing is removed now", provision.BytesWord(used))
		if plan, err := h.RoomFor(0); err == nil && len(plan) > 0 {
			says += " - " + strings.TrimPrefix(localmodel.RoomLine(plan), "to stay within the budget you set for models, roger removes first, ")
			says += ", at the next fetch"
		} else if err != nil {
			says += " - " + err.Error()
		}
	}
	return says, nil
}

func modelBudgetWord(s localmodel.Settings) string {
	if s.DiskBudget <= 0 {
		return "budget for models: none - nothing is ever removed to make room"
	}
	return fmt.Sprintf("budget for models: %s - a fetch past it removes the models served least recently first, "+
		"never one being served or one you placed by hand", provision.BytesWord(s.DiskBudget))
}

// parseByteSize reads 500M, 20G, 1.5T (binary units, as every roger screen shows sizes).
func parseByteSize(s string) (int64, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.TrimSuffix(strings.TrimSuffix(s, "IB"), "B")
	mult := map[byte]float64{'K': 1 << 10, 'M': 1 << 20, 'G': 1 << 30, 'T': 1 << 40}
	if s == "" {
		return 0, false
	}
	m, ok := mult[s[len(s)-1]]
	if ok {
		s = s[:len(s)-1]
	} else {
		m = 1
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return 0, false
	}
	return int64(f * m), true
}

func modelIdleLine(s localmodel.Settings) string {
	if s.IdleAfter <= 0 {
		return "idle timeout: off - a model roger serves keeps serving until it is stopped"
	}
	return fmt.Sprintf("idle timeout: %s - with no turn for that long, roger stops serving and records why", s.IdleAfter)
}

// modelWatchIdle ends a serve whose runtime has counted no tokens for the owner's timeout, and
// records why before it does. It returns the reason, or "" when ctx ended first.
func modelWatchIdle(ctx context.Context, h localmodel.Home, s localmodel.Serving) string {
	w := &localmodel.IdleWatch{}
	tick := time.NewTicker(modelIdleEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ""
		case now := <-tick.C:
			// The owner's setting is read every time, so turning it off (or on) reaches this serve.
			settings, err := h.Settings()
			if err != nil || settings.IdleAfter <= 0 {
				*w = localmodel.IdleWatch{} // off: nothing counts towards a later timeout
				continue
			}
			after := settings.IdleAfter
			w.After = after
			total, busy, err := localmodel.Activity(ctx, s)
			if err != nil {
				continue // an unreadable counter is not idleness
			}
			if w.Observe(now, total, busy) {
				why := localmodel.IdleWhy(after)
				_ = h.RecordStop(localmodel.Stop{Band: s.Band, At: now, Why: why})
				return why
			}
		}
	}
}

func modelStatus() error {
	if j, ok := modelHome().Job(); ok && j.State != localmodel.JobDone {
		fmt.Fprintln(modelOut, modelJobLine(j))
	}
	h := modelHome()
	settings, err := h.Settings()
	if err != nil {
		return err
	}
	lives := h.LiveAll()
	if len(lives) == 0 {
		fmt.Fprintln(modelOut, "nothing is being served by roger on this machine.")
	}
	for _, st := range modelQuietStops(h, lives) {
		fmt.Fprintln(modelOut, "  "+st.Line())
	}
	if len(lives) == 0 {
		fmt.Fprintln(modelOut, "  "+modelIdleLine(settings))
		return nil
	}
	if len(lives) > 1 {
		fmt.Fprintf(modelOut, "serving %d models, each said in full:\n", len(lives))
	}
	for _, s := range lives {
		modelStatusOne(s)
	}
	fmt.Fprintln(modelOut, "  "+modelIdleLine(settings))
	return nil
}

// modelQuietStops is each band roger stopped on its own that is not on the air again.
func modelQuietStops(h localmodel.Home, lives []localmodel.Serving) []localmodel.Stop {
	on := map[string]bool{}
	for _, s := range lives {
		on[s.Band] = true
	}
	var out []localmodel.Stop
	for _, st := range h.Stops() {
		if !on[st.Band] {
			out = append(out, st)
		}
	}
	return out
}

func modelStatusOne(s localmodel.Serving) {
	fmt.Fprintf(modelOut, "serving %q\n", s.Band)
	fmt.Fprintf(modelOut, "  model    %s\n  file     %s\n  quant    %s\n  sha256   %s\n  context  %d\n",
		s.ID, s.File, modelDash(s.Quant), modelDash(s.SHA256), s.Ctx)
	fmt.Fprintf(modelOut, "  runtime  llama.cpp %s, pid %d, for %s\n  address  %s\n",
		s.Runtime, s.RuntimePID, edge.Age(time.Since(s.Started)), s.BaseURL)
	if tps := modelMeasured(s); tps > 0 {
		fmt.Fprintf(modelOut, "  speed    %.1f tok/s measured\n", tps)
	} else if est := modelEstimate(s); est != "" {
		fmt.Fprintf(modelOut, "  speed    %s - nothing answered yet to measure\n", est)
	}
}

// modelBeside is what stays on the air when m is added beside it, or why m cannot be: one band
// answers with one model.
func modelBeside(h localmodel.Home, m localmodel.Model) ([]localmodel.Serving, error) {
	beside := h.LiveAll()
	for _, s := range beside {
		if s.ID == m.ID && s.File == m.File {
			return nil, fmt.Errorf("%s is already on the air as %q - one runtime per model", m.ID, s.Band)
		}
		if s.Band == m.Band {
			return nil, fmt.Errorf("%q is already on the air from %s; one name answers with one model - "+
				"`roger model serve %s` switches to it", s.Band, s.ID, m.ID)
		}
	}
	return beside, nil
}

// modelStop stops one serve named by its band or id, or every one when none is named.
func modelStop(pos []string) error {
	name := ""
	if len(pos) > 0 {
		name = pos[0]
	}
	says, err := modelStopSays(name)
	if err != nil {
		return err
	}
	fmt.Fprintln(modelOut, says)
	return nil
}

// modelStopSays is the stop every surface shares: one serve by band or id, or all when name is "".
func modelStopSays(name string) (string, error) {
	lives := modelHome().LiveAll()
	if len(lives) == 0 {
		return "nothing is being served by roger on this machine - nothing to stop.", nil
	}
	if name != "" {
		var hit []localmodel.Serving
		bands := make([]string, len(lives))
		for i, s := range lives {
			bands[i] = s.Band
			if s.Band == name || s.ID == name {
				hit = append(hit, s)
			}
		}
		if len(hit) == 0 {
			return "", fmt.Errorf("%q is not being served; serving: %s", name, strings.Join(bands, ", "))
		}
		lives = hit
	}
	var said []string
	for _, s := range lives {
		says, err := modelSignalStop(s)
		if err != nil {
			return strings.Join(said, "\n"), err
		}
		said = append(said, says)
	}
	return strings.Join(said, "\n"), nil
}

// modelSignalStop asks the supervising roger to finish, which stops its runtime and clears its
// record. It signals nothing it cannot vouch for, exactly as an Edge stop does.
func modelSignalStop(s localmodel.Serving) (string, error) {
	if s.Supervisor == os.Getpid() {
		return "", errors.New("that is this roger")
	}
	if isRoger, known := edgeProcIsRoger(s.Supervisor); known && !isRoger {
		return "", fmt.Errorf("the process recorded as serving %s is not a roger any more, so nothing was signalled", s.Band)
	}
	if err := edgeSignal(s.Supervisor, syscall.SIGTERM); err != nil {
		return "", fmt.Errorf("could not ask it to stop: %w", err)
	}
	deadline := time.Now().Add(modelStopWait)
	for time.Now().Before(deadline) {
		if !modelHome().StillServing(s) {
			return fmt.Sprintf("stopped serving %s.", s.Band), nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Sprintf("asked %s to stop; it had not finished when this was answered.", s.Band), nil
}

func modelRemove(ref string, yes bool) error {
	h := modelHome()
	m, err := h.Find(ref)
	if err != nil {
		return err
	}
	if _, ok := h.ServingModel(m); ok {
		return fmt.Errorf("%s is being served right now - `roger model stop` first", m.ID)
	}
	if !yes {
		fmt.Fprintf(modelOut, "delete %s (%s) from %s? [y/N]: ", m.ID, provision.BytesWord(m.Bytes), filepath.Dir(m.Path))
		if !edgeConfirmed() {
			fmt.Fprintln(modelOut, "kept. Nothing changed.")
			return nil
		}
	}
	if err := h.Remove(m); err != nil {
		return err
	}
	fmt.Fprintf(modelOut, "removed %s, %s back.\n", m.ID, provision.BytesWord(m.Bytes))
	return nil
}

// modelSourceErr names WHICH failure, because the fix differs for each.
func modelSourceErr(err error) error {
	switch {
	case errors.Is(err, hfhub.ErrGated):
		return fmt.Errorf("%w - accept the terms on the model's page, then set HF_TOKEN", err)
	case errors.Is(err, hfhub.ErrNotFound):
		return fmt.Errorf("%w - check the owner/repo spelling", err)
	case errors.Is(err, hfhub.ErrRateLimited):
		return fmt.Errorf("%w - wait a minute, or set HF_TOKEN for a higher limit", err)
	case errors.Is(err, hfhub.ErrUnreachable):
		return fmt.Errorf("%w - check this machine's network", err)
	}
	return err
}

func modelFetchErr(err error) error {
	switch {
	case errors.Is(err, provision.ErrNoSpace):
		return fmt.Errorf("%w - free some space, or `roger model rm` one you no longer use", err)
	case errors.Is(err, provision.ErrGated):
		return fmt.Errorf("%w - accept the terms on the model's page, then set HF_TOKEN", err)
	case errors.Is(err, provision.ErrCorrupt):
		return fmt.Errorf("%w - nothing was kept; run it again to fetch a fresh copy", err)
	case errors.Is(err, provision.ErrUnreachable):
		return fmt.Errorf("%w - check the network; running it again resumes where it stopped", err)
	case errors.Is(err, provision.ErrRateLimited):
		return fmt.Errorf("%w - wait a minute, then run it again; it resumes", err)
	}
	return err
}

// modelProgress draws one line that moves as the fetch moves: bytes, rate, time left.
var modelProgressShown bool

func modelProgress(p provision.Progress) {
	if p.Total <= 0 {
		return
	}
	line := fmt.Sprintf("  %s of %s", provision.BytesWord(p.Done), provision.BytesWord(p.Total))
	if p.Rate > 0 {
		line += fmt.Sprintf("  ·  %s/s", provision.BytesWord(int64(p.Rate)))
	}
	// An estimate from the first few kilobytes says hours; it is not shown until it means something.
	if p.Remaining > 0 && p.Done*50 >= p.Total {
		line += "  ·  " + p.Remaining.Round(time.Second).String() + " left"
	}
	if p.Resumed {
		line += "  ·  resumed"
	}
	if modelProgressShown && p.Total != modelProgressTotal {
		fmt.Fprintln(modelOut) // a new archive: the finished one keeps its line
	}
	fmt.Fprintf(modelOut, "\r%-72s", line)
	modelProgressShown, modelProgressTotal = true, p.Total
}

// modelProgressTotal is the size of the archive the progress line is about.
var modelProgressTotal int64

func modelProgressDone() {
	if modelProgressShown {
		fmt.Fprintln(modelOut)
		modelProgressShown = false
	}
}

// modelJobLine is the fetch in flight, or the last one that did not finish, in one line.
func modelJobLine(j localmodel.Job) string {
	switch j.State {
	case localmodel.JobFetching:
		line := fmt.Sprintf("fetching %s %s: %s of %s", j.Repo, j.File, provision.BytesWord(j.Done), provision.BytesWord(j.Total))
		if j.Rate > 0 {
			line += fmt.Sprintf(" · %s/s", provision.BytesWord(int64(j.Rate)))
		}
		return line
	case localmodel.JobFailed:
		return fmt.Sprintf("the last fetch (%s %s) failed: %s", j.Repo, j.File, j.Err)
	case localmodel.JobInterrupted:
		return fmt.Sprintf("the last fetch (%s %s) was interrupted at %s of %s - run it again and it resumes",
			j.Repo, j.File, provision.BytesWord(j.Done), provision.BytesWord(j.Total))
	}
	return ""
}

// modelOnNode is `roger model ... --on <node>`: the same request, carried to a member under a
// serve-control grant. The member judges it; this machine only asks and reports.
func modelOnNode(cfg config, verb string, a modelArgv) error {
	task := edge.ModelTask{Ctx: a.ctx}
	switch verb {
	case "serve":
		if len(a.pos) != 1 {
			return errors.New("usage: roger model serve <owner/repo:QUANT | id on that machine> --on <node> [--ctx N]")
		}
		task.Action, task.Model = edge.ModelServe, a.pos[0]
	case "status", "list", "ls":
		task.Action = edge.ModelStatus
	case "stop":
		task.Action = edge.ModelStop
	default:
		return fmt.Errorf("roger model %s works on this machine only; on a member: serve, status, stop", verb)
	}
	res, name, err := modelEdgeInvoke(cfg, a.on, task)
	if err != nil {
		return err
	}
	if task.Action != edge.ModelStatus {
		fmt.Fprintf(modelOut, "%s: %s\n", name, res.Says)
	}
	fmt.Fprintf(modelOut, "%s %s\n", name, edge.ModelLine(res.Now))
	return nil
}

// modelEdgeInvoke is the seam a test drives instead of a live member.
var modelEdgeInvoke = edgeModelInvoke
