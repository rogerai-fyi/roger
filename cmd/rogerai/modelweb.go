package main

// The console's MODELS page, filled from the code behind `roger model` (modelcmd.go). Nothing here
// judges, fetches or serves on its own: it reshapes what the CLI already does into what a page
// draws, so the browser and the terminal cannot disagree about the same model.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/hfhub"
	"rogerai.fm/roger/v6/internal/localmodel"
	"rogerai.fm/roger/v6/internal/provision"
	"rogerai.fm/roger/v6/internal/runtime"
	"rogerai.fm/roger/v6/internal/webui"
)

// modelBudgetEvery bounds how often the page re-measures the machine: the page polls, and reading
// every GPU once a second would make the monitor the load.
const modelBudgetEvery = 30 * time.Second

var modelBudgetCache struct {
	sync.Mutex
	b  localmodel.Budget
	at time.Time
}

func modelBudgetNow() localmodel.Budget {
	modelBudgetCache.Lock()
	defer modelBudgetCache.Unlock()
	if modelBudgetCache.at.IsZero() || time.Since(modelBudgetCache.at) > modelBudgetEvery {
		modelBudgetCache.b, modelBudgetCache.at = modelBudget(), time.Now()
	}
	return modelBudgetCache.b
}

// modelBudget is the pool the runtime roger would start here uses, which is the pool a model has
// to fit in and be read from (local_models.feature §4).
func modelBudget() localmodel.Budget {
	a, _, err := modelPredict()
	return modelBudgetFor(err == nil && a.Offloads)
}

// modelHost is the seam for "which runtime would this machine take".
var modelHost = modelThisHost

// modelRuntimeWord says whether serving can happen here, with which build, and what it costs first.
func modelRuntimeWord() (string, bool) {
	a, _, err := modelPredict()
	if err != nil {
		return "local serving is not available here: " + strings.TrimPrefix(err.Error(), runtime.ErrNoRuntime.Error()+": "), false
	}
	if _, ok := runtime.InstalledBuild(a, modelHome().Runtime()); ok {
		return fmt.Sprintf("llama.cpp %s, the %s build, installed", a.Tag, a.Build), true
	}
	return fmt.Sprintf("llama.cpp %s, the %s build, will be fetched first (%s), verified before it runs",
		a.Tag, a.Build, provision.BytesWord(a.TotalBytes())), true
}

// modelFixFor is the action that fixes a failed fetch, by which failure it was.
func modelFixFor(reason string) string {
	switch reason {
	case "no_space":
		return "free some space, or remove a model you no longer use"
	case "gated":
		return "accept the terms on the model's page, then set HF_TOKEN where roger runs"
	case "corrupt":
		return "nothing was kept; fetch it again for a fresh copy"
	case "unreachable":
		return "check this machine's network; fetching again resumes where it stopped"
	case "rate_limited":
		return "wait a minute, then fetch again; it resumes"
	case "not_found":
		return "the file is gone from the index; pick another"
	}
	return ""
}

func modelWebHooks() webui.ModelHooks {
	return webui.ModelHooks{
		View:   modelWebView,
		Search: modelWebSearch,
		Files:  modelWebFiles,
		Fetch:  modelWebFetch,
		Serve:  modelWebServe,
		Stop:   modelStopSays,
		Remove: modelWebRemove,
	}
}

func modelWebView() (webui.ModelsView, error) {
	h := modelHome()
	all, err := h.List()
	if err != nil {
		return webui.ModelsView{}, err
	}
	b := modelBudgetNow()
	v := webui.ModelsView{Budget: b.Bytes, Pool: b.Pool, BudgetWhy: b.Why, Dir: h.Models(), Models: []webui.ModelRow{}}
	v.Runtime, v.CanServe = modelRuntimeWord()
	lives := h.LiveAll()
	serving := len(lives) > 0
	for _, m := range all {
		v.DiskUsed += m.Bytes
		_, on := h.ServingModel(m)
		v.Models = append(v.Models, webui.ModelRow{ID: m.ID, Repo: m.Repo, File: m.File, Bytes: m.Bytes, Quant: m.Quant,
			Band: m.Band, License: m.License, Verified: m.Verified(), Serving: on})
	}
	for i, live := range lives {
		sv := webui.Serving{Band: live.Band, ID: live.ID, File: live.File, Quant: live.Quant, SHA256: live.SHA256,
			Ctx: live.Ctx, Runtime: live.Runtime, Since: live.Started, Address: live.BaseURL}
		if i == len(lives)-1 {
			v.Serving = &sv
		} else {
			v.Also = append(v.Also, sv)
		}
	}
	if j, ok := h.Job(); ok && (j.State != localmodel.JobDone || time.Since(j.Updated) < time.Minute) {
		v.Job = &webui.FetchJob{Repo: j.Repo, File: j.File, Total: j.Total, Done: j.Done, Rate: j.Rate, RemainingS: j.RemainingS,
			Resumed: j.Resumed, State: j.State, Err: j.Err, Fix: modelFixFor(j.Reason), ID: j.ID}
	}
	if !serving {
		v.ServeLog = modelRecentServeLog(h)
	}
	return v, nil
}

