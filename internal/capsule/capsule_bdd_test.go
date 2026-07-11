package capsule

// capsule_bdd_test.go wires godog so the roger.context.v1 behavior specs
// (features/capsule/{canonical,merge,security}.feature) are EXECUTABLE Cucumber tests
// over REAL ed25519 (no mocks), mirroring the proven relay-auth BDD pattern.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

// The golden canonical strings from the brief (the app-verified vector). The two
// producers differ ONLY in the exported_by value.
const (
	goldenCLI = `{"capsule":"roger.context.v1","id":"cap_x","thread":{"origin_thread_id":"t1","title":"Hi","base_watermark":1},"redaction":"full","summary":{"text":"hi","produced_by":"none","as_of_turn":1},"memory":{"notes":"","facts":[]},"messages":[{"role":"user","content":"hi","x_roger":{"turn":0,"agent":"user","model":null,"provider":null,"ts":100}}],"meta":{"tools_used":[],"exported_by":"roger-cli","created_at":100,"owner_pubkey":"aa"}}`
	goldenIOS = `{"capsule":"roger.context.v1","id":"cap_x","thread":{"origin_thread_id":"t1","title":"Hi","base_watermark":1},"redaction":"full","summary":{"text":"hi","produced_by":"none","as_of_turn":1},"memory":{"notes":"","facts":[]},"messages":[{"role":"user","content":"hi","x_roger":{"turn":0,"agent":"user","model":null,"provider":null,"ts":100}}],"meta":{"tools_used":[],"exported_by":"roger-ios","created_at":100,"owner_pubkey":"aa"}}`
)

type capsuleBDD struct {
	priv      ed25519.PrivateKey
	fixtures  []Capsule // for the two-producer scenario
	subject   Capsule   // the capsule under test
	canon     string
	incoming  Capsule
	target    Capsule
	merged    Capsule
	mergeErr  error
	importErr error
	imported  bool
}

func fixedNow() int64 { return 100 }

func (s *capsuleBDD) freshKeypair() error {
	_, priv, err := ed25519.GenerateKey(nil)
	s.priv = priv
	return err
}

func (s *capsuleBDD) goldenFixture(producer string) error {
	s.subject = goldenInput(producer)
	s.fixtures = append(s.fixtures, s.subject)
	return nil
}

func (s *capsuleBDD) computeCanonical() error {
	s.canon = string(s.subject.canonical())
	return nil
}

func (s *capsuleBDD) canonEquals(producer string) error {
	want := goldenCLI
	if producer == "roger-ios" {
		want = goldenIOS
	}
	if s.canon != want {
		return bddErrf("canonical mismatch\n got: %s\nwant: %s", s.canon, want)
	}
	return nil
}

func (s *capsuleBDD) producersDifferOnlyInExportedBy() error {
	if len(s.fixtures) != 2 {
		return bddErrf("need both producer fixtures")
	}
	a := string(s.fixtures[0].canonical())
	b := string(s.fixtures[1].canonical())
	// Normalize the exported_by value and the bytes must be identical.
	an := strings.Replace(a, `"exported_by":"roger-cli"`, `"exported_by":"X"`, 1)
	an = strings.Replace(an, `"exported_by":"roger-ios"`, `"exported_by":"X"`, 1)
	bn := strings.Replace(b, `"exported_by":"roger-cli"`, `"exported_by":"X"`, 1)
	bn = strings.Replace(bn, `"exported_by":"roger-ios"`, `"exported_by":"X"`, 1)
	if an != bn {
		return bddErrf("producers differ beyond exported_by:\n%s\n%s", a, b)
	}
	if a == b {
		return bddErrf("producers must differ in exported_by")
	}
	return nil
}

func (s *capsuleBDD) canonLiteralNull() error {
	if !strings.Contains(s.canon, `"model":null,"provider":null`) {
		return bddErrf("canonical must carry literal null model+provider: %s", s.canon)
	}
	return nil
}

func (s *capsuleBDD) setSig(v string) error {
	s.canon = string(s.subject.canonical()) // baseline BEFORE the sig is set
	s.subject.Sig = v
	return nil
}

func (s *capsuleBDD) canonUnchanged() error {
	if got := string(s.subject.canonical()); got != s.canon {
		return bddErrf("canonical changed after sig set:\n before: %s\n after: %s", s.canon, got)
	}
	return nil
}

