package protocol

// taxonomy_bdd_test.go makes features/categories/offer_taxonomy.feature EXECUTABLE under
// godog, driving the REAL ModelOffer.Normalize and the registration possession proof, so the
// taxonomy contract (closed category list, tag hygiene, reserved tags, claim source, and
// mixed-version signing) fails red if it regresses. MODEL-CATEGORIES-DESIGN-2026-10-05.
// Reuses errExpect from auth_bdd_test.go (same package).

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

type taxonomyState struct {
	o, first ModelOffer
	j        []byte
	reg      NodeRegistration
	reg2     NodeRegistration
	priv     ed25519.PrivateKey
	valid    bool
}

// unesc lets a feature write a control character as \u001b: Gherkin passes the backslash
// through literally, and the attack these scenarios pin is the real byte, not its spelling.
func unesc(s string) string {
	if u, err := strconv.Unquote(`"` + strings.ReplaceAll(s, `"`, `\"`) + `"`); err == nil {
		return u
	}
	return s
}

func splitTags(s string) []string { return strings.Split(unesc(s), ",") }

func (s *taxonomyState) noTaxonomy(model string) error {
	s.o = ModelOffer{Model: model}
	return nil
}

func (s *taxonomyState) fullOffer(model, cat, sub, tags, tuning, base, policy, license, src string) error {
	s.o = ModelOffer{Model: model, Taxonomy: Taxonomy{
		Category: cat, Subcategory: sub, Tags: splitTags(tags), Tuning: tuning,
		BaseModel: base, ContentPolicy: policy, License: license, TaxonomySource: src,
	}}
	return nil
}

func (s *taxonomyState) catSub(cat, sub string) error {
	s.o = ModelOffer{Model: "m", Taxonomy: Taxonomy{Category: cat, Subcategory: sub}}
	return nil
}
func (s *taxonomyState) catOnly(cat string) error { return s.catSub(cat, "") }
func (s *taxonomyState) subOnly(sub string) error { return s.catSub("", sub) }

func (s *taxonomyState) modalityCatSub(mod, cat, sub string) error {
	s.o = ModelOffer{Model: "m", Modality: mod, Taxonomy: Taxonomy{Category: cat, Subcategory: sub}}
	return nil
}

func (s *taxonomyState) tags(t string) error {
	s.o = ModelOffer{Model: "m", Taxonomy: Taxonomy{Tags: splitTags(t)}}
	return nil
}
func (s *taxonomyState) tagsPlusLong(t string, n int) error {
	s.o = ModelOffer{Model: "m", Taxonomy: Taxonomy{Tags: append(splitTags(t), strings.Repeat("a", n))}}
	return nil
}
func (s *taxonomyState) oneTagOf(n int) error {
	s.o = ModelOffer{Model: "m", Taxonomy: Taxonomy{Tags: []string{strings.Repeat("a", n)}}}
	return nil
}
func (s *taxonomyState) manyTags(n int) error {
	t := make([]string, n)
	for i := range t {
		t[i] = fmt.Sprintf("tag%d", i)
	}
	s.o = ModelOffer{Model: "m", Taxonomy: Taxonomy{Tags: t}}
	return nil
}

func (s *taxonomyState) tuning(v string) error {
	s.o = ModelOffer{Model: "m", Taxonomy: Taxonomy{Tuning: v}}
	return nil
}
func (s *taxonomyState) baseModel(v string) error {
	s.o = ModelOffer{Model: "m", Taxonomy: Taxonomy{BaseModel: unesc(v)}}
	return nil
}
func (s *taxonomyState) baseModelOf(n int) error { return s.baseModel(strings.Repeat("b", n)) }
func (s *taxonomyState) baseModelOfChars(n int, c string) error {
	return s.baseModel(strings.Repeat(c, n))
}
func (s *taxonomyState) licenseOfChars(n int, c string) error { return s.license(strings.Repeat(c, n)) }
func (s *taxonomyState) policy(v string) error {
	s.o = ModelOffer{Model: "m", Taxonomy: Taxonomy{ContentPolicy: v}}
	return nil
}
func (s *taxonomyState) license(v string) error {
	s.o = ModelOffer{Model: "m", Taxonomy: Taxonomy{License: v}}
	return nil
}
func (s *taxonomyState) licenseOf(n int) error { return s.license(strings.Repeat("l", n)) }
func (s *taxonomyState) catSource(cat, src string) error {
	s.o = ModelOffer{Model: "m", Taxonomy: Taxonomy{Category: cat, TaxonomySource: src}}
	return nil
}
func (s *taxonomyState) noTaxonomySource(src string) error {
	s.o = ModelOffer{Model: "m", Taxonomy: Taxonomy{TaxonomySource: src}}
	return nil
}
func (s *taxonomyState) messy(cat, sub, tags, tuning, policy, src string) error {
	s.o = ModelOffer{Model: "m", Taxonomy: Taxonomy{
		Category: cat, Subcategory: sub, Tags: splitTags(tags), Tuning: tuning,
		ContentPolicy: policy, TaxonomySource: src,
	}}
	return nil
}

