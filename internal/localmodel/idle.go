package localmodel

// NOTHING IS UNLOADED BY SURPRISE (local_models.feature §5). Off by default; when the owner sets an
// idle timeout and it fires, the band is withdrawn and the stop recorded with its reason.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Settings are the owner's choices for roger's own serving on this machine. Zero means unset.
type Settings struct {
	IdleAfter  time.Duration `json:"idle_after,omitempty"`  // 0: never unload for idleness
	DiskBudget int64         `json:"disk_budget,omitempty"` // bytes for models; 0: no budget, nothing is ever evicted
	Runtime    string        `json:"runtime,omitempty"`     // auto (unset), cpu, gpu, vulkan or cuda (local_models.feature §10)
}

func (h Home) settingsPath() string { return filepath.Join(h.Dir, "settings.json") }

// Settings reads the owner's choices. A damaged file is an error, never read as "unset".
func (h Home) Settings() (Settings, error) {
	var s Settings
	b, err := os.ReadFile(h.settingsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("%s is damaged: %w", h.settingsPath(), err)
	}
	return s, nil
}

// SetSettings records the owner's choices.
func (h Home) SetSettings(s Settings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(h, h.settingsPath(), b)
}

func writeFileAtomic(h Home, path string, b []byte) error {
	if err := os.MkdirAll(h.Dir, 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// IdleWatch decides idleness from the runtime's own token counters: idle is After with the count
// standing still and nothing in flight.
type IdleWatch struct {
	After time.Duration
	Since time.Time // when the count last moved, or work was last in flight
	last  int64
	seen  bool
}

// Observe takes one reading and reports whether the timeout has fired.
func (w *IdleWatch) Observe(now time.Time, total int64, busy bool) bool {
	if !w.seen || total != w.last || busy {
		w.seen, w.last, w.Since = true, total, now
		return false
	}
	return w.After > 0 && now.Sub(w.Since) >= w.After
}

// Activity reads the runtime's counters: tokens taken in and written, and whether a request is in
// flight. It needs the runtime's key, like every other caller.
func Activity(ctx context.Context, s Serving) (total int64, busy bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(s.BaseURL, "/v1")+"/metrics", nil)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false, fmt.Errorf("the runtime's counters answered %s", resp.Status)
	}
	sc := bufio.NewScanner(resp.Body)
	found := false
	for sc.Scan() {
		name, val, ok := strings.Cut(sc.Text(), " ")
		if !ok || strings.HasPrefix(name, "#") {
			continue
		}
		v, perr := strconv.ParseFloat(strings.TrimSpace(val), 64)
		if perr != nil {
			continue
		}
		switch name {
		case "llamacpp:prompt_tokens_total", "llamacpp:tokens_predicted_total":
			total += int64(v)
			found = true
		case "llamacpp:requests_processing":
			busy = v > 0
		}
	}
	if !found {
		return 0, false, errors.New("the runtime's counters carried no token totals")
	}
	return total, busy, sc.Err()
}

// Stop is why roger stopped serving a band on its own, kept so the owner and a peer can read it later.
type Stop struct {
	Band string    `json:"band"`
	At   time.Time `json:"at"`
	Why  string    `json:"why"`
}

// IdleWhy is the reason an idle stop gives, with the way to undo it.
func IdleWhy(after time.Duration) string {
	return fmt.Sprintf("no turn for %s - the owner's idle timeout (roger model idle off keeps it serving)", after)
}

// Line is the stop in one sentence.
func (s Stop) Line() string {
	return fmt.Sprintf("%s stopped %s: %s", s.Band, s.At.UTC().Format("2006-01-02 15:04 UTC"), s.Why)
}

func (h Home) stopPath() string { return filepath.Join(h.Dir, "stopped.json") }

// Stops is the last stop of each band roger stopped on its own, oldest first.
func (h Home) Stops() []Stop {
	var all []Stop
	b, err := os.ReadFile(h.stopPath())
	if err != nil || json.Unmarshal(b, &all) != nil {
		return nil
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].At.Before(all[j].At) })
	return all
}

func (h Home) writeStops(all []Stop) error {
	if len(all) == 0 {
		_ = os.Remove(h.stopPath())
		return nil
	}
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(h, h.stopPath(), b)
}

// RecordStop keeps a band's stop, replacing that band's older one and no other.
func (h Home) RecordStop(s Stop) error {
	all := []Stop{s}
	for _, o := range h.Stops() {
		if o.Band != s.Band {
			all = append(all, o)
		}
	}
	return h.writeStops(all)
}

// ClearStop forgets a band's stop once it is served again and the stop is history.
func (h Home) ClearStop(band string) {
	var keep []Stop
	for _, o := range h.Stops() {
		if o.Band != band {
			keep = append(keep, o)
		}
	}
	_ = h.writeStops(keep)
}

// LastStop is the most recent stop roger made on its own, if any.
func (h Home) LastStop() (Stop, bool) {
	all := h.Stops()
	if len(all) == 0 {
		return Stop{}, false
	}
	return all[len(all)-1], true
}
