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
	modelHW      = detectLocalHW
	modelHubBase = hfhub.DefaultBase
	modelFetcher = func() *provision.Fetcher { return &provision.Fetcher{OnProgress: modelProgress} }
	modelRuntime = func(ctx context.Context) (exe, tag string, err error) {
		h := runtime.ThisHost()
		if exe, a, ok := runtime.Installed(h, modelHome().Runtime()); ok {
			return exe, a.Tag, nil
		}
		if a, err := runtime.Pick(h); err == nil {
			fmt.Fprintf(modelOut, "this machine has no inference runtime yet: fetching llama.cpp %s (%s), verified before it ever runs.\n",
				a.Tag, provision.BytesWord(a.Bytes))
		}
		exe, a, err := runtime.Install(ctx, h, modelHome().Runtime(), modelFetcher())
		modelProgressDone()
		return exe, a.Tag, err
	}
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
		if s, ok := modelHome().Live(); ok {
			return []detect.Endpoint{{Name: "roger", Base: s.BaseURL, Key: s.Key}}
		}
		return nil
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
			return errors.New("usage: roger model serve <name | owner/repo[:QUANT] | file.gguf> [--ctx N] [--port N] [--detach]")
		}
		return modelServe(ctx, a)
	case "status":
		return modelStatus()
	case "stop":
		return modelStop()
	case "rm", "remove":
		if len(a.pos) != 1 {
			return errors.New("usage: roger model rm <name> [--yes]")
		}
		return modelRemove(a.pos[0], a.yes)
	}
	return fmt.Errorf("unknown: roger model %s (list, search, files, get, serve, status, stop, rm)", verb)
}

