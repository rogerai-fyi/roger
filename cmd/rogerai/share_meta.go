package main

import (
	"flag"
	"strings"

	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/protocol"
)

// shareMetaFlags are `roger share`'s taxonomy flags (features/categories/share_cli.feature).
type shareMetaFlags struct {
	category, tuning, policy, license, baseModel *string
	tags                                         tagList
}

// tagList is a repeatable --tag.
type tagList []string

func (t *tagList) String() string     { return strings.Join(*t, ",") }
func (t *tagList) Set(v string) error { *t = append(*t, v); return nil }

func addShareMetaFlags(fs *flag.FlagSet) *shareMetaFlags {
	m := &shareMetaFlags{
		category:  fs.String("category", "", "declare what the model is for: <category>[/<subcategory>], e.g. vision/ocr (saved per model)"),
		tuning:    fs.String("tuning", "", "declare how the weights were made: base|instruct|finetune|merge|quantized|adapter"),
		policy:    fs.String("policy", "", "declare the content-policy grade of the weights: aligned|reduced-refusal|uncensored (broker screening applies regardless)"),
		license:   fs.String("license", "", "declare the model license (SPDX id, e.g. Apache-2.0)"),
		baseModel: fs.String("base-model", "", "declare the model this one derives from (e.g. Qwen/Qwen3-VL-8B-Instruct)"),
	}
	fs.Var(&m.tags, "tag", "declare a domain tag (repeatable, e.g. --tag manufacturing --tag ppe)")
	return m
}

var shareMetaFlagNames = map[string]bool{
	"category": true, "tag": true, "tuning": true, "policy": true, "license": true, "base-model": true,
}

// resolveShareMeta returns the taxonomy a headless share goes on air with. Any taxonomy flag
// REPLACES the model's saved declaration as a whole (so what is on air is exactly what the
// command line says) and is validated and saved first; with no flag the saved declaration is
// used; with neither, the detector's guess. A declaration always rides as "declared".
func resolveShareMeta(fs *flag.FlagSet, m *shareMetaFlags, model, modality string, caps []string) (protocol.Taxonomy, error) {
	given := false
	fs.Visit(func(f *flag.Flag) { given = given || shareMetaFlagNames[f.Name] })
	declared := loadConfig().Meta[model]
	if given {
		cat, sub := protocol.SplitCategory(*m.category)
		var err error
		declared, err = protocol.Taxonomy{Category: cat, Subcategory: sub, Tags: m.tags, Tuning: *m.tuning,
			ContentPolicy: *m.policy, License: *m.license, BaseModel: *m.baseModel}.Declare(modality)
		if err != nil {
			return protocol.Taxonomy{}, err
		}
		c := loadConfig()
		setShareMeta(&c, model, declared)
		if err := saveConfig(c); err != nil {
			return protocol.Taxonomy{}, err
		}
	}
	return declared.OnAir(modality, detect.InferTaxonomy(model, modality, caps)), nil
}

// setShareMeta records (or, for an empty taxonomy, removes) a model's declaration in c.
func setShareMeta(c *config, model string, t protocol.Taxonomy) {
	if !t.Declared() {
		delete(c.Meta, model)
		return
	}
	if c.Meta == nil {
		c.Meta = map[string]protocol.Taxonomy{}
	}
	c.Meta[model] = t
}
