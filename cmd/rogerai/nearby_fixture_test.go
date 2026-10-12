package main

// Fixtures for features/models/short_names_nearby.feature: a stand-in for the model hub's API that
// serves real GGUF-shaped files (never the real hub), REAL Edge fleet records under a real local
// authority, and a REAL standalone Tower (internal/localplane) over pinned TLS, joined the way an
// owner joins one. A test never reaches anything outside this process.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/lanmode"
	"rogerai.fm/roger/v6/internal/servedfile"
	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/tower"
)

// hubFile is one file in a stand-in repository. listSize and listSHA, when set, are what the
// listing claims instead of the truth: a huge quant that is never downloaded, or a file whose bytes
// changed after it was listed.
type hubFile struct {
	body     []byte
	listSize int64
	listSHA  string
}

type hubRepo struct {
	id        string
	downloads int
	license   string
	pipeline  string
	files     map[string]*hubFile
}

// fakeHub is the model hub's API, in-process.
type fakeHub struct {
	mu       sync.Mutex
	repos    []*hubRepo
	srv      *httptest.Server
	searches []string // every search text sent
	fetched  []string // every file whose bytes were asked for (a header read included)
}

func newFakeHub(t *testing.T) *fakeHub {
	h := &fakeHub{}
	h.srv = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *fakeHub) add(r *hubRepo) *hubRepo {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.repos = append(h.repos, r)
	return r
}

func (h *fakeHub) repo(id string) *hubRepo {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.repos {
		if strings.EqualFold(r.id, id) {
			return r
		}
	}
	return nil
}

// sha is the true digest of a file's bytes.
func (h *fakeHub) sha(repo, file string) string {
	sum := sha256.Sum256(h.repo(repo).files[file].body)
	return hex.EncodeToString(sum[:])
}

func (h *fakeHub) searched() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.searches...)
}

func (h *fakeHub) searchedFor(text string) bool {
	for _, s := range h.searched() {
		if s == text {
			return true
		}
	}
	return false
}

// downloaded reports whether a whole file was fetched (not just its header read).
func (h *fakeHub) downloaded(file string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, f := range h.fetched {
		if f == file+" whole" {
			return true
		}
	}
	return false
}

func (h *fakeHub) serve(w http.ResponseWriter, q *http.Request) {
	p := q.URL.Path
	switch {
	case p == "/api/models":
		text := strings.ToLower(q.URL.Query().Get("search"))
		h.mu.Lock()
		h.searches = append(h.searches, text)
		var hits []*hubRepo
		for _, r := range h.repos {
			if text == "" || strings.Contains(strings.ToLower(r.id), text) {
				hits = append(hits, r)
			}
		}
		h.mu.Unlock()
		sort.SliceStable(hits, func(i, j int) bool { return hits[i].downloads > hits[j].downloads })
		if n, err := strconv.Atoi(q.URL.Query().Get("limit")); err == nil && n < len(hits) {
			hits = hits[:n]
		}
		out := []map[string]any{}
		for _, r := range hits {
			tags := []string{"gguf"}
			if r.license != "" {
				tags = append(tags, "license:"+r.license)
			}
			out = append(out, map[string]any{"id": r.id, "downloads": r.downloads, "tags": tags, "pipeline_tag": r.pipeline})
		}
		_ = json.NewEncoder(w).Encode(out)
	case strings.HasPrefix(p, "/api/models/") && strings.HasSuffix(p, "/tree/main"):
		r := h.repo(strings.TrimSuffix(strings.TrimPrefix(p, "/api/models/"), "/tree/main"))
		if r == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		h.mu.Lock()
		out := []map[string]any{}
		for name, f := range r.files {
			sum := sha256.Sum256(f.body)
			sha, size := hex.EncodeToString(sum[:]), int64(len(f.body))
			if f.listSHA != "" {
				sha = f.listSHA
			}
			if f.listSize > 0 {
				size = f.listSize
			}
			out = append(out, map[string]any{"type": "file", "path": name, "size": 130, "lfs": map[string]any{"oid": sha, "size": size}})
		}
		h.mu.Unlock()
		_ = json.NewEncoder(w).Encode(out)
	default:
		repo, file, ok := strings.Cut(strings.TrimPrefix(p, "/"), "/resolve/main/")
		r := h.repo(repo)
		if !ok || r == nil || r.files[file] == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		h.mu.Lock()
		how := "whole" // a fetch, or a fetch resuming
		if rg := q.Header.Get("Range"); strings.HasPrefix(rg, "bytes=0-") && rg != "bytes=0-" {
			how = "header" // the fit check reading the header
		}
		h.fetched = append(h.fetched, file+" "+how)
		body := r.files[file].body
		h.mu.Unlock()
		http.ServeContent(w, q, file, time.Time{}, bytes.NewReader(body))
	}
}

