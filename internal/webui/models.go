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
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ModelsView is the page's state: the machine, what is on disk, what is served, what is fetching.
type ModelsView struct {
	Budget    int64      `json:"budget"`               // bytes, 0 when unreadable
	Pool      string     `json:"pool,omitempty"`       // "system memory", "video memory across 4 GPUs"
	BudgetWhy string     `json:"budget_why,omitempty"` // the reading that failed
	Runtime   string     `json:"runtime"`              // installed, will be fetched, or not available and why
	CanServe  bool       `json:"can_serve"`
	Models    []ModelRow `json:"models"`
	DiskUsed  int64      `json:"disk_used"`
	Dir       string     `json:"dir"`
	Serving   *Serving   `json:"serving,omitempty"`
	Job       *FetchJob  `json:"job,omitempty"`
	ServeLog  []string   `json:"serve_log,omitempty"` // a background serve that never came on air, in its own words
}

// ModelRow is one model on this disk.
type ModelRow struct {
	ID       string `json:"id"`
	Repo     string `json:"repo,omitempty"`
	File     string `json:"file"`
	Bytes    int64  `json:"bytes"`
	Quant    string `json:"quant,omitempty"`
	Band     string `json:"band"`
	License  string `json:"license,omitempty"`
	Verified bool   `json:"verified"`
	Serving  bool   `json:"serving"`
}

// Serving is what is on the air, said in full.
type Serving struct {
	Band    string    `json:"band"`
	ID      string    `json:"id"`
	File    string    `json:"file"`
	Quant   string    `json:"quant,omitempty"`
	SHA256  string    `json:"sha256,omitempty"`
	Ctx     int       `json:"ctx"`
	Runtime string    `json:"runtime"`
	Since   time.Time `json:"since"`
	Address string    `json:"address"`
}

// FetchJob is the fetch in flight, or the last one that did not finish.
type FetchJob struct {
	Repo       string  `json:"repo"`
	File       string  `json:"file"`
	Total      int64   `json:"total"`
	Done       int64   `json:"done"`
	Rate       float64 `json:"rate"`
	RemainingS int64   `json:"remaining_s,omitempty"`
	Resumed    bool    `json:"resumed,omitempty"`
	State      string  `json:"state"`
	Err        string  `json:"err,omitempty"`
	Fix        string  `json:"fix,omitempty"`
	ID         string  `json:"id,omitempty"`
}

// RepoRow is one search result: the index's word, marked as such on the page.
type RepoRow struct {
	Repo      string `json:"repo"`
	Downloads int    `json:"downloads"`
	License   string `json:"license,omitempty"`
	Gated     bool   `json:"gated"`
}

// FilesView is one repo judged against this machine.
type FilesView struct {
	Repo    string       `json:"repo"`
	License string       `json:"license,omitempty"`
	Budget  int64        `json:"budget"`
	Pool    string       `json:"pool,omitempty"`
	Ctx     int          `json:"ctx"`
	Choices []FileChoice `json:"choices"`
}

// FileChoice is one file of a repo with its own verdict.
type FileChoice struct {
	Label      string `json:"label"`
	File       string `json:"file"`
	Bytes      int64  `json:"bytes"`
	Quant      string `json:"quant,omitempty"`
	Fit        string `json:"fit"`   // fits | tight | will not fit | unknown
	Short      string `json:"short"` // the verdict for a row
	Line       string `json:"line"`  // the verdict in a sentence
	Sum        string `json:"sum,omitempty"`
	Unservable string `json:"unservable,omitempty"`
	Suggested  bool   `json:"suggested"`
	Have       bool   `json:"have"` // already on this disk
}

// ModelHooks are filled by cmd/rogerai from the code behind `roger model`. A nil hook means this
// build cannot do that from the console, and the page says so rather than offering it.
type ModelHooks struct {
	View   func() (ModelsView, error)
	Search func(ctx context.Context, q string) ([]RepoRow, error)
	Files  func(ctx context.Context, repo string, ctxLen int) (FilesView, error)
	Fetch  func(repo, file string) error
	Serve  func(id string, ctxLen int) (string, error)
	Stop   func() (string, error)
	Remove func(id string) (string, error)
}

func (s *Server) modelRoutes() {
	s.mux.HandleFunc("/api/models", s.auth(s.handleModels))
	s.mux.HandleFunc("/api/models/search", s.auth(s.handleModelSearch))
	s.mux.HandleFunc("/api/models/files", s.auth(s.handleModelFiles))
	s.mux.HandleFunc("/api/models/fetch", s.action(s.handleModelFetch))
	s.mux.HandleFunc("/api/models/serve", s.action(s.handleModelServe))
	s.mux.HandleFunc("/api/models/stop", s.action(s.handleModelStop))
	s.mux.HandleFunc("/api/models/remove", s.action(s.handleModelRemove))
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
	}
	if !decode(r, &req) || req.ID == "" || req.Ctx < 0 {
		http.Error(w, "model id required", http.StatusBadRequest)
		return
	}
	if !req.Confirm {
		http.Error(w, "serving needs the owner's yes", http.StatusPreconditionRequired)
		return
	}
	says, err := h.Serve(req.ID, req.Ctx)
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
	var req confirmed
	if !decode(r, &req) || !req.Confirm {
		http.Error(w, "stopping needs the owner's yes", http.StatusPreconditionRequired)
		return
	}
	says, err := h.Stop()
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