// --- merge steps ---

func (s *capsuleBDD) signedCapsuleWithTurns(watermark int, table *godog.Table) error {
	msgs, err := rowsToMessages(table)
	if err != nil {
		return err
	}
	c := Capsule{Capsule: Version, ID: "cap", Thread: Thread{OriginThreadID: "t1", Title: "T", BaseWatermark: watermark}, Messages: msgs, Meta: Meta{ToolsUsed: []string{}}}
	c.Sign(s.priv)
	s.incoming = c
	return nil
}

// contentForTurn is the fixed content convention the tables use (turn 0 "hi", 1 "yo",
// 2 "more"), so a synthetic target and a table-built incoming agree on the content of an
// overlapping turn (otherwise every overlap would look like a fork).
func contentForTurn(t int) string {
	switch t {
	case 0:
		return "hi"
	case 1:
		return "yo"
	case 2:
		return "more"
	default:
		return "t" + strconv.Itoa(t)
	}
}

func (s *capsuleBDD) targetWithTurns(csv string, watermark int) error {
	c := Capsule{Capsule: Version, Thread: Thread{BaseWatermark: watermark}}
	for _, t := range parseCSVInts(csv) {
		role := "user"
		if t%2 == 1 {
			role = "assistant"
		}
		c.Messages = append(c.Messages, Message{Role: role, Content: contentForTurn(t), XRoger: XRoger{Turn: t, Agent: "user", TS: int64(t)}})
	}
	s.target = c
	return nil
}

func (s *capsuleBDD) mergeIntoEmpty() error {
	s.merged, s.mergeErr = Merge(s.incoming, Capsule{Capsule: Version})
	return nil
}
func (s *capsuleBDD) mergeIntoTarget() error {
	s.merged, s.mergeErr = Merge(s.incoming, s.target)
	return nil
}

// mergeIntoResult merges the current incoming into the previous merge result - used both
// for the idempotent "merge again" case and the "merge a smaller capsule" case.
func (s *capsuleBDD) mergeIntoResult() error {
	s.merged, s.mergeErr = Merge(s.incoming, s.merged)
	return nil
}

func (s *capsuleBDD) mergeSucceeds() error {
	if s.mergeErr != nil {
		return bddErrf("expected merge success, got %v", s.mergeErr)
	}
	return nil
}
func (s *capsuleBDD) mergeRejected(kind string) error {
	var want error
	switch kind {
	case "unverified":
		want = ErrUnverified
	case "forked":
		want = ErrForkedTurn
	case "unknown-version":
		want = ErrUnknownVersion
	default:
		return bddErrf("unknown rejection kind %q", kind)
	}
	if !errors.Is(s.mergeErr, want) {
		return bddErrf("expected %v, got %v", want, s.mergeErr)
	}
	return nil
}

func (s *capsuleBDD) mergedTurns(csv string) error {
	got := mergedTurnCSV(s.merged)
	if got != csv {
		return bddErrf("merged turns = %q, want %q", got, csv)
	}
	return nil
}
func (s *capsuleBDD) mergedWatermark(n int) error {
	if s.merged.Thread.BaseWatermark != n {
		return bddErrf("merged watermark = %d, want %d", s.merged.Thread.BaseWatermark, n)
	}
	return nil
}
func (s *capsuleBDD) mergedSigCleared() error {
	if s.merged.Sig != "" {
		return bddErrf("merged sig must be cleared, got %q", s.merged.Sig)
	}
	return nil
}
func (s *capsuleBDD) tamperIncomingAfterSigning() error {
	if len(s.incoming.Messages) == 0 {
		return bddErrf("no message to tamper")
	}
	s.incoming.Messages[len(s.incoming.Messages)-1].Content = "evil"
	return nil
}
func (s *capsuleBDD) changeVersionResign(v string) error {
	s.incoming.Capsule = v
	s.incoming.Sign(s.priv)
	return nil
}

// --- security steps ---

