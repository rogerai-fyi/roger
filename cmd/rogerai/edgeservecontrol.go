package main

// SERVE CONTROL (features/edge/remote_model.feature): changing what a member serves, from another
// machine on the Edge.
//
//	roger edge serve-control [on|off]          the owner's switch, ON THIS MACHINE (off by default)
//	roger model serve <id> --on <node>         ask a member to serve a model
//	roger model status --on <node>             what it serves and fetches
//	roger model stop --on <node>               ask it to stop
//
// The command differs from the local one only by naming the node. The member does everything with
// its OWN judgement: it resolves the id against its own disk or its own source, measures its own
// memory, refuses with its own arithmetic, and keeps the old model serving until the new one answers
// (the background `roger model serve` it hands off to proves before it switches).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/catalog"
	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/localmodel"
	"rogerai.fm/roger/v6/internal/provision"
	"rogerai.fm/roger/v6/internal/store"
)

func edgeServeControlOn(cfg config) bool { return cfg.EdgeServeControl }

func cmdEdgeServeControl(cfg config, args []string) error {
	leaf, _ := edgeLeaf("serve-control")
	argv, err := parseEdgeArgv(leaf, args)
	if err != nil {
		return err
	}
	if len(argv.pos) == 0 {
		if edgeServeControlOn(cfg) {
			fmt.Println("serve-control: ON for this machine.")
			edgePrintServeControlGrants()
		} else {
			fmt.Println("serve-control: OFF for this machine (the default). No member can change what it serves, grant or not.")
		}
		fmt.Println("\n  roger edge serve-control on | off")
		return nil
	}
	on, ok := edgeParseOnOff(argv.pos[0])
	if !ok {
		return usagef("usage: roger edge serve-control %s", leaf.args)
	}
	cfg.EdgeServeControl = on
	if err := saveConfig(cfg); err != nil {
		return err
	}
	if on {
		fmt.Println("serve-control is ON for this machine.")
		edgePrintServeControlGrants()
		return nil
	}
	fmt.Println("serve-control is OFF for this machine. No member can change what it serves, grant or not.")
	return nil
}

// edgePrintServeControlGrants says everything the switch grants, and what it never does.
func edgePrintServeControlGrants() {
	fmt.Println("  a member holding a grant scoped to serve-control on this node can: SERVE a model that is on")
	fmt.Println("  this disk, FETCH one it names exactly as owner/repo:QUANT from this machine's own source,")
	fmt.Println("  STOP serving, and ask what is served. This machine judges every request against its OWN")
	fmt.Println("  memory and refuses what will not fit. It never takes an address, a path or a program.")
}

// edgeModelNow is this machine's own serving and fetch, for its reports and for a status answer.
func edgeModelNow() *store.EdgeModel {
	h := modelHome()
	out := &store.EdgeModel{}
	any := false
	if s, ok := h.Live(); ok {
		out.Band, out.ID, out.Quant, out.Ctx, out.Since = s.Band, s.ID, s.Quant, s.Ctx, s.Started.Unix()
		any = true
	}
	if j, ok := h.Job(); ok && (j.State != localmodel.JobDone || time.Since(j.Updated) < time.Minute) {
		out.Fetch = &store.EdgeFetch{Repo: j.Repo, File: j.File, Done: j.Done, Total: j.Total, Rate: j.Rate, State: j.State, Err: j.Err}
		any = true
	}
	if word, ok := modelRuntimeWord(); !ok {
		out.CannotServe = word
		any = true
	}
	if out.Band == "" {
		// A serve asked for from afar that never came on the air: its own last words reach the owner.
		if lines := modelRecentServeLog(h); len(lines) > 0 {
			out.ServeFailed = lines[len(lines)-1]
			any = true
		}
	}
	if all, err := h.List(); err == nil && len(all) > 0 {
		for _, m := range all {
			if len(out.Have) == edge.MaxHave {
				break
			}
			out.Have = append(out.Have, m.ID)
		}
		any = true
	}
	if !any {
		return nil
	}
	return out
}

