package hfhub_test

// THE MODEL INDEX (features/serving/local_models.feature §1-2).
//
// This is a thin client over a public HTTP API, so the tests are about the three things that are
// NOT thin: telling a gated repository apart from a broken one, refusing to invent a size or a
// quantisation, and the one trick the whole browse screen rests on - judging whether a model can
// run here by reading the first megabyte of it, rather than the whole four gigabytes.

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/hfhub"
)

func hub(t *testing.T, h http.HandlerFunc) *hfhub.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &hfhub.Client{Base: srv.URL}
}

func TestSearchReturnsWhatTheIndexSaysAndNothingMore(t *testing.T) {
	var gotQuery string
	c := hub(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		fmt.Fprint(w, `[
		  {"id":"unsloth/Qwen3-4B-GGUF","downloads":51234,"likes":88,"gated":false,
		   "tags":["gguf","text-generation","license:apache-2.0"],"pipeline_tag":"text-generation"},
		  {"id":"meta-llama/Llama-3.1-8B","downloads":9,"likes":1,"gated":"manual","tags":["gguf"]}
		]`)
	})
	got, err := c.Search(context.Background(), hfhub.Query{Text: "qwen3", Limit: 10})
	require.NoError(t, err)
	require.Len(t, got, 2)

	require.Equal(t, "unsloth/Qwen3-4B-GGUF", got[0].Repo)
	require.Equal(t, 51234, got[0].Downloads)
	require.False(t, got[0].Gated)
	require.Equal(t, "apache-2.0", got[0].License, "the licence is read from the tags and shown")

	// Gated is a tri-state at the far end and a plain fact here: it cannot be fetched without
	// access, whether the gate is automatic or manual.
	require.True(t, got[1].Gated)

	// The query asks for what we can actually serve and in an order worth reading.
	require.Contains(t, gotQuery, "search=qwen3")
	require.Contains(t, gotQuery, "filter=gguf")
	require.Contains(t, gotQuery, "limit=10")
}

func TestFilesCarryTheExactSizeAndDigestSoNothingIsEstimated(t *testing.T) {
	c := hub(t, func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.URL.Path, "/tree/main")
		require.Equal(t, "true", r.URL.Query().Get("recursive"))
		fmt.Fprint(w, `[
		  {"type":"file","path":"README.md","size":1234,"oid":"abc"},
		  {"type":"file","path":"Qwen3-4B-Q4_K_M.gguf","size":135,
		   "lfs":{"oid":"deadbeef","size":2503342080}},
		  {"type":"file","path":"Qwen3-4B-Q8_0.gguf","size":135,
		   "lfs":{"oid":"cafebabe","size":4280000000}},
		  {"type":"directory","path":"sub"}
		]`)
	})
	got, err := c.Files(context.Background(), "unsloth/Qwen3-4B-GGUF")
	require.NoError(t, err)

	// Only the servable weights, and the LFS size is the real one: the plain size field on an LFS
	// entry is the pointer file, which is a hundred-odd bytes and would be a catastrophic input
	// to a fitness decision.
	require.Len(t, got, 2)
	require.Equal(t, "Qwen3-4B-Q4_K_M.gguf", got[0].Path)
	require.Equal(t, int64(2503342080), got[0].Bytes)
	require.Equal(t, "deadbeef", got[0].SHA256)
	require.Equal(t, "Q4_K_M", got[0].Quant, "the quantisation comes from the file name")

	// Ordered smallest first, because that is the one most likely to fit.
	require.Less(t, got[0].Bytes, got[1].Bytes)
}

func TestAGatedRepositoryIsToldApartFromABrokenOne(t *testing.T) {
	gated := hub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Error-Code", "GatedRepo")
		w.Header().Set("WWW-Authenticate", "Bearer")
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := gated.Files(context.Background(), "meta-llama/Llama-3.1-8B-Instruct")
	require.ErrorIs(t, err, hfhub.ErrGated)
	require.Contains(t, err.Error(), "access", "the message does not say what to do about it")

	missing := hub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err = missing.Files(context.Background(), "nobody/nothing")
	require.ErrorIs(t, err, hfhub.ErrNotFound)

	limited := hub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err = limited.Files(context.Background(), "a/b")
	require.ErrorIs(t, err, hfhub.ErrRateLimited)
	require.Contains(t, err.Error(), "42", "a rate limit that does not say how long is not useful")
}

