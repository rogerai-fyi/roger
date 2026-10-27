package protocol

import (
	"slices"
	"testing"
)

// The exported taxonomy helpers the broker builds filters and /categories from.

func TestCategoriesIsACopyOfTheClosedList(t *testing.T) {
	got := Categories()
	if len(got) != 6 || got[0].Name != "language" || got[5].Name != "signals" {
		t.Fatalf("unexpected list/order: %+v", got)
	}
	got[0].Name = "medical"
	got[1].Subcategories[0] = "radiology"
	again := Categories()
	if again[0].Name != "language" || again[1].Subcategories[0] != "vlm" {
		t.Fatal("mutating the returned list widened the closed set")
	}
	if KnownCategory("medical") || KnownSubcategory("vision", "radiology") {
		t.Fatal("a caller's mutation reached the lookups")
	}
}

func TestKnownCategoryAndSubcategory(t *testing.T) {
	for _, tc := range []struct {
		cat, sub string
		catOK    bool
		subOK    bool
	}{
		{"vision", "ocr", true, true},
		{"vision", "code", true, false},
		{"", "ocr", false, true}, // any category
		{"", "classification", false, true},
		{"", "nope", false, false},
		{"medical", "ocr", false, false},
		{"Vision", "ocr", false, false}, // callers canonicalize first
	} {
		if got := KnownCategory(tc.cat); got != tc.catOK {
			t.Errorf("KnownCategory(%q) = %v", tc.cat, got)
		}
		if got := KnownSubcategory(tc.cat, tc.sub); got != tc.subOK {
			t.Errorf("KnownSubcategory(%q, %q) = %v", tc.cat, tc.sub, got)
		}
	}
}

func TestCanonicalTagAndPolicy(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{" Smoke Fire ", "smoke-fire", true},
		{"diagnosis", "diagnosis", false}, // reserved
		{"a/b", "", false},
		{"", "", false},
	} {
		got, ok := CanonicalTag(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("CanonicalTag(%q) = %q,%v", tc.in, got, ok)
		}
	}
	for in, want := range map[string]string{"Abliterated": "uncensored", "aligned": "aligned", "nsfw": "", "": ""} {
		if got := CanonicalContentPolicy(in); got != want {
			t.Errorf("CanonicalContentPolicy(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalDoesNotTouchTheOriginal(t *testing.T) {
	orig := Taxonomy{Category: "Vision", Tags: []string{"PPE", "x"}}
	c := orig.Canonical(ModalityChat)
	if c.Category != "vision" || !slices.Equal(c.Tags, []string{"ppe", "x"}) {
		t.Fatalf("canonical = %+v", c)
	}
	if orig.Category != "Vision" || orig.Tags[0] != "PPE" {
		t.Fatalf("Canonical mutated the original: %+v", orig)
	}
}