// edgeModelRun is the MEMBER side: the whole request, after the face authenticated the caller and
// checked the grant. It returns the result for the wire, or the sentence it is refused with.
func edgeModelRun(cfg config, callerID, callerName string, raw json.RawMessage, now time.Time) (edge.ModelResult, string) {
	if !edgeServeControlOn(cfg) {
		return edge.ModelResult{}, edge.ServeControlOffReason
	}
	task, refused := edge.ParseModelTask(raw)
	if refused != "" {
		return edge.ModelResult{}, refused
	}
	res := edge.ModelResult{Action: task.Action, Model: task.Model}
	switch task.Action {
	case edge.ModelStatus:
		res.Now, res.Says = edgeModelNow(), "what this machine serves and fetches now"
		return res, ""
	case edge.ModelStop:
		s, ok := modelHome().Live()
		if !ok {
			res.Says, res.Now = "nothing is being served here - nothing to stop.", edgeModelNow()
			return res, ""
		}
		if err := edgeRecordModel(callerID, callerName, "stop serving "+s.Band); err != nil {
			return res, edgeUnrecorded(err)
		}
		says, err := modelSignalStop(s)
		if err != nil {
			return res, err.Error()
		}
		res.Says, res.Changed = says, true
		res.Now = edgeModelNow()
		return res, ""
	}
	if err := edgeRecordModel(callerID, callerName, "serve "+task.Model); err != nil {
		return res, edgeUnrecorded(err)
	}
	says, why := edgeModelServe(task)
	if why != "" {
		return res, why
	}
	res.Says, res.Changed = says, true
	res.Now = edgeModelNow()
	return res, ""
}

// edgeModelServe serves a model this machine has, or fetches one named by an exact index id first.
// Everything is judged HERE, against this machine's own measurements.
func edgeModelServe(task edge.ModelTask) (string, string) {
	if word, ok := modelRuntimeWord(); !ok {
		return "", word
	}
	h := modelHome()
	all, err := h.List()
	if err != nil {
		return "", err.Error()
	}
	for _, m := range all {
		if m.ID == task.Model {
			// Over the Edge there is no owner's word to serve on: a model this machine cannot judge
			// is refused, exactly as the fetch path refuses one (remote_model.feature: the target decides).
			if _, err := detect.ReadShape(m.Path); err != nil && !errors.Is(err, detect.ErrNotGenerative) {
				return "", "this machine cannot judge " + m.ID + " (" + err.Error() + "), so it will not serve it unasked"
			}
			ctxLen, says, err := modelPlan(m, modelBudgetNow(), task.Ctx)
			if err != nil {
				return "", err.Error()
			}
			if err := modelSpawn([]string{"model", "serve", m.ID, "--yes", "--ctx", fmt.Sprint(ctxLen)},
				filepath.Join(h.Dir, "serve.log")); err != nil {
				recordServeFailure(h, err)
				return "", "could not start serving: " + err.Error()
			}
			return says + " - starting; it serves once proven" + edgeStillServing(h), ""
		}
	}
	repo, quant, ok := edge.ExactIndexID(task.Model)
	if !ok {
		return "", fmt.Sprintf("this machine does not have %q, and it searches nowhere for a model - "+
			"name it exactly as owner/repo:QUANT to have it fetched from this machine's own source", task.Model)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	hub := modelHub()
	files, err := hub.Files(ctx, repo)
	if err != nil {
		return "", modelSourceErr(err).Error()
	}
	var f *localmodel.Choice
	for _, c := range localmodel.Rank(files, catalog.Shape{}, localmodel.Budget{}, 0) {
		if strings.EqualFold(c.File.Quant, quant) && c.Unservable == "" {
			if f != nil {
				return "", fmt.Sprintf("%s has more than one %s file; name the model on that machine with `roger model files %s`", repo, quant, repo)
			}
			c := c
			f = &c
		}
	}
	if f == nil {
		return "", fmt.Sprintf("%s has no servable %s file", repo, quant)
	}
	shape, err := hub.ShapeOf(ctx, repo, f.File.Path, f.File.Bytes)
	if err != nil {
		return "", "this machine could not read that model's header, so it cannot judge it: " + err.Error()
	}
	v := localmodel.Judge(shape, modelBudgetNow(), task.Ctx)
	switch v.Fit {
	case catalog.FitWontFit:
		msg := "this machine's own arithmetic: " + v.Line()
		if s := v.Sum(); s != "" {
			msg += " (" + s + ")"
		}
		return "", msg
	case catalog.FitUnknown:
		return "", "this machine cannot judge it, so it will not fetch it unasked: " + v.Line()
	}
	ctxLen := v.Ctx
	err = h.StartFetch(hub, provision.Fetcher{}, repo, f.File, modelLicense(ctx, repo), func(m localmodel.Model, err error) {
		if err == nil {
			if serr := modelSpawn([]string{"model", "serve", m.ID, "--yes", "--ctx", fmt.Sprint(ctxLen)}, filepath.Join(h.Dir, "serve.log")); serr != nil {
				recordServeFailure(h, serr) // the owner who asked from afar reads it in the member's report
			}
		}
	})
	if err != nil {
		return "", err.Error()
	}
	return fmt.Sprintf("%s - fetching %s (%s); it serves once fetched, checked and proven%s",
		v.Line(), f.File.Path, provision.BytesWord(f.File.Bytes), edgeStillServing(h)), ""
}

func edgeStillServing(h localmodel.Home) string {
	if s, ok := h.Live(); ok {
		return ", and " + s.ID + " keeps serving until then"
	}
	return ""
}

// edgeRecordModel puts a remote model request on THIS machine's own history BEFORE it is acted on,
// naming the caller by name and by node id (a name is not unique and can change). A request that
// cannot be recorded is not acted on: an unaudited change to what a machine runs is worse than none.
func edgeRecordModel(callerID, callerName, what string) error {
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	id, _, ok, err := edgeIdentityStore().LoadIdentity()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("this machine has no Edge identity to record against")
	}
	who := callerName
	if who == "" {
		who = edgeDisplayName(callerID)
	}
	if err := st.fleet.RecordEvent(id.NodeID, "model", what+", asked by "+who+" ("+edgeShortID(callerID)+")"); err != nil {
		return err
	}
	return st.save()
}