// modelRecentServeLog is a background serve's last words when it never came on the air, so the
// page can say why instead of waiting forever. Old logs are history, not news.
func modelRecentServeLog(h localmodel.Home) []string {
	p := filepath.Join(h.Dir, "serve.log")
	fi, err := os.Stat(p)
	if err != nil || time.Since(fi.ModTime()) > 10*time.Minute {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	// A serve that came on the air said so; whatever it said after (a normal stop) is not a failure.
	if strings.Contains(string(b), "on the air locally") {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var keep []string
	for _, l := range lines {
		if l = strings.TrimSpace(strings.ReplaceAll(l, "\r", "")); l != "" && !strings.HasPrefix(l, "update available") {
			keep = append(keep, l)
		}
	}
	if len(keep) > 6 {
		keep = keep[len(keep)-6:]
	}
	return keep
}

func modelWebSearch(ctx context.Context, q string) ([]webui.RepoRow, error) {
	ms, err := modelHub().Search(ctx, hfhub.Query{Text: q, Limit: 30})
	if err != nil {
		return nil, modelSourceErr(err)
	}
	out := make([]webui.RepoRow, 0, len(ms))
	for _, m := range ms {
		out = append(out, webui.RepoRow{Repo: m.Repo, Downloads: m.Downloads, License: m.License, Gated: m.Gated})
	}
	return out, nil
}

func modelWebFiles(ctx context.Context, repo string, ctxLen int) (webui.FilesView, error) {
	if _, err := modelHome().PathFor(repo, "x.gguf"); err != nil {
		return webui.FilesView{}, err
	}
	cs, b, err := modelRank(ctx, repo, ctxLen)
	if err != nil {
		return webui.FilesView{}, err
	}
	have := map[string]bool{}
	if all, err := modelHome().List(); err == nil {
		for _, m := range all {
			if m.Repo == repo {
				have[m.File] = true
			}
		}
	}
	v := webui.FilesView{Repo: repo, License: modelLicense(ctx, repo), Budget: b.Bytes, Pool: b.Pool, Choices: []webui.FileChoice{}}
	labels, pick := localmodel.Labels(cs), localmodel.Suggest(cs)
	for i, c := range cs {
		fc := webui.FileChoice{Label: labels[i], File: c.File.Path, Bytes: c.File.Bytes, Quant: c.File.Quant,
			Unservable: c.Unservable, Suggested: i == pick, Have: have[c.File.Path]}
		if c.Unservable == "" {
			fc.Fit, fc.Short, fc.Line, fc.Sum = c.Verdict.Fit.String(), c.Verdict.Short(), c.Verdict.Line(), c.Verdict.Sum()
			if fc.PaceLine = c.Verdict.PaceLine(); fc.PaceLine != "" {
				fc.Pace = c.Verdict.Pace.Short()
			}
			if !fc.Have {
				plan, err := modelHome().RoomFor(c.File.Bytes)
				fc.Room = localmodel.RoomLine(plan)
				if err != nil {
					fc.Room = "the budget you set for models cannot make room for it: " + err.Error()
				}
			}
			v.Ctx = c.Verdict.Ctx
		}
		v.Choices = append(v.Choices, fc)
	}
	return v, nil
}

// modelWebFetch re-reads the index's own listing and fetches only a file that is IN it: the page
// names a file, the index supplies everything about it.
func modelWebFetch(repo, file string) error {
	h := modelHome()
	if _, err := h.PathFor(repo, file); err != nil {
		return err
	}
	hub := modelHub()
	files, err := hub.Files(context.Background(), repo)
	if err != nil {
		return modelSourceErr(err)
	}
	for _, f := range files {
		if f.Path == file {
			return h.StartFetch(hub, provision.Fetcher{}, repo, f, modelLicense(context.Background(), repo), nil)
		}
	}
	return fmt.Errorf("%s has no file %q in its listing", repo, file)
}

// modelWebServe settles everything in front of the owner, then hands the serve to a roger of its
// own, so closing the page does not stop it and a switch proves the new model before the old one
// goes (that roger does exactly what `roger model serve` does).
func modelWebServe(id string, ctxLen int, add bool) (string, error) {
	h := modelHome()
	m, err := h.Find(id)
	if err != nil {
		return "", err
	}
	if word, ok := modelRuntimeWord(); !ok {
		return "", errors.New(word + " - it can still be monitored and controlled from your Edge")
	}
	var beside []localmodel.Serving
	if add {
		if beside, err = modelBeside(h, m); err != nil {
			return "", err
		}
	}
	ctxLen, says, err := modelPlan(context.Background(), m, modelBudgetNow(), ctxLen, modelInstalledExe(), beside...)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(h.Dir, 0o700); err != nil {
		return "", err
	}
	args := []string{"model", "serve", m.ID, "--yes", "--ctx", fmt.Sprint(ctxLen)}
	if add {
		args = append(args, "--add")
	}
	if err := modelSpawn(args, filepath.Join(h.Dir, "serve.log")); err != nil {
		return "", fmt.Errorf("could not start serving in the background: %w", err)
	}
	return says + modelFetchFirst() + " - starting in the background; it is on the air once this page shows it serving.", nil
}

func modelWebRemove(id string) (string, error) {
	h := modelHome()
	m, err := h.Find(id)
	if err != nil {
		return "", err
	}
	if _, ok := h.ServingModel(m); ok {
		return "", fmt.Errorf("%s is being served right now - stop it first", m.ID)
	}
	if err := h.Remove(m); err != nil {
		return "", err
	}
	return fmt.Sprintf("removed %s, %s back.", m.ID, provision.BytesWord(m.Bytes)), nil
}