// --- When ---------------------------------------------------------------------------

func (s *taxonomyState) normalize() error { s.o.Normalize(); return nil }
func (s *taxonomyState) normalizeTwice() error {
	s.o.Normalize()
	s.first = s.o
	s.first.Tags = append([]string(nil), s.o.Tags...)
	s.o.Normalize()
	return nil
}
func (s *taxonomyState) marshal() (err error) { s.j, err = json.Marshal(s.o); return err }
func (s *taxonomyState) roundTrip() error {
	if err := s.marshal(); err != nil {
		return err
	}
	s.first = s.o
	s.o = ModelOffer{}
	return json.Unmarshal(s.j, &s.o)
}

// --- Then ---------------------------------------------------------------------------

func eq(field, want, got string) error {
	if want != got {
		return errExpect(fmt.Sprintf("%s %q, got %q", field, want, got))
	}
	return nil
}

func (s *taxonomyState) categoryIs(v string) error    { return eq("category", v, s.o.Category) }
func (s *taxonomyState) subcategoryIs(v string) error { return eq("subcategory", v, s.o.Subcategory) }
func (s *taxonomyState) noCategory() error            { return eq("category", "", s.o.Category) }
func (s *taxonomyState) noSubcategory() error         { return eq("subcategory", "", s.o.Subcategory) }
func (s *taxonomyState) noTags() error {
	if len(s.o.Tags) != 0 {
		return errExpect(fmt.Sprintf("no tags, got %q", s.o.Tags))
	}
	return nil
}
func (s *taxonomyState) noRest() error {
	t := s.o.Taxonomy
	if t.Tuning != "" || t.BaseModel != "" || t.ContentPolicy != "" || t.License != "" || t.TaxonomySource != "" {
		return errExpect(fmt.Sprintf("no tuning/base/policy/license/source, got %+v", t))
	}
	return nil
}
func (s *taxonomyState) noKeys(keys string) error {
	var m map[string]any
	if err := json.Unmarshal(s.j, &m); err != nil {
		return err
	}
	for _, k := range strings.Split(keys, ",") {
		if _, ok := m[k]; ok {
			return errExpect("no JSON key " + k + " in " + string(s.j))
		}
	}
	return nil
}
func (s *taxonomyState) unchanged() error {
	if !reflect.DeepEqual(s.first.Taxonomy, s.o.Taxonomy) {
		return errExpect(fmt.Sprintf("taxonomy %+v, got %+v", s.first.Taxonomy, s.o.Taxonomy))
	}
	return nil
}
func (s *taxonomyState) tagsAre(v string) error {
	return eq("tags", v, strings.Join(s.o.Tags, ","))
}
func (s *taxonomyState) tagCount(n int) error {
	if len(s.o.Tags) != n {
		return errExpect(fmt.Sprintf("%d tags, got %d", n, len(s.o.Tags)))
	}
	return nil
}
func (s *taxonomyState) tuningIs(v string) error    { return eq("tuning", v, s.o.Tuning) }
func (s *taxonomyState) baseModelIs(v string) error { return eq("base model", v, s.o.BaseModel) }
func (s *taxonomyState) policyIs(v string) error    { return eq("content policy", v, s.o.ContentPolicy) }
func (s *taxonomyState) licenseIs(v string) error   { return eq("license", v, s.o.License) }
func (s *taxonomyState) sourceIs(v string) error    { return eq("claim source", v, s.o.TaxonomySource) }
func runeLen(field, v string, n int) error {
	if got := len([]rune(v)); got != n {
		return errExpect(fmt.Sprintf("%s of %d characters, got %d", field, n, got))
	}
	return nil
}
func (s *taxonomyState) baseModelLen(n int) error { return runeLen("base model", s.o.BaseModel, n) }
func (s *taxonomyState) licenseLen(n int) error   { return runeLen("license", s.o.License, n) }
func (s *taxonomyState) secondEqualsFirst() error {
	if !reflect.DeepEqual(s.first, s.o) {
		return errExpect(fmt.Sprintf("idempotent normalize: %+v, got %+v", s.first, s.o))
	}
	return nil
}

// --- signing --------------------------------------------------------------------------

