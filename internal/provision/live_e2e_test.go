package provision_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"rogerai.fm/roger/v7/internal/provision"
)

// A REAL fetch from the real source. It is gated behind LIVE=1 and never runs in CI, for the same
// reason internal/harness keeps a live end-to-end test: a test server honours Range politely,
// and the thing this code actually has to survive is a 302 to a presigned CDN URL that expires,
// which no httptest handler will ever reproduce.
//
// It has already earned its place. On 2026-09-30 it fetched 84.1 MiB at 43 MiB/s, verified the
// digest the index published, and then resumed a deliberately truncated file from 42.1 MiB
// through that redirect - which is the one path most likely to be silently wrong.
func TestLiveHuggingFaceFetchAndResume(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("set LIVE=1")
	}
	const repo = "QuantFactory/SmolLM2-135M-GGUF"
	const file = "SmolLM2-135M.Q2_K.gguf"

	// The tree listing gives the exact size and the sha256 (lfs.oid).
	resp, err := http.Get("https://huggingface.co/api/models/" + repo + "/tree/main?recursive=true")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var tree []struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
		LFS  struct {
			OID  string `json:"oid"`
			Size int64  `json:"size"`
		} `json:"lfs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		t.Fatal(err)
	}
	var want struct {
		size int64
		sum  string
	}
	for _, e := range tree {
		if e.Path == file {
			want.size, want.sum = e.Size, e.LFS.OID
			if e.LFS.Size > 0 {
				want.size = e.LFS.Size
			}
		}
	}
	if want.sum == "" {
		t.Fatalf("no %s in the tree listing", file)
	}
	t.Logf("index says: %d bytes, sha256 %s", want.size, want.sum[:16])

	dest := filepath.Join(t.TempDir(), file)
	var seen provision.Progress
	f := &provision.Fetcher{
		FreeSpace:  func(string) (int64, bool) { return 1 << 40, true },
		OnProgress: func(p provision.Progress) { seen = p },
	}
	r := provision.Request{
		URL:  "https://huggingface.co/" + repo + "/resolve/main/" + file,
		Dest: dest, Bytes: want.size, SHA256: want.sum,
	}
	if err := f.Fetch(context.Background(), r); err != nil {
		t.Fatalf("live fetch failed: %v", err)
	}
	t.Logf("fetched %s at %.1f MiB/s", provision.BytesWord(seen.Done), seen.Rate/(1<<20))

	// Now prove the RESUME path against the real CDN: truncate and fetch again.
	if err := os.Truncate(dest, want.size/2); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dest, dest+".part"); err != nil {
		t.Fatal(err)
	}
	var second provision.Progress
	f.OnProgress = func(p provision.Progress) {
		if second.Total == 0 {
			second = p
		}
	}
	if err := f.Fetch(context.Background(), r); err != nil {
		t.Fatalf("live resume failed: %v", err)
	}
	if second.Done == 0 {
		t.Errorf("the resume started from zero against the real CDN")
	}
	t.Logf("resumed from %s of %s", provision.BytesWord(second.Done), provision.BytesWord(second.Total))
	fi, _ := os.Stat(dest)
	if fi.Size() != want.size {
		t.Errorf("final size %d, want %d", fi.Size(), want.size)
	}
}