func (s *capsuleBDD) signedSummaryOnly() error {
	c, err := Export(SummaryOnly(Draft{
		ID:       "cap_s",
		Thread:   Thread{OriginThreadID: "t1", Title: "T", BaseWatermark: 3},
		Summary:  Summary{Text: "so far", ProducedBy: "operator:m", AsOfTurn: 2},
		Memory:   Memory{Notes: "secret", Facts: []string{"private"}},
		Messages: []Message{{Role: "user", Content: "current", XRoger: XRoger{Turn: 2, Agent: "user", TS: 9}}},
	}), s.priv, "roger-cli", fixedNow)
	s.subject = c
	return err
}
func (s *capsuleBDD) redactionIs(v string) error {
	if s.subject.Redaction != v {
		return bddErrf("redaction = %q, want %q", s.subject.Redaction, v)
	}
	return nil
}
func (s *capsuleBDD) flipRedaction(v string) error { s.subject.Redaction = v; return nil }
func (s *capsuleBDD) noLongerVerifies() error {
	if s.subject.Verify() {
		return bddErrf("capsule must not verify after tamper")
	}
	return nil
}
func (s *capsuleBDD) tamperField(field string) error {
	switch field {
	case "content":
		s.subject.Messages[0].Content = "evil"
	case "role":
		s.subject.Messages[0].Role = "system"
	case "title":
		s.subject.Thread.Title = "other"
	case "summary":
		s.subject.Summary.Text = "rewritten"
	case "created_at":
		s.subject.Meta.CreatedAt = 200
	case "exported_by":
		s.subject.Meta.ExportedBy = "roger-ios"
	case "watermark":
		s.subject.Thread.BaseWatermark = 99
	default:
		return bddErrf("unknown field %q", field)
	}
	return nil
}
func (s *capsuleBDD) signedCapsuleWithToolCalls() error {
	c := Capsule{Capsule: Version, ID: "cap_tc", Thread: Thread{BaseWatermark: 1}, Messages: []Message{{Role: "assistant", Content: "c", ToolCalls: json.RawMessage(`[{"id":"1"}]`), XRoger: XRoger{Turn: 0, Agent: "roger:m", TS: 1}}}, Meta: Meta{ToolsUsed: []string{}}}
	c.Sign(s.priv)
	s.incoming = c
	return nil
}
func (s *capsuleBDD) importIncoming() error {
	raw, _ := s.incoming.Marshal()
	_, s.importErr = Import(raw)
	return nil
}
func (s *capsuleBDD) importRejectedToolCalls() error {
	if !errors.Is(s.importErr, ErrToolCalls) {
		return bddErrf("expected ErrToolCalls, got %v", s.importErr)
	}
	return nil
}
func (s *capsuleBDD) draftWithToolCalls() error {
	s.subject = Capsule{} // reuse subject as a marker; the draft is built in exportDraft
	return nil
}
func (s *capsuleBDD) exportDraft() error {
	_, s.importErr = Export(Draft{ID: "cap_d", Messages: []Message{{Role: "assistant", Content: "c", ToolCalls: json.RawMessage(`[{"id":"1"}]`), XRoger: XRoger{Turn: 0, Agent: "roger:m"}}}}, s.priv, "roger-cli", fixedNow)
	return nil
}
func (s *capsuleBDD) exportRefusedToolCalls() error {
	if !errors.Is(s.importErr, ErrToolCalls) {
		return bddErrf("expected ErrToolCalls, got %v", s.importErr)
	}
	return nil
}
func (s *capsuleBDD) marshalAndImport() error {
	raw, _ := s.incoming.Marshal()
	c, err := Import(raw)
	s.importErr = err
	s.imported = err == nil
	s.subject = c
	return nil
}
func (s *capsuleBDD) importSucceeds() error {
	if !s.imported {
		return bddErrf("expected import success, got %v", s.importErr)
	}
	return nil
}
func (s *capsuleBDD) importedVerifies() error {
	if !s.subject.Verify() {
		return bddErrf("imported capsule must verify")
	}
	return nil
}

// --- helpers ---

func rowsToMessages(table *godog.Table) ([]Message, error) {
	var msgs []Message
	for i, row := range table.Rows {
		if i == 0 {
			continue // header
		}
		turn, err := strconv.Atoi(strings.TrimSpace(row.Cells[0].Value))
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, Message{
			Role:    strings.TrimSpace(row.Cells[1].Value),
			Content: strings.TrimSpace(row.Cells[2].Value),
			XRoger:  XRoger{Turn: turn, Agent: "user", TS: int64(turn)},
		})
	}
	return msgs, nil
}