// edgeUnrecorded is the refusal when the audit record could not be written.
func edgeUnrecorded(err error) string {
	return "this machine could not record the request on its history, so it did not act on it: " + err.Error()
}

// edgeModelInvoke is the CALLER side: find the member's face, a live serve-control grant, and ask.
func edgeModelInvoke(cfg config, node string, task edge.ModelTask) (edge.ModelResult, string, error) {
	var out edge.ModelResult
	b, _ := json.Marshal(task)
	if _, refused := edge.ParseModelTask(b); refused != "" {
		return out, node, fmt.Errorf("%s", refused) // refused here, before anything is sent
	}
	peer, calleeID, err := edgeResolveNodeFace(node)
	if err != nil {
		return out, node, err
	}
	name := edgeDisplayName(calleeID)
	callerID := edgeThisNodeID()
	grants, err := edgeGrantStore().List()
	if err != nil {
		return out, name, err
	}
	g, ok := edgeSelectGrant(grants, callerID, calleeID, string(edge.ServeControl), time.Now())
	if !ok {
		return out, name, fmt.Errorf("no live grant lets %s change what %s serves - issue one on the authority with `roger edge grant self serve-control %s`",
			edgeGrantWho(callerID), name, name)
	}
	cert := edgeCallerCert()
	if cert == nil {
		return out, name, fmt.Errorf("this machine has no Edge certificate to act with - run `roger edge enroll` first")
	}
	res, _, err := edgeDialInvokeTask(context.Background(), peer, cert, g, string(edge.ServeControl), b)
	if err != nil {
		return out, name, err
	}
	if res.Refused != "" {
		return out, name, fmt.Errorf("%s refused: %s", name, res.Refused)
	}
	if json.Unmarshal(res.Output, &out) != nil {
		return out, name, fmt.Errorf("%s answered with something that is not a serve-control result", name)
	}
	return out, name, nil
}

// edgeServeModelHook is the screens' way to ask a member to serve a model: the CLI's own request.
func edgeServeModelHook(node, id string) (string, error) {
	res, _, err := edgeModelInvoke(loadConfig(), node, edge.ModelTask{Action: edge.ModelServe, Model: id})
	if err != nil {
		return "", err
	}
	return res.Says, nil
}

// recordServeFailure writes a serve that could not even be started where the member's report reads
// a failed serve's last words, so the owner who asked from afar is told why.
func recordServeFailure(h localmodel.Home, err error) {
	_ = os.MkdirAll(h.Dir, 0o700)
	_ = os.WriteFile(filepath.Join(h.Dir, "serve.log"), []byte("error: could not start serving: "+err.Error()+"\n"), 0o600)
}
