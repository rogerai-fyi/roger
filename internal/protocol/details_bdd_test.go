package protocol

// details_bdd_test.go makes the @protocol scenarios of features/details/offer_details.feature
// and features/details/text_sanitization.feature EXECUTABLE: the REAL ModelOffer.Normalize,
// Taxonomy.Declare and the registration possession proof. Reuses unesc, errExpect and eq.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
)

type detailsState struct {
	o        ModelOffer
	j        []byte
	tax      Taxonomy // a declaration under test
	saved    Taxonomy // what Declare returned
	err      error
	raw      string // the text a "summary <raw>" outline row carries
	reg      NodeRegistration
	priv     ed25519.PrivateKey
	sigA     []byte
	valid    bool
	declared bool
}

// setField writes one details field by its spec name.
func setField(t *Taxonomy, field, v string) error {
	switch field {
	case "summary":
		t.Summary = v
	case "best_at":
		t.BestAt = v
	case "not_for":
		t.NotFor = v
	default:
		return fmt.Errorf("unknown details field %q", field)
	}
	return nil
}

func getField(t Taxonomy, field string) string {
	switch field {
	case "summary":
		return t.Summary
	case "best_at":
		return t.BestAt
	case "not_for":
		return t.NotFor
	}
	return "?" + field
}

func (s *detailsState) offer(model string) {
	s.o = ModelOffer{Model: model, PriceIn: 1, PriceOut: 2}
}

func (s *detailsState) noDetails(model string) error { s.offer(model); return nil }
func (s *detailsState) withSummary(model, sum string) error {
	s.newReg(unesc(sum)) // the signing scenario reads the offer from a registration
	s.offer(model)
	s.o.Summary = unesc(sum)
	return nil
}
func (s *detailsState) withSummarySource(model, sum, src string) error {
	s.offer(model)
	s.o.Summary, s.o.DetailsSource = unesc(sum), src
	return nil
}
func (s *detailsState) sourceNoText(model, src string) error {
	s.offer(model)
	s.o.DetailsSource = src
	return nil
}

// pageKey decodes a raw wire body carrying "page", the way a hostile node would send it.
func (s *detailsState) pageKey(model, key, val string) error {
	body := fmt.Sprintf(`{"model":%q,"summary":"x",%q:%q}`, model, key, val)
	s.o = ModelOffer{}
	return json.Unmarshal([]byte(body), &s.o)
}
func (s *detailsState) voiceSummary(model, modality, sum string) error {
	s.offer(model)
	s.o.Modality, s.o.Summary = modality, sum
	return nil
}
func (s *detailsState) fieldOfN(field string, n int) error {
	s.offer("m")
	return setField(&s.o.Taxonomy, field, strings.Repeat("a", n))
}
func (s *detailsState) allHuge(n int) error {
	s.offer("m")
	big := strings.Repeat("a", n)
	s.o.Summary, s.o.BestAt, s.o.NotFor = big, big, big
	return nil
}
func (s *detailsState) summaryBytes(b string) error {
	s.offer("m")
	s.o.Summary = unesc(b)
	return nil
}
func (s *detailsState) summaryRunes(n int, ch string) error {
	s.offer("m")
	s.o.Summary = strings.Repeat(ch, n)
	return nil
}
func (s *detailsState) summaryAndBest(sum, best string) error {
	s.offer("m")
	s.o.Summary, s.o.BestAt = sum, best
	return nil
}

func (s *detailsState) normalize() error { s.o.Normalize(); return nil }
func (s *detailsState) encode() error {
	var err error
	s.j, err = json.Marshal(s.o)
	return err
}