func parseCSVInts(csv string) []int {
	var out []int
	for _, p := range strings.Split(csv, ",") {
		if p = strings.TrimSpace(p); p != "" {
			n, _ := strconv.Atoi(p)
			out = append(out, n)
		}
	}
	return out
}

func mergedTurnCSV(c Capsule) string {
	var parts []string
	for _, m := range c.Messages {
		parts = append(parts, strconv.Itoa(m.XRoger.Turn))
	}
	return strings.Join(parts, ",")
}

type bddError string

func (e bddError) Error() string            { return string(e) }
func bddErrf(format string, a ...any) error { return bddError(fmt.Sprintf(format, a...)) }

func TestCapsuleBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &capsuleBDD{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = capsuleBDD{}
				return ctx, nil
			})
			// canonical
			sc.Step(`^the golden fixture capsule exported by "([^"]*)"$`, st.goldenFixture)
			sc.Step(`^I compute its canonical bytes$`, st.computeCanonical)
			sc.Step(`^they equal the golden canonical string for "([^"]*)"$`, st.canonEquals)
			sc.Step(`^their canonical bytes differ only in the exported_by value$`, st.producersDifferOnlyInExportedBy)
			sc.Step(`^the canonical bytes carry a literal null model and provider$`, st.canonLiteralNull)
			sc.Step(`^I set the signature to "([^"]*)"$`, st.setSig)
			sc.Step(`^the canonical bytes are unchanged$`, st.canonUnchanged)
			// merge
			sc.Step(`^a fresh operator keypair$`, st.freshKeypair)
			sc.Step(`^a signed capsule with watermark (\d+) and turns$`, st.signedCapsuleWithTurns)
			sc.Step(`^the target thread has turns "([^"]*)" at watermark (\d+)$`, st.targetWithTurns)
			sc.Step(`^I merge it into an empty thread$`, st.mergeIntoEmpty)
			sc.Step(`^I merge it into the target$`, st.mergeIntoTarget)
			sc.Step(`^I merge it again into the result$`, st.mergeIntoResult)
			sc.Step(`^I merge it into the result$`, st.mergeIntoResult)
			sc.Step(`^the merge succeeds$`, st.mergeSucceeds)
			sc.Step(`^the merge is rejected as (unverified|forked|unknown-version)$`, st.mergeRejected)
			sc.Step(`^the merged thread has turns "([^"]*)"$`, st.mergedTurns)
			sc.Step(`^the merged watermark is (\d+)$`, st.mergedWatermark)
			sc.Step(`^the merged signature is cleared$`, st.mergedSigCleared)
			sc.Step(`^the incoming capsule is tampered after signing$`, st.tamperIncomingAfterSigning)
			sc.Step(`^the incoming capsule version is changed and re-signed to "([^"]*)"$`, st.changeVersionResign)
			// security
			sc.Step(`^a signed summary-only capsule$`, st.signedSummaryOnly)
			sc.Step(`^the capsule redaction is "([^"]*)"$`, st.redactionIs)
			sc.Step(`^I flip the redaction to "([^"]*)" without the key$`, st.flipRedaction)
			sc.Step(`^the capsule no longer verifies$`, st.noLongerVerifies)
			sc.Step(`^I tamper the field "([^"]*)"$`, st.tamperField)
			sc.Step(`^a signed capsule carrying tool_calls$`, st.signedCapsuleWithToolCalls)
			sc.Step(`^I import it$`, st.importIncoming)
			sc.Step(`^the import is rejected as tool_calls-unsupported$`, st.importRejectedToolCalls)
			sc.Step(`^a draft carrying tool_calls$`, st.draftWithToolCalls)
			sc.Step(`^I export it$`, st.exportDraft)
			sc.Step(`^the export is refused as tool_calls-unsupported$`, st.exportRefusedToolCalls)
			sc.Step(`^I marshal and import it$`, st.marshalAndImport)
			sc.Step(`^the import succeeds$`, st.importSucceeds)
			sc.Step(`^the imported capsule verifies$`, st.importedVerifies)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/capsule/canonical.feature", "../../features/capsule/merge.feature", "../../features/capsule/security.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("capsule behavior scenarios failed (see godog output above)")
	}
}