func TestATokenIsSentOnlyWhenThereIsOne(t *testing.T) {
	var seen []string
	h := func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		fmt.Fprint(w, `[]`)
	}
	srv := httptest.NewServer(http.HandlerFunc(h))
	defer srv.Close()

	anon := &hfhub.Client{Base: srv.URL}
	_, _ = anon.Search(context.Background(), hfhub.Query{})
	require.Equal(t, "", seen[0], "an anonymous browse sent an empty Authorization header")

	withTok := &hfhub.Client{Base: srv.URL, Token: "hf_secret"}
	_, _ = withTok.Search(context.Background(), hfhub.Query{})
	require.Equal(t, "Bearer hf_secret", seen[1])
}

// THE TRICK THE BROWSE SCREEN RESTS ON. The architecture block of a GGUF sits in the first
// megabyte, before the tokenizer arrays, so one ranged request answers "will this run here"
// about a file nobody has downloaded.
func TestAModelIsJudgedFromItsFirstMegabyteAlone(t *testing.T) {
	header := ggufHeaderBytes()
	var rangeAsked string
	var served int64
	c := hub(t, func(w http.ResponseWriter, r *http.Request) {
		rangeAsked = r.Header.Get("Range")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/2503342080", len(header)-1))
		w.WriteHeader(http.StatusPartialContent)
		n, _ := w.Write(header)
		served = int64(n)
	})
	shape, err := c.ShapeOf(context.Background(), "unsloth/Qwen3-4B-GGUF",
		"Qwen3-4B-Q4_K_M.gguf", 2503342080)
	require.NoError(t, err)

	require.Equal(t, 36, shape.Layers)
	require.Equal(t, 8, shape.KVHeads)
	require.Equal(t, 40960, shape.TrainCtx)
	require.Equal(t, int64(2503342080), shape.FileBytes, "the size comes from the index, not the range")

	require.True(t, strings.HasPrefix(rangeAsked, "bytes=0-"), "it did not ask for a range: %q", rangeAsked)
	require.Less(t, served, int64(2<<20), "it pulled more than a header's worth")
}

func TestAnUnreadableHeaderSaysSoRatherThanGuessing(t *testing.T) {
	c := hub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("this is not a gguf"))
	})
	_, err := c.ShapeOf(context.Background(), "a/b", "x.gguf", 1000)
	require.Error(t, err)
}

// A source that answers an entirely unexpected shape is an error, not an empty result: an empty
// list reads as "there are none", which is a different and wrong answer.
func TestNonsenseFromTheIndexIsAnErrorNotAnEmptyList(t *testing.T) {
	c := hub(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"not":"a list"}`)
	})
	_, err := c.Search(context.Background(), hfhub.Query{})
	require.Error(t, err)
}

// ggufHeaderBytes builds a real GGUF header for the Qwen3-4B shape the research measured.
func ggufHeaderBytes() []byte {
	var b []byte
	put := func(v any) {
		switch x := v.(type) {
		case string:
			b = binary.LittleEndian.AppendUint64(b, uint64(len(x)))
			b = append(b, x...)
		case uint32:
			b = binary.LittleEndian.AppendUint32(b, x)
		case uint64:
			b = binary.LittleEndian.AppendUint64(b, x)
		}
	}
	b = append(b, 'G', 'G', 'U', 'F')
	put(uint32(3))
	put(uint64(0)) // tensors
	kvs := []struct {
		k string
		v any
	}{
		{"general.architecture", "qwen3"},
		{"qwen3.block_count", uint32(36)},
		{"qwen3.context_length", uint32(40960)},
		{"qwen3.embedding_length", uint32(2560)},
		{"qwen3.attention.head_count", uint32(32)},
		{"qwen3.attention.head_count_kv", uint32(8)},
		{"qwen3.attention.key_length", uint32(128)},
		{"qwen3.attention.value_length", uint32(128)},
		{"qwen3.vocab_size", uint32(151936)},
	}
	put(uint64(len(kvs)))
	for _, e := range kvs {
		put(e.k)
		switch e.v.(type) {
		case string:
			put(uint32(8)) // string
			put(e.v)
		default:
			put(uint32(4)) // uint32
			put(e.v)
		}
	}
	return b
}

// A LIVE REPOSITORY LISTING FOUND THIS. A hand-rolled quant parser here silently dropped the
// label for IQ1_M, because it recognised only the labels the bits-per-weight table happened to
// price. The label and the price are different questions: a file whose cost we cannot compute
// still has a name the owner chose it by. The index now reuses the one parser that knows the
// whole family list.
func TestEveryQuantLabelARealRepositoryPublishesIsRead(t *testing.T) {
	c := hub(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[
		  {"type":"file","path":"Qwen3-4B-UD-IQ1_S.gguf","lfs":{"oid":"a","size":1084000000}},
		  {"type":"file","path":"Qwen3-4B-UD-IQ1_M.gguf","lfs":{"oid":"b","size":1138000000}},
		  {"type":"file","path":"Qwen3-4B-UD-IQ2_XXS.gguf","lfs":{"oid":"c","size":1256000000}},
		  {"type":"file","path":"Qwen3-4B-UD-Q4_K_XL.gguf","lfs":{"oid":"d","size":2600000000}},
		  {"type":"file","path":"Qwen3-4B-Q8_0.gguf","lfs":{"oid":"e","size":4280000000}},
		  {"type":"file","path":"Qwen3-4B-BF16.gguf","lfs":{"oid":"f","size":8050000000}}
		]`)
	})
	got, err := c.Files(context.Background(), "unsloth/Qwen3-4B-GGUF")
	require.NoError(t, err)
	require.Len(t, got, 6)
	for _, f := range got {
		require.NotEmpty(t, f.Quant, "%s: the label the owner chooses by was dropped", f.Path)
	}
	byPath := map[string]string{}
	for _, f := range got {
		byPath[f.Path] = f.Quant
	}
	require.Equal(t, "IQ1_M", byPath["Qwen3-4B-UD-IQ1_M.gguf"])
	require.Equal(t, "IQ2_XXS", byPath["Qwen3-4B-UD-IQ2_XXS.gguf"])
	require.Equal(t, "BF16", byPath["Qwen3-4B-BF16.gguf"])
}

