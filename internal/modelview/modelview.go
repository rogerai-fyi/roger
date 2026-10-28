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
	Budget    int64           `json:"budget"`               // bytes, 0 when unreadable
	Pool      string          `json:"pool,omitempty"`       // "system memory", "video memory across 4 GPUs"
	BudgetWhy string          `json:"budget_why,omitempty"` // the reading that failed
	Runtime   string          `json:"runtime"`              // installed, will be fetched, or not available and why
	CanServe  bool            `json:"can_serve"`
	Models    []Row           `json:"models"`
	DiskUsed  int64           `json:"disk_used"`
	Dir       string          `json:"dir"`
	Serving   *Serving        `json:"serving,omitempty"` // the one on the air last: what a switch replaces
	Also      []Serving       `json:"also,omitempty"`    // the others on the air beside it, in the order they started
	Job       *FetchJob       `json:"job,omitempty"`
	ServeLog  []string        `json:"serve_log,omitempty"` // a background serve that never came on air, in its own words
	Settings  ServingSettings `json:"settings"`
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
	Band     string    `json:"band"`
	ID       string    `json:"id"`
	File     string    `json:"file"`
	Quant    string    `json:"quant,omitempty"`
	SHA256   string    `json:"sha256,omitempty"`
	Ctx      int       `json:"ctx"`
	Runtime  string    `json:"runtime"`
	Since    time.Time `json:"since"`
	Address  string    `json:"address"`
	Build    string    `json:"build,omitempty"`          // cpu, vulkan, cuda, metal
	Measured float64   `json:"measured_tok_s,omitempty"` // the speed its runtime measured; 0 before it has answered
	Estimate string    `json:"estimate,omitempty"`       // the estimate, shown until there is a measurement
}

// ServingSettings is the owner's choices for roger's own serving, as SHARE shows them (§11).
type ServingSettings struct {
	Runtime    string   `json:"runtime"`     // auto, cpu, gpu, vulkan or cuda
	Here       string   `json:"here"`        // what that means on THIS machine, in one line
	Idle       string   `json:"idle"`        // "off" or a duration
	DiskBudget int64    `json:"disk_budget"` // bytes; 0 is no budget
	Choices    []string `json:"choices"`
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

// NearbyView is what is served nearby, judged against this machine (features/models/
// short_names_nearby.feature section 7): the model browser opens on it. Every field is computed by
// the same code as `roger model serve` with no name, so no surface can suggest differently.
type NearbyView struct {
	Budget    int64       `json:"budget"`
	Pool      string      `json:"pool,omitempty"`
	BudgetWhy string      `json:"budget_why,omitempty"`
	Fits      []NearbyRow `json:"fits"`  // served nearby and fits this machine
	Other     []NearbyRow `json:"other"` // served nearby, but would not fit or cannot be judged (Why)
}

// NearbyRow is one name served nearby, the exact file it is, and whose word that is.
type NearbyRow struct {
	Name   string `json:"name"`
	Repo   string `json:"repo"`
	File   string `json:"file"`
	Quant  string `json:"quant,omitempty"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	By     string `json:"by"`              // "your Edge member atlas", "a station on the Tower lab (its word)"
	Fit    string `json:"fit,omitempty"`   // fits | tight | will not fit | unknown
	Short  string `json:"short,omitempty"` // the verdict for a row
	Line   string `json:"line,omitempty"`  // the verdict in a sentence
	Why    string `json:"why,omitempty"`   // why it is not offered here
	Ask    string `json:"ask"`             // the consent line, word for word as the terminal asks it
}

// NearbyPick is the exact file the owner said yes to. The fetch re-reads what is nearby and refuses
// if the name no longer maps to these bytes.
type NearbyPick struct {
	Name   string `json:"name"`
	Repo   string `json:"repo"`
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

// Hooks are filled by cmd/rogerai from the code behind `roger model`. A nil hook means this
// build cannot do that from the console, and the page says so rather than offering it.
type Hooks struct {
	View   func() (View, error)
	Search func(ctx context.Context, q string) ([]RepoRow, error)
	Files  func(ctx context.Context, repo string, ctxLen int) (FilesView, error)
	Fetch  func(repo, file string) error
	Serve  func(id string, ctxLen int, add bool) (string, error) // add: beside what is on the air, not instead
	Stop   func(id string) (string, error)                       // "" stops everything roger serves
	Remove func(id string) (string, error)
	Set    func(kind, value string) (string, error) // runtime, idle or budget; nil means the CLI alone sets them

	Nearby      func(ctx context.Context) (NearbyView, error) // what is served nearby, judged here
	FetchNearby func(p NearbyPick) (string, error)            // fetch the exact file a nearby name maps to
}
