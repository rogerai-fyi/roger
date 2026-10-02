// Package modelview is what a surface needs to draw `roger model`: the shapes and the hooks. The
// console and the TUI both take a Hooks filled by cmd/rogerai from the CLI's own code, so the
// browser, the terminal app and the CLI cannot judge, fetch or serve differently.
package modelview

import (
	"context"
	"time"
)

// View is a surface's state: the machine, what is on disk, what is served, what is fetching.
type View struct {
	Budget    int64     `json:"budget"`               // bytes, 0 when unreadable
	Pool      string    `json:"pool,omitempty"`       // "system memory", "video memory across 4 GPUs"
	BudgetWhy string    `json:"budget_why,omitempty"` // the reading that failed
	Runtime   string    `json:"runtime"`              // installed, will be fetched, or not available and why
	CanServe  bool      `json:"can_serve"`
	Models    []Row     `json:"models"`
	DiskUsed  int64     `json:"disk_used"`
	Dir       string    `json:"dir"`
	Serving   *Serving  `json:"serving,omitempty"`
	Job       *FetchJob `json:"job,omitempty"`
	ServeLog  []string  `json:"serve_log,omitempty"` // a background serve that never came on air, in its own words
}

// Row is one model on this disk.
type Row struct {
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
	Pace       string `json:"pace,omitempty"`      // how fast it would answer, for a row - a claim apart from Fit
	PaceLine   string `json:"pace_line,omitempty"` // the same with its working
	Room       string `json:"room,omitempty"`      // what fetching it removes under the owner's budget, or why it cannot
	Unservable string `json:"unservable,omitempty"`
	Suggested  bool   `json:"suggested"`
	Have       bool   `json:"have"` // already on this disk
}

// Hooks are filled by cmd/rogerai from the code behind `roger model`. A nil hook means this
// build cannot do that from the console, and the page says so rather than offering it.
type Hooks struct {
	View   func() (View, error)
	Search func(ctx context.Context, q string) ([]RepoRow, error)
	Files  func(ctx context.Context, repo string, ctxLen int) (FilesView, error)
	Fetch  func(repo, file string) error
	Serve  func(id string, ctxLen int) (string, error)
	Stop   func() (string, error)
	Remove func(id string) (string, error)
}