// qwenHub is the stand-in hub most scenarios use: a popular repository, a lookalike from another
// author, two more that fit, one whose only file is far too big, and one with nothing servable.
func qwenHub(t *testing.T) *fakeHub {
	h := newFakeHub(t)
	h.add(&hubRepo{id: "Qwen/Qwen3-8B-GGUF", downloads: 912345, license: "apache-2.0", pipeline: "text-generation",
		files: map[string]*hubFile{"Qwen3-8B-Q4_K_M.gguf": {body: modelGGUF(4000)}, "Qwen3-8B-Q8_0.gguf": {body: modelGGUF(8000)}}})
	h.add(&hubRepo{id: "lookalike/Qwen3-8B-GGUF", downloads: 120, license: "mit", pipeline: "text-generation",
		files: map[string]*hubFile{"Qwen3-8B-Q4_K_M.gguf": {body: modelGGUF(4100)}}})
	h.add(&hubRepo{id: "third/Qwen3-8B-Chat-GGUF", downloads: 50, license: "apache-2.0", pipeline: "text-generation",
		files: map[string]*hubFile{"Qwen3-8B-Chat-Q4_K_M.gguf": {body: modelGGUF(4200)}}})
	h.add(&hubRepo{id: "fourth/Qwen3-8B-Mini-GGUF", downloads: 10, license: "apache-2.0", pipeline: "text-generation",
		files: map[string]*hubFile{"Qwen3-8B-Mini-Q4_K_M.gguf": {body: modelGGUF(4300)}}})
	h.add(&hubRepo{id: "big/Qwen3-8B-Huge-GGUF", downloads: 5000, license: "apache-2.0", pipeline: "text-generation",
		files: map[string]*hubFile{"Qwen3-8B-Huge-Q8_0.gguf": {body: modelGGUF(10), listSize: 900 << 30}}})
	h.add(&hubRepo{id: "proj/Qwen3-8B-Vision-GGUF", downloads: 3000, license: "apache-2.0", pipeline: "image-text-to-text",
		files: map[string]*hubFile{"mmproj-Qwen3-8B-F16.gguf": {body: modelGGUF(10)}}})
	return h
}

// nearbyRig is a machine with a model home (newModelRig), the stand-in hub, a market broker it can
// be told to list, and real Edge members and Towers that are added per scenario.
type nearbyRig struct {
	*modelRig
	hub      *fakeHub
	market   []client.Offer
	broker   *httptest.Server
	enrolled bool
}

func newNearbyRig(t *testing.T, ramMiB int) *nearbyRig {
	t.Helper()
	useTempConfig(t)
	r := &nearbyRig{modelRig: newModelRig(t, ramMiB), hub: qwenHub(t)}
	modelHubBase = r.hub.srv.URL
	r.broker = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.URL.Path != "/discover" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"offers": r.market})
	}))
	t.Cleanup(r.broker.Close)
	oldInteractive := edgeIsInteractive
	edgeIsInteractive = func() bool { return true }
	t.Cleanup(func() { edgeIsInteractive = oldInteractive; lanmode.SetPins(nil) })
	return r
}

func (r *nearbyRig) cfg() config {
	c := loadConfig()
	c.Broker = r.broker.URL
	return c
}

// do runs `roger model <args>` as the owner at a terminal that answers with in.
func (r *nearbyRig) do(t *testing.T, in string, args ...string) (string, error) {
	t.Helper()
	edgeStdin = strings.NewReader(in)
	r.out.Reset()
	err := cmdModel(r.cfg(), args)
	return r.out.String(), err
}

