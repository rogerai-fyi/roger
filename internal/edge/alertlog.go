package edge

// The ALERT LOG (health.feature §5): rules, what is firing, and the history, on this machine's
// disk. With no sink configured this is where alerts live - visible on the fleet surfaces, sent
// nowhere. Files are written whole through a temp+rename so a crash never leaves a half rule set.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AlertLog is the alert store rooted at Dir.
type AlertLog struct{ Dir string }

// alertRulesFile is what the owner set: their own rules, and which defaults they turned off.
type alertRulesFile struct {
	Rules []Rule   `json:"rules"`
	Off   []string `json:"off,omitempty"`
}

func (l AlertLog) path(name string) string { return filepath.Join(l.Dir, name) }

func (l AlertLog) readJSON(name string, v any) error {
	b, err := os.ReadFile(l.path(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(b, v)
}

func (l AlertLog) writeJSON(name string, v any) error {
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := l.path(name + ".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.path(name))
}

func (l AlertLog) rulesFile() (alertRulesFile, error) {
	var f alertRulesFile
	err := l.readJSON("rules.json", &f)
	return f, err
}

// Rules is the rule set in force: the defaults (with the owner's off-switches applied) and then
// the owner's own rules.
func (l AlertLog) Rules() (RuleSet, error) {
	f, err := l.rulesFile()
	if err != nil {
		return nil, err
	}
	out := RuleSet(DefaultRules())
	for _, id := range f.Off {
		out = out.Off(id)
	}
	return append(out, f.Rules...), nil
}

// SetRule stores an owner rule, replacing one with the same id.
func (l AlertLog) SetRule(r Rule) error {
	if err := ValidateRule(r); err != nil {
		return err
	}
	for _, d := range DefaultRules() {
		if d.ID == r.ID {
			return fmt.Errorf("%q is a default rule: turn it off, or add your own under another id", r.ID)
		}
	}
	f, err := l.rulesFile()
	if err != nil {
		return err
	}
	r.Default, r.Off = false, false
	kept := f.Rules[:0]
	for _, x := range f.Rules {
		if x.ID != r.ID {
			kept = append(kept, x)
		}
	}
	f.Rules = append(kept, r)
	return l.writeJSON("rules.json", f)
}

// SetOff turns a rule off or back on. Defaults are only ever turned off, never removed.
func (l AlertLog) SetOff(id string, off bool) error {
	f, err := l.rulesFile()
	if err != nil {
		return err
	}
	found := false
	for i := range f.Rules {
		if f.Rules[i].ID == id {
			f.Rules[i].Off, found = off, true
		}
	}
	if !found {
		isDefault := false
		for _, d := range DefaultRules() {
			isDefault = isDefault || d.ID == id
		}
		if !isDefault {
			return fmt.Errorf("no rule %q", id)
		}
		kept := f.Off[:0]
		for _, x := range f.Off {
			if x != id {
				kept = append(kept, x)
			}
		}
		f.Off = kept
		if off {
			f.Off = append(f.Off, id)
		}
	}
	return l.writeJSON("rules.json", f)
}

// Remove deletes an owner rule. A default cannot be removed - SetOff it.
func (l AlertLog) Remove(id string) error {
	for _, d := range DefaultRules() {
		if d.ID == id {
			return fmt.Errorf("%q is a default rule: `alert off %s` turns it off", id, id)
		}
	}
	f, err := l.rulesFile()
	if err != nil {
		return err
	}
	kept := f.Rules[:0]
	found := false
	for _, x := range f.Rules {
		if x.ID == id {
			found = true
			continue
		}
		kept = append(kept, x)
	}
	if !found {
		return fmt.Errorf("no rule %q", id)
	}
	f.Rules = kept
	return l.writeJSON("rules.json", f)
}

// State is what was firing at the last evaluation.
func (l AlertLog) State() (AlertState, error) {
	st := AlertState{}
	err := l.readJSON("state.json", &st)
	return st, err
}

// Judge is the one way the log advances: under the log's lock it reads what was firing, judges
// the fleet against the rules in force, records the new state and appends each change. The lock
// is what makes a crossing ONE event when the host judges every pass and a CLI read judges
// beside it in another process.
func (l AlertLog) Judge(hs []NodeHealth, now time.Time) (AlertState, []Alert, error) {
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(l.path("lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	if err := flockExclusive(f); err != nil {
		return nil, nil, err
	}
	defer func() { _ = flockUnlock(f) }()
	rules, err := l.Rules()
	if err != nil {
		return nil, nil, err
	}
	prev, err := l.State()
	if err != nil {
		return nil, nil, err
	}
	st, alerts := Evaluate(rules, hs, prev, now)
	return st, alerts, l.Record(st, alerts)
}

// Record stores the new state and appends each alert to the history.
func (l AlertLog) Record(st AlertState, alerts []Alert) error {
	if err := l.writeJSON("state.json", st); err != nil {
		return err
	}
	if len(alerts) == 0 {
		return nil
	}
	f, err := os.OpenFile(l.path("log.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, a := range alerts {
		b, err := json.Marshal(a)
		if err != nil {
			return err
		}
		w.Write(b)
		w.WriteByte('\n')
	}
	return w.Flush()
}

// Recent is the last n alerts, newest last.
func (l AlertLog) Recent(n int) ([]Alert, error) {
	b, err := os.ReadFile(l.path("log.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	var out []Alert
	for _, ln := range lines {
		if ln == "" {
			continue
		}
		var a Alert
		if json.Unmarshal([]byte(ln), &a) == nil {
			out = append(out, a)
		}
	}
	return out, nil
}