func modelList() error {
	h := modelHome()
	all, err := h.List()
	if err != nil {
		return err
	}
	live, serving := h.Live()
	if len(all) == 0 {
		fmt.Fprintf(modelOut, "no models on this machine yet (%s).\n", h.Models())
		fmt.Fprintln(modelOut, "  roger model search <words>   look for one     roger model serve file.gguf   serve one you already have")
		return nil
	}
	used, n, _ := h.Usage()
	fmt.Fprintf(modelOut, "%d models, %s, in %s\n\n", n, provision.BytesWord(used), h.Models())
	for _, m := range all {
		mark := " "
		if serving && live.File == m.File && live.ID == m.ID {
			mark = "▶"
		}
		check := "checked"
		if !m.Verified() {
			check = "found  " // dropped in by hand: served on the owner's word
		}
		fmt.Fprintf(modelOut, "%s %-44s %-8s %9s  %s\n", mark, m.ID, modelDash(m.Quant), provision.BytesWord(m.Bytes), check)
	}
	if serving {
		fmt.Fprintf(modelOut, "\n▶ serving %s as %q since %s ago\n", live.ID, live.Band, edge.Age(time.Since(live.Started)))
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
	b := modelBudget()
	// The runtime first: serving needs it anyway, and once it is here its own fit check answers.
	exe, tag, err := modelRuntime(ctx)
	if err != nil {
		if errors.Is(err, runtime.ErrNoRuntime) {
			return fmt.Errorf("local serving is not available on this machine: %v.\n"+
				"  it can still join an Edge, relay, and be monitored and controlled from the authority", err)
		}
		return fmt.Errorf("could not get the runtime: %w", err)
	}
	ctxLen, says, err := modelPlan(ctx, m, b, a.ctx, exe)
	if err != nil {
		return err
	}
	fmt.Fprintln(modelOut, says)
	h := modelHome()
	prev, hadPrev := h.Live()
	if a.detach {
		return modelServeDetached(h, m, ctxLen, a.port, prev, hadPrev)
	}
	fmt.Fprintf(modelOut, "starting llama.cpp %s on %s at %d context...\n", tag, m.File, ctxLen)
	srv, s, err := h.Serve(ctx, localmodel.ServeOpts{Model: m, Exe: exe, Tag: tag, Ctx: ctxLen, Port: a.port, Budget: b, Env: modelServeEnv})
	if err != nil {
		if hadPrev {
			return fmt.Errorf("%v\n  %s is still serving - nothing was switched", err, prev.ID)
		}
		return err
	}
	defer h.Release(os.Getpid())
	defer func() { _ = srv.Stop(context.Background()) }()
	// The new one answers; only now is the old one stopped.
	if hadPrev && prev.Supervisor != os.Getpid() {
		says, _ := modelSignalStop(prev)
		fmt.Fprintf(modelOut, "switched from %s: %s\n", prev.ID, says)
	}
	fmt.Fprintf(modelOut, "on the air locally: %q - %s, %s, sha256 %s, %d context\n", s.Band, s.ID, modelDash(s.Quant),
		modelDash(provision.ShortDigest(s.SHA256)), s.Ctx)
	fmt.Fprintf(modelOut, "  %s (key in %s)\n", s.BaseURL, filepath.Join(h.Dir, "serving.json"))
	fmt.Fprintln(modelOut, "  roger share picks it up like any local server. ctrl-c or `roger model stop` ends it.")
	modelUntilDone(ctx)
	fmt.Fprintf(modelOut, "stopping %s.\n", s.Band)
	return nil
}

// modelServeDetached hands the serve to a roger of its own and waits until it is really answering.
// Everything that can be refused - the fit, the runtime, the consent to fetch - was already settled
// here, in front of the owner, so the background has nothing left to ask.
func modelServeDetached(h localmodel.Home, m localmodel.Model, ctxLen, port int, prev localmodel.Serving, hadPrev bool) error {
	ref := m.Path
	if _, err := h.Find(m.ID); err == nil {
		ref = m.ID
	}
	args := []string{"model", "serve", ref, "--yes", "--ctx", strconv.Itoa(ctxLen)}
	if port > 0 {
		args = append(args, "--port", strconv.Itoa(port))
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
		if s, ok := h.Live(); ok && s.File == m.File && (!hadPrev || s.Supervisor != prev.Supervisor) {
			fmt.Fprintf(modelOut, "on the air locally: %q - %s, %d context, at %s\n", s.Band, s.ID, s.Ctx, s.BaseURL)
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

// modelInstalledExe is the runtime's server when it is already on this machine, else "".
func modelInstalledExe() string {
	exe, _, ok := runtime.Installed(modelHost(), modelHome().Runtime())
	if !ok {
		return ""
	}
	return exe
}

// modelPlan settles the context a model will serve at, or refuses with the arithmetic - never
// silently reducing what the owner asked for. says is the line to show the owner either way. With
// the runtime here (serverExe), its own fit check answers and roger's estimate steps aside.
func modelPlan(ctx context.Context, m localmodel.Model, b localmodel.Budget, want int, serverExe string) (ctxLen int, says string, err error) {
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

func modelStatus() error {
	if j, ok := modelHome().Job(); ok && j.State != localmodel.JobDone {
		fmt.Fprintln(modelOut, modelJobLine(j))
	}
	s, ok := modelHome().Live()
	if !ok {
		fmt.Fprintln(modelOut, "nothing is being served by roger on this machine.")
		return nil
	}
	fmt.Fprintf(modelOut, "serving %q\n", s.Band)
	fmt.Fprintf(modelOut, "  model    %s\n  file     %s\n  quant    %s\n  sha256   %s\n  context  %d\n",
		s.ID, s.File, modelDash(s.Quant), modelDash(s.SHA256), s.Ctx)
	fmt.Fprintf(modelOut, "  runtime  llama.cpp %s, pid %d, for %s\n  address  %s\n",
		s.Runtime, s.RuntimePID, edge.Age(time.Since(s.Started)), s.BaseURL)
	return nil
}

func modelStop() error {
	s, ok := modelHome().Live()
	if !ok {
		fmt.Fprintln(modelOut, "nothing is being served by roger on this machine - nothing to stop.")
		return nil
	}
	says, err := modelSignalStop(s)
	if err != nil {
		return err
	}
	fmt.Fprintln(modelOut, says)
	return nil
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
		if cur, ok := modelHome().Live(); !ok || cur.Supervisor != s.Supervisor {
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
	if s, ok := h.Live(); ok && s.ID == m.ID && s.File == m.File {
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
	fmt.Fprintf(modelOut, "\r%-72s", line)
	modelProgressShown = true
}

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