func TestTheDownloadURLAndAuthHeaderAreWhatAFetchNeeds(t *testing.T) {
	// A fetch of a gated file needs the token; an ordinary one must not be handed an empty header,
	// which some servers read as a failed authentication rather than as no attempt.
	anon := &hfhub.Client{Base: "https://example.test"}
	require.Nil(t, anon.AuthHeader())
	tok := &hfhub.Client{Base: "https://example.test", Token: "hf_x"}
	require.Equal(t, map[string]string{"Authorization": "Bearer hf_x"}, tok.AuthHeader())

	// The URL is the ORIGIN's, never a CDN address: it is re-resolved on every attempt because
	// the redirect target is presigned and expires.
	require.Equal(t, "https://example.test/a/b/resolve/main/m.gguf", anon.DownloadURL("a/b", "m.gguf"))

	// A client with no base uses the public index rather than an empty host.
	require.Equal(t, hfhub.DefaultBase+"/a/b/resolve/main/m.gguf",
		(&hfhub.Client{}).DownloadURL("a/b", "m.gguf"))
}

func TestTheGatedFlagReadsEveryShapeTheIndexUses(t *testing.T) {
	// The index publishes false, "auto" or "manual". The last two differ in how an owner gets
	// access, not in whether they need it.
	for body, want := range map[string]bool{
		`[{"id":"a/b","gated":false}]`:    false,
		`[{"id":"a/b","gated":"auto"}]`:   true,
		`[{"id":"a/b","gated":"manual"}]`: true,
		`[{"id":"a/b","gated":true}]`:     true,
		`[{"id":"a/b"}]`:                  false,
		`[{"id":"a/b","gated":"false"}]`:  false,
	} {
		c := hub(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
		got, err := c.Search(context.Background(), hfhub.Query{})
		require.NoError(t, err, body)
		require.Len(t, got, 1, body)
		require.Equal(t, want, got[0].Gated, body)
	}
}

func TestAnUnreachableIndexIsSaidToBeUnreachable(t *testing.T) {
	c := &hfhub.Client{Base: "http://127.0.0.1:1"}
	_, err := c.Search(context.Background(), hfhub.Query{})
	require.ErrorIs(t, err, hfhub.ErrUnreachable)
	_, err = c.Files(context.Background(), "a/b")
	require.ErrorIs(t, err, hfhub.ErrUnreachable)
	_, err = c.ShapeOf(context.Background(), "a/b", "m.gguf", 1000)
	require.ErrorIs(t, err, hfhub.ErrUnreachable)
}

func TestACancelledReadStopsRatherThanReportingTheNetwork(t *testing.T) {
	c := hub(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[]`) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Search(ctx, hfhub.Query{})
	require.ErrorIs(t, err, context.Canceled)
}

func TestAnUpstreamErrorKeepsItsStatus(t *testing.T) {
	c := hub(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	_, err := c.Files(context.Background(), "a/b")
	require.ErrorIs(t, err, hfhub.ErrUpstream)
	require.Contains(t, err.Error(), "502", "an upstream failure that hides its status cannot be debugged")
}

// A rate limit with no Retry-After is still a rate limit, and must not be mistaken for an
// ordinary upstream error that a caller might retry immediately.
func TestARateLimitWithoutARetryAfterIsStillARateLimit(t *testing.T) {
	c := hub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := c.Search(context.Background(), hfhub.Query{})
	require.ErrorIs(t, err, hfhub.ErrRateLimited)
}
