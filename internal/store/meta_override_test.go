package store

import (
	"reflect"
	"testing"

	"rogerai.fm/roger/v6/internal/protocol"
)

// TestMetaOverrideCRUD covers the owner-authored taxonomy override on BOTH backends (the
// Postgres one when ROGERAI_TEST_DATABASE_URL is set, as the cover gate does):
// set/get round-trip of every field, overwrite, the per-(node,model) key, owner-scoped clear,
// and independence from the PRICE override for the same (node, model).
func TestMetaOverrideCRUD(t *testing.T) {
	for name, db := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			tx := protocol.Taxonomy{Category: "vision", Subcategory: "ocr", Tags: []string{"ppe", "manufacturing"},
				Tuning: "finetune", BaseModel: "Qwen/Qwen3-VL-8B", ContentPolicy: "aligned", License: "MIT",
				TaxonomySource: protocol.SourceDeclared}
			ov := MetaOverride{Owner: "owner-a", NodeID: "n1", Model: "qwen", Taxonomy: tx, UpdatedAt: 100}
			if err := db.SetMetaOverride(ov); err != nil {
				t.Fatalf("SetMetaOverride: %v", err)
			}
			got, ok, err := db.MetaOverride("n1", "qwen")
			if err != nil || !ok {
				t.Fatalf("MetaOverride ok=%v err=%v", ok, err)
			}
			if !reflect.DeepEqual(got, ov) {
				t.Fatalf("round-trip:\n got %+v\nwant %+v", got, ov)
			}
			if _, ok, _ := db.MetaOverride("n1", "other"); ok {
				t.Error("an override for another model was found")
			}
			if _, ok, _ := db.MetaOverride("n2", "qwen"); ok {
				t.Error("an override for another node was found")
			}

			// Overwrite (an upsert on node+model).
			ov.Taxonomy = protocol.Taxonomy{Category: "language", Subcategory: "code", TaxonomySource: protocol.SourceDeclared}
			ov.UpdatedAt = 200
			if err := db.SetMetaOverride(ov); err != nil {
				t.Fatalf("overwrite: %v", err)
			}
			if got, _, _ := db.MetaOverride("n1", "qwen"); !reflect.DeepEqual(got, ov) {
				t.Fatalf("overwrite not stored: %+v", got)
			}

			// A price override on the same (node, model) is a different row.
			if err := db.SetOfferOverride(OfferOverride{Owner: "owner-a", NodeID: "n1", Model: "qwen", PriceIn: 2, PriceOut: 3}); err != nil {
				t.Fatal(err)
			}

			// Owner-scoped clear: another owner clears nothing.
			if ok, err := db.ClearMetaOverride("owner-b", "n1", "qwen"); ok || err != nil {
				t.Fatalf("another owner's clear ok=%v err=%v, want false/nil", ok, err)
			}
			if _, ok, _ := db.MetaOverride("n1", "qwen"); !ok {
				t.Fatal("another owner's clear removed the override")
			}
			if ok, err := db.ClearMetaOverride("owner-a", "n1", "qwen"); !ok || err != nil {
				t.Fatalf("own clear ok=%v err=%v", ok, err)
			}
			if _, ok, _ := db.MetaOverride("n1", "qwen"); ok {
				t.Fatal("the override survived its owner's clear")
			}
			if ok, _ := db.ClearMetaOverride("owner-a", "n1", "qwen"); ok {
				t.Error("clearing twice reported a removal")
			}
			// ...and the price override is untouched by any of it.
			if p, ok, _ := db.OfferOverride("n1", "qwen"); !ok || p.PriceIn != 2 || p.PriceOut != 3 {
				t.Fatalf("the price override changed: %+v ok=%v", p, ok)
			}
		})
	}
}