func (s *taxonomyState) newReg(model string, tx Taxonomy) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	s.priv = priv
	s.reg = NodeRegistration{
		NodeID: "amber-fox", PubKey: hex.EncodeToString(pub), TS: 123,
		Offers: []ModelOffer{{Model: model, PriceIn: 1, PriceOut: 2, Taxonomy: tx}},
	}
}

func (s *taxonomyState) signRich(cat, tags, policy string) error {
	s.newReg("qwen3-vl-8b", Taxonomy{Category: cat, Tags: splitTags(tags), ContentPolicy: policy})
	s.reg.SignRegistration(s.priv)
	return nil
}
func (s *taxonomyState) signBare() error {
	s.newReg("qwen3-vl-8b", Taxonomy{})
	s.reg.SignRegistration(s.priv)
	return nil
}

// decodeAndVerify is the broker's side: the registration crosses the wire as JSON. A broker
// that predates the taxonomy has no such fields, so json drops them on decode - modelled by
// zeroing them after decode. A broker that has the fields keeps them. Either way it verifies
// BEFORE normalizing (cmd/rogerai-broker/tunnel.go register: verify, then Normalize).
func (s *taxonomyState) decodeAndVerify(predates bool) error {
	b, err := json.Marshal(s.reg)
	if err != nil {
		return err
	}
	var got NodeRegistration
	if err := json.Unmarshal(b, &got); err != nil {
		return err
	}
	if predates {
		for i := range got.Offers {
			got.Offers[i].Taxonomy = Taxonomy{}
		}
	}
	s.valid = got.VerifyRegistration()
	return nil
}
func (s *taxonomyState) sigValid() error {
	if !s.valid {
		return errExpect("a valid registration signature")
	}
	return nil
}
func (s *taxonomyState) sigInvalid() error {
	if s.valid {
		return errExpect("an invalid registration signature")
	}
	return nil
}
func (s *taxonomyState) twoRegs() error {
	s.newReg("qwen3-vl-8b", Taxonomy{})
	s.reg2 = s.reg
	s.reg2.Offers = []ModelOffer{{Model: "qwen3-vl-8b", PriceIn: 1, PriceOut: 2, Taxonomy: Taxonomy{
		Category: "vision", Subcategory: "ocr", Tags: []string{"ppe"}, Tuning: "finetune",
		BaseModel: "Qwen/Qwen3-VL-8B", ContentPolicy: "uncensored", License: "MIT", TaxonomySource: "declared",
	}}}
	return nil
}
func (s *taxonomyState) sameSigningBytes() error {
	if string(s.reg.regSigningBytes()) != string(s.reg2.regSigningBytes()) {
		return errExpect("identical signing bytes with and without a taxonomy")
	}
	return nil
}
func (s *taxonomyState) signedCatTags(cat, tags string) error {
	s.newReg("qwen3-vl-8b", Taxonomy{Category: cat, Tags: splitTags(tags)})
	s.reg.SignRegistration(s.priv)
	return nil
}
func (s *taxonomyState) verify() error { s.valid = s.reg.VerifyRegistration(); return nil }
func (s *taxonomyState) stillHas(cat, tags string) error {
	o := s.reg.Offers[0]
	if err := eq("category", cat, o.Category); err != nil {
		return err
	}
	return eq("tags", tags, strings.Join(o.Tags, ","))
}
func (s *taxonomyState) signModelCat(model, cat string) error {
	s.newReg(model, Taxonomy{Category: cat})
	s.reg.SignRegistration(s.priv)
	return nil
}
func (s *taxonomyState) tamperModel(model string) error {
	s.reg.Offers[0].Model = model
	s.valid = s.reg.VerifyRegistration()
	return nil
}

func TestOfferTaxonomyBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &taxonomyState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = taxonomyState{}
				return ctx, nil
			})
			q := `"([^"]*)"`
			sc.Step(`^a node registering an offer with the broker$`, func() error { return nil })
			sc.Step(`^an offer for model `+q+` with no taxonomy fields$`, st.noTaxonomy)
			sc.Step(`^an offer for model `+q+` with category `+q+`, subcategory `+q+`, tags `+q+`, tuning `+q+`, base model `+q+`, content policy `+q+`, license `+q+` and source `+q+`$`, st.fullOffer)
			sc.Step(`^an offer with category `+q+` and subcategory `+q+`$`, st.catSub)
			sc.Step(`^an offer with category `+q+` and no subcategory$`, st.catOnly)
			sc.Step(`^an offer with no category and subcategory `+q+`$`, st.subOnly)
			sc.Step(`^an offer with modality `+q+`, category `+q+` and subcategory `+q+`$`, st.modalityCatSub)
			sc.Step(`^an offer with tags `+q+` and one tag of (\d+) characters$`, st.tagsPlusLong)
			sc.Step(`^an offer with tags `+q+`$`, st.tags)
			sc.Step(`^an offer with one tag of (\d+) characters$`, st.oneTagOf)
			sc.Step(`^an offer with (\d+) distinct tags$`, st.manyTags)
			sc.Step(`^an offer with tuning `+q+`$`, st.tuning)
			sc.Step(`^an offer with base model "(.*)"$`, st.baseModel)
			sc.Step(`^an offer with a base model of (\d+) characters$`, st.baseModelOf)
			sc.Step(`^an offer with a base model of (\d+) `+q+` characters$`, st.baseModelOfChars)
			sc.Step(`^an offer with a license of (\d+) `+q+` characters$`, st.licenseOfChars)
			sc.Step(`^an offer with content policy `+q+`$`, st.policy)
			sc.Step(`^an offer with license "(.*)"$`, st.license)
			sc.Step(`^an offer with a license of (\d+) characters$`, st.licenseOf)
			sc.Step(`^an offer with category `+q+` and claim source `+q+`$`, st.catSource)
			sc.Step(`^an offer with no taxonomy fields and claim source `+q+`$`, st.noTaxonomySource)
			sc.Step(`^an offer with category `+q+`, subcategory `+q+`, tags `+q+`, tuning `+q+`, content policy `+q+` and claim source `+q+`$`, st.messy)

			sc.Step(`^the broker normalizes the offer$`, st.normalize)
			sc.Step(`^the broker normalizes the offer twice$`, st.normalizeTwice)
			sc.Step(`^the offer is marshalled to JSON$`, st.marshal)
			sc.Step(`^the offer is marshalled to JSON and back$`, st.roundTrip)

			sc.Step(`^the offer has no category$`, st.noCategory)
			sc.Step(`^the offer has no subcategory$`, st.noSubcategory)
			sc.Step(`^the offer has no tags$`, st.noTags)
			sc.Step(`^the offer has no tuning, base model, content policy, license or claim source$`, st.noRest)
			sc.Step(`^the JSON has none of the keys `+q+`$`, st.noKeys)
			sc.Step(`^every taxonomy field is unchanged$`, st.unchanged)
			sc.Step(`^the offer category is `+q+`$`, st.categoryIs)
			sc.Step(`^the offer subcategory is `+q+`$`, st.subcategoryIs)
			sc.Step(`^the offer tags are `+q+`$`, st.tagsAre)
			sc.Step(`^the offer has (\d+) tags?$`, st.tagCount)
			sc.Step(`^the offer tuning is `+q+`$`, st.tuningIs)
			sc.Step(`^the offer base model is `+q+`$`, st.baseModelIs)
			sc.Step(`^the offer base model is (\d+) characters long$`, st.baseModelLen)
			sc.Step(`^the offer content policy is `+q+`$`, st.policyIs)
			sc.Step(`^the offer license is `+q+`$`, st.licenseIs)
			sc.Step(`^the offer license is (\d+) characters long$`, st.licenseLen)
			sc.Step(`^the offer claim source is `+q+`$`, st.sourceIs)
			sc.Step(`^the second result equals the first$`, st.secondEqualsFirst)

			sc.Step(`^a node signs a registration whose offer has category `+q+`, tags `+q+` and content policy `+q+`$`, st.signRich)
			sc.Step(`^a broker that predates the taxonomy decodes and verifies it$`, func() error { return st.decodeAndVerify(true) })
			sc.Step(`^a node that predates the taxonomy signs a registration$`, st.signBare)
			sc.Step(`^a broker with the taxonomy decodes and verifies it$`, func() error { return st.decodeAndVerify(false) })
			sc.Step(`^the registration signature is valid$`, st.sigValid)
			sc.Step(`^the registration signature is invalid$`, st.sigInvalid)
			sc.Step(`^two registrations identical except one offer has a full taxonomy$`, st.twoRegs)
			sc.Step(`^their signing bytes are identical$`, st.sameSigningBytes)
			sc.Step(`^a signed registration whose offer has category `+q+` and tags `+q+`$`, st.signedCatTags)
			sc.Step(`^the broker verifies it$`, st.verify)
			sc.Step(`^the offer still has category `+q+` and tags `+q+`$`, st.stillHas)
			sc.Step(`^a node signs a registration for model `+q+` with category `+q+`$`, st.signModelCat)
			sc.Step(`^the model id is changed to `+q+` in transit$`, st.tamperModel)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/categories/offer_taxonomy.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("categories/offer_taxonomy behavior scenarios failed (see godog output above)")
	}
}
