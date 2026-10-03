package webui

// THE MODELS PAGE (features/serving/local_models.feature §8): the console does what `roger model`
// does, through the same code, and nothing more. Every hook is filled by cmd/rogerai from the code
// behind the CLI, so the browser cannot judge, fetch or serve differently from the terminal.
//
// What the browser may send is deliberately narrow: a model id roger already knows, or a repo and a
// file name that the hook re-checks against the index's own listing before a byte moves. There is no
// field for a URL, a path or a runtime. A fetch, a switch and a removal each carry confirm:true,
// which the page sets only after the owner has seen the verdict and said yes.

import (
	"net/http"
	"strconv"
	"strings"

	"rogerai.fm/roger/v6/internal/modelview"
)

// The shapes live in internal/modelview so the TUI draws from the same hooks as this page.
type (
	ModelsView = modelview.View
	ModelRow   = modelview.Row
	Serving    = modelview.Serving
	FetchJob   = modelview.FetchJob
	RepoRow    = modelview.RepoRow
	FilesView  = modelview.FilesView
	FileChoice = modelview.FileChoice
	ModelHooks = modelview.Hooks

	ServingSettings = modelview.ServingSettings
)

func (s *Server) modelRoutes() {
	s.mux.HandleFunc("/api/models", s.auth(s.handleModels))
	s.mux.HandleFunc("/api/models/search", s.auth(s.handleModelSearch))
	s.mux.HandleFunc("/api/models/files", s.auth(s.handleModelFiles))
	s.mux.HandleFunc("/api/models/fetch", s.action(s.handleModelFetch))
	s.mux.HandleFunc("/api/models/serve", s.action(s.handleModelServe))
	s.mux.HandleFunc("/api/models/stop", s.action(s.handleModelStop))
	s.mux.HandleFunc("/api/models/remove", s.action(s.handleModelRemove))
	s.mux.HandleFunc("/api/models/set", s.auth(s.handleModelSet))
}

const noModels = "this build cannot manage models from the console - use `roger model`"

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Models
	if h.View == nil {
		http.Error(w, noModels, http.StatusNotImplemented)
		return
	}
	v, err := h.View()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if v.Models == nil {
		v.Models = []ModelRow{}
	}
	writeJSON(w, v)
}

func (s *Server) handleModelSearch(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Models
	if h.Search == nil {
		http.Error(w, noModels, http.StatusNotImplemented)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 200 {
		http.Error(w, "that search is too long", http.StatusBadRequest)
		return
	}
	rows, err := h.Search(r.Context(), q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if rows == nil {
		rows = []RepoRow{}
	}
	writeJSON(w, map[string]any{"source": "huggingface", "results": rows})
}

func (s *Server) handleModelFiles(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Models
	if h.Files == nil {
		http.Error(w, noModels, http.StatusNotImplemented)
		return
	}
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	ctxLen, _ := strconv.Atoi(r.URL.Query().Get("ctx"))
	if repo == "" || ctxLen < 0 {
		http.Error(w, "repo required", http.StatusBadRequest)
		return
	}
	v, err := h.Files(r.Context(), repo, ctxLen)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, v)
}

// confirmed is the console's half of the consent rule: the page sets it only after the owner
// read the verdict and said yes. A request without it changes nothing.
type confirmed struct {
	Confirm bool `json:"confirm"`
}

func (s *Server) handleModelFetch(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Models
	if h.Fetch == nil {
		http.Error(w, noModels, http.StatusNotImplemented)
		return
	}
	var req struct {
		confirmed
		Repo string `json:"repo"`
		File string `json:"file"`
	}
	if !decode(r, &req) || req.Repo == "" || req.File == "" {
		http.Error(w, "repo and file required", http.StatusBadRequest)
		return
	}
	if !req.Confirm {
		http.Error(w, "a fetch needs the owner's yes", http.StatusPreconditionRequired)
		return
	}
	if err := h.Fetch(req.Repo, req.File); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.writeModels(w)
}

func (s *Server) handleModelServe(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Models
	if h.Serve == nil {
		http.Error(w, noModels, http.StatusNotImplemented)
		return
	}
	var req struct {
		confirmed
		ID  string `json:"id"`
		Ctx int    `json:"ctx"`
		Add bool   `json:"add"`
	}
	if !decode(r, &req) || req.ID == "" || req.Ctx < 0 {
		http.Error(w, "model id required", http.StatusBadRequest)
		return
	}
	if !req.Confirm {
		http.Error(w, "serving needs the owner's yes", http.StatusPreconditionRequired)
		return
	}
	says, err := h.Serve(req.ID, req.Ctx, req.Add)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.writeModelsSaying(w, says)
}

func (s *Server) handleModelStop(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Models
	if h.Stop == nil {
		http.Error(w, noModels, http.StatusNotImplemented)
		return
	}
	var req struct {
		confirmed
		ID string `json:"id"` // "" stops everything roger serves
	}
	if !decode(r, &req) || !req.Confirm {
		http.Error(w, "stopping needs the owner's yes", http.StatusPreconditionRequired)
		return
	}
	says, err := h.Stop(req.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.writeModelsSaying(w, says)
}

func (s *Server) handleModelRemove(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Models
	if h.Remove == nil {
		http.Error(w, noModels, http.StatusNotImplemented)
		return
	}
	var req struct {
		confirmed
		ID string `json:"id"`
	}
	if !decode(r, &req) || req.ID == "" {
		http.Error(w, "model id required", http.StatusBadRequest)
		return
	}
	if !req.Confirm {
		http.Error(w, "removing needs the owner's yes", http.StatusPreconditionRequired)
		return
	}
	says, err := h.Remove(req.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.writeModelsSaying(w, says)
}

func (s *Server) writeModels(w http.ResponseWriter) { s.writeModelsSaying(w, "") }

// writeModelsSaying answers an action with the fresh page state, so the page shows what it did.
func (s *Server) writeModelsSaying(w http.ResponseWriter, says string) {
	v := ModelsView{Models: []ModelRow{}}
	if s.opts.Models.View != nil {
		if got, err := s.opts.Models.View(); err == nil {
			v = got
		}
	}
	if v.Models == nil {
		v.Models = []ModelRow{}
	}
	writeJSON(w, map[string]any{"says": says, "view": v})
}

// handleModelSet changes one serving setting (local_models.feature §11) with the CLI's own sentence:
// a POST carrying the owner's yes, like every other change from this page.
func (s *Server) handleModelSet(w http.ResponseWriter, r *http.Request) {
	h := s.opts.Models
	if h.Set == nil {
		http.Error(w, noModels, http.StatusNotImplemented)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		confirmed
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if !decode(r, &req) || req.Kind == "" || req.Value == "" {
		http.Error(w, "a setting and its value are required", http.StatusBadRequest)
		return
	}
	if !req.Confirm {
		http.Error(w, "changing a setting needs the owner's yes", http.StatusPreconditionRequired)
		return
	}
	says, err := h.Set(req.Kind, req.Value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeModelsSaying(w, says)
}