func (s *detailsState) noText() error {
	if s.o.Summary != "" || s.o.BestAt != "" || s.o.NotFor != "" {
		return errExpect(fmt.Sprintf("no details text, got %q / %q / %q", s.o.Summary, s.o.BestAt, s.o.NotFor))
	}
	return nil
}
func (s *detailsState) noSource() error          { return eq("details source", "", s.o.DetailsSource) }
func (s *detailsState) noSummary() error         { return eq("summary", "", s.o.Summary) }
func (s *detailsState) noPage() error            { return eq("page", "", s.o.Page) }
func (s *detailsState) sourceIs(v string) error  { return eq("details source", v, s.o.DetailsSource) }
func (s *detailsState) summaryIs(v string) error { return eq("summary", unesc(v), s.o.Summary) }
func (s *detailsState) bestIs(v string) error    { return eq("best_at", v, s.o.BestAt) }
func (s *detailsState) catSubIs(cat, sub string) error {
	if err := eq("category", cat, s.o.Category); err != nil {
		return err
	}
	return eq("subcategory", sub, s.o.Subcategory)
}
func (s *detailsState) fieldKept(field string, n int) error {
	if got := utf8.RuneCountInString(getField(s.o.Taxonomy, field)); got != n {
		return errExpect(fmt.Sprintf("%s of %d characters, got %d", field, n, got))
	}
	return nil
}
func (s *detailsState) validUTF8() error {
	if !utf8.ValidString(s.o.Summary) {
		return errExpect("valid UTF-8")
	}
	return nil
}
func (s *detailsState) validUTF8N(n int) error {
	if err := s.validUTF8(); err != nil {
		return err
	}
	return s.fieldKept("summary", n)
}
func (s *detailsState) startsEnds(a, b string) error {
	if !strings.HasPrefix(s.o.Summary, a) || !strings.HasSuffix(s.o.Summary, b) {
		return errExpect(fmt.Sprintf("summary from %q to %q, got %q", a, b, s.o.Summary))
	}
	return nil
}
func (s *detailsState) totalAtMost(n int) error {
	got := utf8.RuneCountInString(s.o.Summary + s.o.BestAt + s.o.NotFor)
	if got > n {
		return errExpect(fmt.Sprintf("at most %d characters, got %d", n, got))
	}
	return nil
}
func (s *detailsState) noJSONKeys(a, b, c, d string) error {
	for _, k := range []string{a, b, c, d} {
		if strings.Contains(string(s.j), `"`+k+`"`) {
			return errExpect(fmt.Sprintf("no %q key in %s", k, s.j))
		}
	}
	return nil
}

// ---- signing
func (s *detailsState) newReg(sum string) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	s.priv = priv
	s.reg = NodeRegistration{NodeID: "amber-fox", PubKey: hex.EncodeToString(pub), TS: 123,
		Offers: []ModelOffer{{Model: "demo-1b-chat", PriceIn: 1, PriceOut: 2, Taxonomy: Taxonomy{Summary: sum}}}}
}
func (s *detailsState) signingBytes() error {
	s.sigA = s.reg.regSigningBytes()
	return nil
}
func (s *detailsState) signingEqualsBare() error {
	bare := s.reg
	bare.Offers = []ModelOffer{{Model: "demo-1b-chat", PriceIn: 1, PriceOut: 2}}
	if string(bare.regSigningBytes()) != string(s.sigA) {
		return errExpect("identical signing bytes with and without details")
	}
	return nil
}
func (s *detailsState) signingLacks(a, b, c string) error {
	for _, k := range []string{a, b, c} {
		if strings.Contains(string(s.sigA), k) {
			return errExpect(fmt.Sprintf("signing bytes without %q", k))
		}
	}
	return nil
}
func (s *detailsState) signReg(sum string) error {
	s.newReg(sum)
	s.reg.SignRegistration(s.priv)
	return nil
}
func (s *detailsState) canonicalizeVerify() error {
	b, err := json.Marshal(s.reg)
	if err != nil {
		return err
	}
	var got NodeRegistration
	if err := json.Unmarshal(b, &got); err != nil {
		return err
	}
	for i := range got.Offers {
		got.Offers[i].Summary = "rewritten"
		got.Offers[i].DetailsSource = SourceRogerAI
	}
	s.valid = got.VerifyRegistration()
	return nil
}
func (s *detailsState) proofOK() error {
	if !s.valid {
		return errExpect("the possession proof to verify")
	}
	return nil
}

// ---- declarations (the strict layer)
func (s *detailsState) taxOnlySummary(v string) error { s.tax = Taxonomy{Summary: v}; return nil }
func (s *detailsState) taxOnlyCategory(v string) error {
	c, sub := SplitCategory(v)
	s.tax = Taxonomy{Category: c, Subcategory: sub}
	return nil
}
func (s *detailsState) isDeclared() error {
	if !s.tax.Declared() {
		return errExpect("Declared() true")
	}
	return nil
}
func (s *detailsState) notDeclared() error {
	if s.tax.Declared() {
		return errExpect("Declared() false")
	}
	return nil
}
func (s *detailsState) hasDetails() error {
	if !s.tax.DetailsDeclared() {
		return errExpect("DetailsDeclared() true")
	}
	return nil
}
func (s *detailsState) noDetailsDeclared() error {
	if s.tax.DetailsDeclared() {
		return errExpect("DetailsDeclared() false")
	}
	return nil
}

