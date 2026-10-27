package hfhub_test

// THE WHOLE CHAIN, AGAINST THE REAL INDEX. Gated behind LIVE=1 and never run in CI, for the same
// reason internal/provision keeps one: the thing worth proving is that a browse, a file listing
// and a one-megabyte ranged read really do answer "can this machine run this" about a model
// nobody has downloaded. A test server cannot prove that, because a test server is not the thing
// that might change shape.

import (
	"context"
	"os"
	"testing"

	"rogerai.fm/roger/v7/internal/catalog"
	"rogerai.fm/roger/v7/internal/hfhub"
)

func TestLiveBrowseAndJudgeWithoutDownloading(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("set LIVE=1")
	}
	c := &hfhub.Client{Token: os.Getenv("HF_TOKEN")}
	ctx := context.Background()

	found, err := c.Search(ctx, hfhub.Query{Text: "qwen3", Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("the index returned nothing for a search that certainly matches")
	}
	for _, m := range found {
		t.Logf("  %-44s %8d downloads  gated=%v  licence=%q", m.Repo, m.Downloads, m.Gated, m.License)
	}

	// A repository that really carries several quantisations of one model.
	const repo = "unsloth/Qwen3-4B-GGUF"
	files, err := c.Files(ctx, repo)
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	if len(files) < 2 {
		t.Fatalf("expected several quantisations in %s, got %d", repo, len(files))
	}
	t.Logf("%s carries %d GGUF files", repo, len(files))

	// Judge the smallest AND a mid-size one, from one megabyte each.
	budget := int64(8) << 30 // pretend an 8 GiB machine
	for _, f := range files[:min(3, len(files))] {
		shape, err := c.ShapeOf(ctx, repo, f.Path, f.Bytes)
		if err != nil {
			t.Logf("  %-34s %8.2f GiB  %-8s -> %v", f.Path, float64(f.Bytes)/(1<<30), f.Quant, err)
			continue
		}
		full, ok := catalog.NeedAt(shape, shape.TrainCtx, 1, catalog.CacheF16, catalog.CacheF16)
		if !ok {
			t.Errorf("%s: a complete shape produced no need", f.Path)
			continue
		}
		maxCtx, fits := catalog.MaxCtxFor(shape, budget, 1, catalog.CacheF16, catalog.CacheF16)
		t.Logf("  %-34s %6.2f GiB  %-8s trained ctx %6d -> needs %6.2f GiB there; in 8 GiB the most is %6d (%v)",
			f.Path, float64(f.Bytes)/(1<<30), f.Quant, shape.TrainCtx,
			float64(full.Total)/(1<<30), maxCtx, fits)
		if f.SHA256 == "" {
			t.Errorf("%s: the index published no digest, so a download could not be verified", f.Path)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