// hubFileOf is the servedfile report for a file the stand-in hub has, with its true digest.
func (r *nearbyRig) hubFileOf(repo, file, quant string) servedfile.File {
	return servedfile.File{Repo: repo, File: file, Quant: quant, SHA256: r.hub.sha(repo, file),
		Bytes: int64(len(r.hub.repo(repo).files[file].body))}
}

func (r *nearbyRig) enroll(t *testing.T) {
	if r.enrolled {
		return
	}
	runEdgeCLI(t, "edge", "authority", "local", "workshop")
	runEdgeCLI(t, "edge", "enroll", "workshop")
	r.enrolled = true
}

// member records an Edge member serving band from f (nil: an older member that names no file).
// proven is whether the serving probe answered; a member that only claims the band is not a source.
func (r *nearbyRig) member(t *testing.T, node, band string, f *servedfile.File, proven bool) {
	t.Helper()
	r.memberBands(t, node, proven, []string{band}, []*servedfile.File{f})
}

// memberBands records a member serving several bands at once, the first its latest.
func (r *nearbyRig) memberBands(t *testing.T, node string, proven bool, bands []string, files []*servedfile.File) {
	t.Helper()
	r.enroll(t)
	st, err := loadEdgeState()
	require.NoError(t, err)
	id := "n_" + node + strings.Repeat("0", 24-len(node))
	m := &store.EdgeModel{}
	for i, band := range bands {
		e := store.EdgeServed{Band: band, ID: band}
		if f := files[i]; f != nil {
			e.ID, e.Quant, e.File, e.SHA256, e.Bytes = f.Ref(), f.Quant, f.File, f.SHA256, f.Bytes
		}
		if i == 0 {
			m.Band, m.ID, m.Quant, m.File, m.SHA256, m.Bytes = e.Band, e.ID, e.Quant, e.File, e.SHA256, e.Bytes
		} else {
			m.Also = append(m.Also, e)
		}
	}
	obs := edge.Observation{Name: node, Kind: "host", Addr: "192.168.1.77:1", Fingerprint: strings.Repeat("cd", 32),
		Instances: []edge.Instance{{Name: "serve", Caps: []store.EdgeCap{{Name: string(edge.Serve), State: string(edge.Claimed)}},
			Bands: bands, Model: m}}}
	_, err = st.fleet.Observe(id, obs)
	require.NoError(t, err)
	if proven {
		require.NoError(t, st.fleet.RecordInstanceProbe(id, "serve", edge.Serve, true))
		require.NoError(t, st.fleet.RecordServeProbe(id, "serve", bands))
	}
	require.NoError(t, st.save())
}

// pinnedTower stands a real Tower over pinned TLS and joins it as the owner does. Stations are
// attached to the returned state.
func (r *nearbyRig) pinnedTower(t *testing.T) (*tower.State, string) {
	t.Helper()
	st, addr := joinTowerFixture(t)
	inv := towerInvite(t, st, addr, tower.JoinClient)
	captureStdout(t, func() { require.NoError(t, cmdJoin(loadConfig(), []string{inv.String()})) })
	return st, addr
}

// station attaches a station to a Tower serving band, reporting f as its file (nil: an older
// station that reports none).
func station(t *testing.T, st *tower.State, id, band string, f *servedfile.File) {
	t.Helper()
	_, err := st.AttachStation(id, "kh-"+id, []string{band})
	require.NoError(t, err)
	if f != nil {
		_, err = st.SetStationFiles(id, map[string]servedfile.File{band: *f})
		require.NoError(t, err)
	}
}

// changingStdin answers the prompts in order, running change just before handing out the first.
type changingStdin struct {
	answers string
	change  func()
	done    bool
}

func (c *changingStdin) Read(p []byte) (int, error) {
	if !c.done {
		c.done = true
		c.change()
	}
	if c.answers == "" {
		return 0, io.EOF
	}
	n := copy(p[:1], c.answers)
	c.answers = c.answers[n:]
	return n, nil
}