func (s *detailsState) declare(t Taxonomy) {
	s.saved, s.err = t.Declare(ModalityChat)
}
func (s *detailsState) declareNChars(field string, n int, ch string) error {
	var t Taxonomy
	if err := setField(&t, field, strings.Repeat(ch, n)); err != nil {
		return err
	}
	s.declare(t)
	return nil
}
func (s *detailsState) declareField(field, text string) error {
	var t Taxonomy
	if err := setField(&t, field, unesc(text)); err != nil {
		return err
	}
	s.declare(t)
	return nil
}
func (s *detailsState) declareSummary(text string) error { return s.declareField("summary", text) }
func (s *detailsState) declareNotFor(text string) error  { return s.declareField("not_for", text) }
func (s *detailsState) declarePadded(pre string, n int, ch, post string) error {
	s.declare(Taxonomy{Summary: pre + strings.Repeat(ch, n) + post})
	return nil
}
func (s *detailsState) accepted() error {
	if s.err != nil {
		return errExpect(fmt.Sprintf("accepted, got %v", s.err))
	}
	return nil
}
func (s *detailsState) refusedWith(msg string) error {
	msg = strings.ReplaceAll(msg, `\"`, `"`) // a Gherkin string spells a quote as \"
	if s.err == nil || !strings.Contains(s.err.Error(), msg) {
		return errExpect(fmt.Sprintf("refused with %q, got %v", msg, s.err))
	}
	return nil
}
func (s *detailsState) result(res string) error {
	if res == "accepted" {
		return s.accepted()
	}
	m := regexp.MustCompile(`^refused with "(.*)"$`).FindStringSubmatch(res)
	if m == nil {
		return fmt.Errorf("unknown result %q", res)
	}
	return s.refusedWith(m[1])
}
func (s *detailsState) savedNoSummary() error {
	if err := s.accepted(); err != nil {
		return err
	}
	return eq("saved summary", "", s.saved.Summary)
}
func (s *detailsState) savedSummaryLen(n int) error {
	if got := utf8.RuneCountInString(s.saved.Summary); got != n {
		return errExpect(fmt.Sprintf("saved summary of %d, got %d", n, got))
	}
	return nil
}

// ---- the both-layers outline
// Gherkin itself decodes "\n" in a table cell, so a real newline is re-spelled before unesc.
func (s *detailsState) rawSummary(raw string) error {
	s.raw = unesc(strings.ReplaceAll(raw, "\n", `\n`))
	return nil
}
func (s *detailsState) declaredByOperator() error {
	s.declare(Taxonomy{Summary: s.raw})
	return nil
}
func (s *detailsState) savedSummaryIs(v string) error {
	if err := s.accepted(); err != nil {
		return err
	}
	return eq("saved summary", unesc(v), s.saved.Summary)
}
func (s *detailsState) arrivesFromNode() error {
	s.offer("m")
	s.o.Summary = s.raw
	s.o.Normalize()
	return nil
}

func TestOfferDetailsBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &detailsState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = detailsState{}
				return ctx, nil
			})
			q := `"([^"]*)"`
			sc.Step(`^a node registering an offer with the broker$`, func() error { return nil })
			sc.Step(`^an offer for model `+q+` with no details fields$`, st.noDetails)
			sc.Step(`^an offer for model `+q+` with summary `+q+`$`, st.withSummary)
			sc.Step(`^an offer for model `+q+` with summary `+q+` and details source `+q+`$`, st.withSummarySource)
			sc.Step(`^an offer for model `+q+` with details source `+q+` and no details text$`, st.sourceNoText)
			sc.Step(`^an offer for model `+q+` carrying a `+q+` key `+q+`$`, st.pageKey)
			sc.Step(`^an offer for model `+q+` with modality `+q+` and summary `+q+`$`, st.voiceSummary)
			sc.Step(`^an offer whose (summary|best_at|not_for) is a string of (\d+) "a" characters$`, st.fieldOfN)
			sc.Step(`^an offer whose summary, best_at and not_for are each (\d+) characters$`, st.allHuge)
			sc.Step(`^an offer whose summary bytes are `+q+`$`, st.summaryBytes)
			sc.Step(`^an offer whose summary is (\d+) `+q+` characters$`, st.summaryRunes)
			sc.Step(`^an offer whose summary is `+q+` and best_at is `+q+`$`, st.summaryAndBest)
			sc.Step(`^the broker normalizes the offer$`, st.normalize)
			sc.Step(`^the offer is encoded as JSON$`, st.encode)
			sc.Step(`^the offer has no summary, best_at or not_for$`, st.noText)
			sc.Step(`^the offer has no details source$`, st.noSource)
			sc.Step(`^the offer has no summary$`, st.noSummary)
			sc.Step(`^the offer has no page link$`, st.noPage)
			sc.Step(`^the offer's details source is `+q+`$`, st.sourceIs)
			sc.Step(`^the offer's best_at is `+q+`$`, st.bestIs)
			sc.Step(`^the offer's category is `+q+` and subcategory `+q+`$`, st.catSubIs)
			sc.Step(`^the offer's (summary|best_at|not_for) has (\d+) characters$`, st.fieldKept)
			sc.Step(`^the offer's summary is valid UTF-8$`, st.validUTF8)
			sc.Step(`^the offer's summary is valid UTF-8 with (\d+) characters$`, st.validUTF8N)
			sc.Step(`^the offer's summary starts with `+q+` and ends with `+q+`$`, st.startsEnds)
			sc.Step(`^the three fields together hold at most (\d+) characters$`, st.totalAtMost)
			sc.Step(`^the JSON has no `+q+`, `+q+`, `+q+` or `+q+` key$`, st.noJSONKeys)
			sc.Step(`^the registration signing bytes are computed$`, st.signingBytes)
			sc.Step(`^the signing bytes equal those of the same offer with no details$`, st.signingEqualsBare)
			sc.Step(`^the signing bytes contain neither `+q+` nor `+q+` nor `+q+`$`, st.signingLacks)
			sc.Step(`^a node signs a registration whose offer has summary `+q+`$`, st.signReg)
			sc.Step(`^the broker canonicalizes the details and verifies the registration$`, st.canonicalizeVerify)
			sc.Step(`^the possession proof verifies$`, st.proofOK)
			sc.Step(`^a taxonomy with only summary `+q+`$`, st.taxOnlySummary)
			sc.Step(`^a taxonomy with only category `+q+`$`, st.taxOnlyCategory)
			sc.Step(`^the taxonomy is Declared$`, st.isDeclared)
			sc.Step(`^the taxonomy is not Declared$`, st.notDeclared)
			sc.Step(`^the taxonomy has declared details$`, st.hasDetails)
			sc.Step(`^the taxonomy has no declared details$`, st.noDetailsDeclared)
			sc.Step(`^the operator declares (summary|best_at|not_for) as a string of (\d+) `+q+` characters$`, st.declareNChars)
			sc.Step(`^the operator declares summary as `+q+` followed by (\d+) `+q+` characters followed by `+q+`$`, st.declarePadded)
			sc.Step(`^the operator declares (summary|best_at) `+q+`$`, st.declareField)
			sc.Step(`^the operator declares not_for `+q+`$`, st.declareNotFor)
			sc.Step(`^the declaration is (accepted|refused with ".*")$`, st.result)
			sc.Step(`^the saved declaration has no summary$`, st.savedNoSummary)
			sc.Step(`^the saved summary has (\d+) characters$`, st.savedSummaryLen)
			sc.Step(`(?s)^a summary "(.*)"$`, st.rawSummary)
			sc.Step(`^it is declared by the operator$`, st.declaredByOperator)
			sc.Step(`^the saved summary is "(.*)"$`, st.savedSummaryIs)
			sc.Step(`^it arrives from a node and the broker normalizes it$`, st.arrivesFromNode)
			sc.Step(`^the offer's summary is "(.*)"$`, st.summaryIs)
		},
		Options: &godog.Options{
			Format: "pretty",
			Paths: []string{"../../features/details/offer_details.feature",
				"../../features/details/text_sanitization.feature"},
			Tags:     "@protocol",
			TestingT: t,
			Strict:   true,
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("details @protocol behavior scenarios failed (see godog output above)")
	}
}
