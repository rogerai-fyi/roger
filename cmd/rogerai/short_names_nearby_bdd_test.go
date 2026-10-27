package main

// THE RUNNER for features/models/short_names_nearby.feature (approved 2026-10-11), every scenario
// but the four @tui ones, which internal/tui runs against the real screen. Real Edge fleet records
// under a real local authority, a real standalone Tower over pinned TLS joined the way an owner
// joins one, the real console server, and a stand-in for the model hub's API serving GGUF-shaped
// files (the real hub is never called from a test).

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/detect"
	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/lanmode"
	"rogerai.fm/roger/v7/internal/localmodel"
	"rogerai.fm/roger/v7/internal/modelview"
	"rogerai.fm/roger/v7/internal/provision"
	"rogerai.fm/roger/v7/internal/servedfile"
	"rogerai.fm/roger/v7/internal/tower"
	"rogerai.fm/roger/v7/internal/webui"
)

const shortNamesFeature = "../../features/models/short_names_nearby.feature"

type shortNamesBDD struct {
	t        *testing.T
	r        *nearbyRig
	out      string
	err      error
	answers  string
	tw       *tower.State // the pinned Tower "lab"
	twAddr   string
	sent     *edge.ModelTask
	nearby   bool   // a nearby source was set up by the scenario
	pending  func() // runs while the owner is being asked (a mapping or a file that moves meanwhile)
	webBody  string
	webCode  int
	webToken string
	webURL   string
	expected string // the digest a source reported, before anything moved
	fitExe   func(string) string
}

func (s *shortNamesBDD) reset() {
	modelFitExe = s.fitExe // a scenario's fit tool never leaks into the next; the rig sets the rest
	s.r = newNearbyRig(s.t, 16384)
	s.r.hub.add(&hubRepo{id: "bartowski/SmolLM2-135M-Instruct-GGUF", downloads: 77000, license: "apache-2.0", pipeline: "text-generation",
		files: map[string]*hubFile{"SmolLM2-135M-Instruct-Q4_K_M.gguf": {body: modelGGUF(3000)}}})
	// The most downloaded of all, and never a suggestion: not a chat model, or only a tight fit.
	s.r.hub.add(&hubRepo{id: "rank/Rerank-GGUF", downloads: 990000, license: "apache-2.0", pipeline: "text-ranking",
		files: map[string]*hubFile{"Rerank-Q4_K_M.gguf": {body: modelGGUF(4000)}}})
	s.r.hub.add(&hubRepo{id: "tight/Tight-GGUF", downloads: 980000, license: "apache-2.0", pipeline: "text-generation",
		files: map[string]*hubFile{"Tight-Q8_0.gguf": {body: modelGGUF(4000), listSize: 25 << 29}}})
	s.out, s.err, s.answers, s.tw, s.twAddr, s.sent, s.nearby, s.pending = "", nil, "n\n", nil, "", nil, false, nil
	s.webBody, s.webCode, s.expected = "", 0, ""
	modelEdgeInvoke = func(_ config, node string, task edge.ModelTask) (edge.ModelResult, string, error) {
		t := task
		s.sent = &t
		return edge.ModelResult{Says: "starting"}, node, nil
	}
}

func (s *shortNamesBDD) qwen(quant string) servedfile.File {
	return s.r.hubFileOf("Qwen/Qwen3-8B-GGUF", "Qwen3-8B-"+quant+".gguf", quant)
}

func (s *shortNamesBDD) atlasServes(band string, f servedfile.File) {
	s.r.member(s.t, "atlas", band, &f, true)
	s.nearby = true
}

// lab is the pinned Tower "lab", stood and joined once per scenario.
func (s *shortNamesBDD) lab() *tower.State {
	if s.tw == nil {
		s.tw, s.twAddr = s.r.pinnedTower(s.t)
	}
	return s.tw
}

// label is the scenario's wording with the Tower's name for its address on this test machine.
func (s *shortNamesBDD) label(l string) string {
	if s.twAddr != "" {
		l = strings.ReplaceAll(l, "Tower lab", "Tower "+nearbyHost(s.twAddr))
	}
	return l
}

// impliedNearby is the source a scenario that names "qwen3-8b" without a Given relies on: atlas
// serving it from the popular repository.
func (s *shortNamesBDD) impliedNearby() {
	if !s.nearby {
		s.atlasServes("qwen3-8b", s.qwen("Q4_K_M"))
	}
}

// run runs the command as the owner typed it, asking for the full account: this file asserts facts
// in today's wording, which --verbose keeps (quiet_output.feature); the default has its own runner.
func (s *shortNamesBDD) run(line string) error {
	args := strings.Fields(strings.TrimPrefix(line, "roger model"))
	if s.pending != nil {
		p := s.pending
		edgeStdin = &changingStdin{answers: s.answers, change: p}
		s.r.out.Reset()
		s.err = cmdModel(s.r.cfg(), fullAccount(args))
		s.out = s.r.out.String()
		return nil
	}
	s.out, s.err = s.r.do(s.t, s.answers, args...)
	return nil
}

func (s *shortNamesBDD) has(want ...string) error {
	all := s.out
	if s.err != nil {
		all += "\nERROR: " + s.err.Error()
	}
	for _, w := range want {
		if !strings.Contains(all, w) {
			return fmt.Errorf("missing %q in:\n%s", w, all)
		}
	}
	return nil
}

func (s *shortNamesBDD) hasNot(bad ...string) error {
	all := s.out
	if s.err != nil {
		all += "\nERROR: " + s.err.Error()
	}
	for _, b := range bad {
		if strings.Contains(all, b) {
			return fmt.Errorf("unexpected %q in:\n%s", b, all)
		}
	}
	return nil
}

func (s *shortNamesBDD) matches(re string) error {
	if !regexp.MustCompile(re).MatchString(s.out) {
		return fmt.Errorf("no match for %s in:\n%s", re, s.out)
	}
	return nil
}

func (s *shortNamesBDD) nothingDownloaded() error {
	for _, r := range s.r.hub.repos {
		for name := range r.files {
			if s.r.hub.downloaded(name) {
				return fmt.Errorf("%s was downloaded", name)
			}
		}
	}
	if all, _ := s.r.home.List(); len(all) > 0 {
		return fmt.Errorf("something is on disk: %v", all[0].ID)
	}
	return nil
}

const smolRepo = "bartowski/SmolLM2-135M-Instruct-GGUF"

// smolOnDisk puts SmolLM2 on this disk at the given quantisations, fetched verified the way an
// owner fetches them; the hub also publishes Q8_0, so a --quant fetch of it can be shown.
func (s *shortNamesBDD) smolOnDisk(quants ...string) error {
	s.r.hub.repo(smolRepo).files["SmolLM2-135M-Instruct-Q8_0.gguf"] = &hubFile{body: modelGGUF(6000)}
	for _, q := range quants {
		if out, err := s.r.do(s.t, "", "get", smolRepo+":"+q, "--yes"); err != nil {
			return fmt.Errorf("get %s: %v\n%s", q, err, out)
		}
	}
	s.r.hub.fetched = nil
	return nil
}

func (s *shortNamesBDD) noTerminal() error {
	edgeIsInteractive = func() bool { return false }
	return nil
}

func (s *shortNamesBDD) hubUnreachable() error {
	modelHubBase = "http://127.0.0.1:1"
	return nil
}

func (s *shortNamesBDD) onlyRepos(ids ...string) {
	var keep []*hubRepo
	for _, r := range s.r.hub.repos {
		for _, id := range ids {
			if r.id == id {
				keep = append(keep, r)
			}
		}
	}
	s.r.hub.repos = keep
}

// changeBytes makes the hub serve other bytes, of the same size, under a file's name, while its
// listing still gives the digest it gave before.
func (s *shortNamesBDD) changeBytes(repo, file string) func() {
	return func() {
		f := s.r.hub.repo(repo).files[file]
		sum := sha256.Sum256(f.body)
		b := append([]byte(nil), f.body...)
		b[len(b)-1] ^= 0xff
		f.listSHA, f.body = hex.EncodeToString(sum[:]), b
	}
}

func (s *shortNamesBDD) steps(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		s.reset()
		return ctx, nil
	})
	sc.Step(`^a machine whose usable memory roger can measure$`, func() error { return nil })

	// ---- the sources ---------------------------------------------------------------------------
	sc.Step(`^this machine serves "([^"]*)"$`, s.thisMachineServes)
	sc.Step(`^Edge member "([^"]*)" serves "([^"]*)"$`, func(node, band string) error {
		f := s.qwen("Q4_K_M")
		s.r.member(s.t, node, band, &f, true)
		s.nearby = true
		return nil
	})
	sc.Step(`^Edge member "atlas" serves "([^"]*)" from a known repository, quantisation and digest$`, func(band string) error {
		s.atlasServes(band, s.qwen("Q4_K_M"))
		return nil
	})
	sc.Step(`^the pinned LAN Tower "lab" has a station serving "([^"]*)"$`, func(band string) error {
		f := s.r.hubFileOf("third/Qwen3-8B-Chat-GGUF", "Qwen3-8B-Chat-Q4_K_M.gguf", "Q4_K_M")
		station(s.t, s.lab(), "st-1", band, &f)
		return nil
	})
	sc.Step(`^a station joined to the pinned Tower "lab" serves "([^"]*)"$`, func(band string) error {
		f := s.r.hubFileOf("third/Qwen3-8B-Chat-GGUF", "Qwen3-8B-Chat-Q4_K_M.gguf", "Q4_K_M")
		station(s.t, s.lab(), "st-1", band, &f)
		return nil
	})
	sc.Step(`^a market station advertises "([^"]*)"$`, func(band string) error {
		s.r.market = append(s.r.market, client.Offer{Model: band, Online: true})
		return nil
	})
	sc.Step(`^no band is served on this machine, by an Edge member or by a pinned Tower$`, func() error { return nil })
	sc.Step(`^the name "qwen3-8b" is served by (.*)$`, func(src string) error {
		switch {
		case src == "this machine":
			return s.thisMachineServes("qwen3-8b")
		case strings.HasPrefix(src, "Edge member"):
			s.atlasServes("qwen3-8b", s.qwen("Q4_K_M"))
		default:
			f := s.qwen("Q4_K_M")
			station(s.t, s.lab(), "st-1", "qwen3-8b", &f)
		}
		return nil
	})
	sc.Step(`^atlas serves "qwen3-8b" at Q8_0$`, func() error {
		s.atlasServes("qwen3-8b", s.qwen("Q8_0"))
		return nil
	})
	sc.Step(`^Q8_0 of that repository does not fit this machine$`, func() error {
		s.r.hub.repo("Qwen/Qwen3-8B-GGUF").files["Qwen3-8B-Q8_0.gguf"].listSize = 900 << 30
		return nil
	})
	sc.Step(`^the file a nearby source maps "qwen3-8b" to is already on this disk with a matching digest$`, func() error {
		if _, err := s.r.do(s.t, "", "get", "Qwen/Qwen3-8B-GGUF:Q4_K_M", "--yes"); err != nil {
			return err
		}
		s.atlasServes("qwen3-8b", s.qwen("Q4_K_M"))
		s.r.hub.fetched = nil
		return nil
	})
	sc.Step(`^a model the owner placed on disk is listed as "qwen3-8b" by "roger model list"$`, func() error {
		if err := os.MkdirAll(s.r.home.Models(), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(s.r.home.Models(), "qwen3-8b.gguf"), modelGGUF(4000), 0o644); err != nil {
			return err
		}
		s.atlasServes("qwen3-8b", s.qwen("Q4_K_M"))
		out, err := s.r.do(s.t, "", "list")
		if err != nil || !strings.Contains(out, "qwen3-8b") {
			return fmt.Errorf("not listed: %v\n%s", err, out)
		}
		return nil
	})
	sc.Step(`^atlas maps "qwen3-8b" to one repository and file$`, func() error {
		s.atlasServes("qwen3-8b", s.qwen("Q4_K_M"))
		return nil
	})
	sc.Step(`^a station on the Tower "lab" maps "qwen3-8b" to a different repository or file$`, func() error {
		f := s.r.hubFileOf("lookalike/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
		station(s.t, s.lab(), "st-1", "qwen3-8b", &f)
		return nil
	})
	sc.Step(`^two nearby sources map "qwen3-8b" to different files$`, func() error {
		s.atlasServes("qwen3-8b", s.qwen("Q4_K_M"))
		f := s.r.hubFileOf("lookalike/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
		station(s.t, s.lab(), "st-1", "qwen3-8b", &f)
		return nil
	})
	sc.Step(`^atlas and pi-1 both serve "qwen3-8b" from the same repository, file and digest$`, func() error {
		f := s.qwen("Q4_K_M")
		s.r.member(s.t, "atlas", "qwen3-8b", &f, true)
		s.r.member(s.t, "pi-1", "qwen3-8b", &f, true)
		s.nearby = true
		return nil
	})
	sc.Step(`^no terminal is attached$`, s.noTerminal)
	sc.Step(`^nothing nearby serves "qwen3-8b"$`, func() error {
		s.atlasServes("gpt-oss-20b", s.qwen("Q4_K_M")) // something else is nearby; not this name
		return nil
	})
	sc.Step(`^the model hub cannot be reached$`, s.hubUnreachable)
	sc.Step(`^the hub returns exactly one repository matching "qwen3-8b"$`, func() error {
		s.onlyRepos("Qwen/Qwen3-8B-GGUF")
		return nil
	})
	sc.Step(`^the hub returns no repository matching "qwen3-8b"$`, func() error {
		s.onlyRepos()
		return nil
	})
	sc.Step(`^a matching repository has no servable weights, or an architecture this runtime cannot load$`, func() error {
		return nil // proj/Qwen3-8B-Vision-GGUF carries only a vision projector
	})
	sc.Step(`^Edge member "atlas" serves "qwen3-8b" and "gpt-oss-20b"$`, func() error {
		fits, huge := s.qwen("Q4_K_M"), s.r.hubFileOf("big/Qwen3-8B-Huge-GGUF", "Qwen3-8B-Huge-Q8_0.gguf", "Q8_0")
		s.r.memberBands(s.t, "atlas", true, []string{"qwen3-8b", "gpt-oss-20b"}, []*servedfile.File{&fits, &huge})
		s.nearby = true
		return nil
	})
	sc.Step(`^only "qwen3-8b" fits this machine$`, func() error { return nil }) // gpt-oss-20b is the 900 GiB file
	sc.Step(`^nothing is served nearby$`, func() error { return nil })
	sc.Step(`^roger cannot read this machine's memory$`, func() error {
		modelHW = func() detect.LocalHW { return detect.LocalHW{} }
		s.impliedNearby()
		return nil
	})
	sc.Step(`^roger's estimate says the resolved file fits$`, func() error {
		s.impliedNearby()
		return nil
	})
	sc.Step(`^the runtime's own fit check on this machine says it does not$`, func() error {
		modelFitExe = func(string) string { return os.Args[0] }
		modelServeEnv = map[string]string{"FAKE_LLAMA": "1", "FAKE_FIT": "no"}
		return nil
	})
	sc.Step(`^a disk budget is set$`, func() error {
		if _, err := s.r.do(s.t, "", "get", "Qwen/Qwen3-8B-GGUF:Q8_0", "--yes"); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(s.r.home.Models(), "mine.gguf"), modelGGUF(10), 0o644); err != nil {
			return err
		}
		all, _ := s.r.home.List()
		var used int64
		for _, m := range all {
			used += m.Bytes
		}
		s.atlasServes("house-qwen", s.qwen("Q4_K_M")) // a name not on this disk
		return s.r.home.SetSettings(localmodel.Settings{DiskBudget: used + 100})
	})
	sc.Step(`^the machine has no internet but Edge members and a pinned Tower are reachable$`, func() error {
		s.atlasServes("qwen3-8b", s.qwen("Q4_K_M"))
		f := s.r.hubFileOf("third/Qwen3-8B-Chat-GGUF", "Qwen3-8B-Chat-Q4_K_M.gguf", "Q4_K_M")
		station(s.t, s.lab(), "st-1", "smollm2-135m-instruct", &f)
		return s.hubUnreachable()
	})
	sc.Step(`^a market station advertises "qwen3-8b" and claims a repository of its choosing$`, func() error {
		s.r.market = []client.Offer{{Model: "qwen3-8b", Online: true, FileBy: "station",
			File: &servedfile.File{Repo: "evil/Qwen3-8B-GGUF", File: qwenQ4, Quant: "Q4_K_M", SHA256: strings.Repeat("ab", 32)}}}
		return nil
	})
	sc.Step(`^nothing else nearby serves "qwen3-8b"$`, func() error { return nil })
	sc.Step(`^an advertisement on the LAN claims to be an Edge member serving "qwen3-8b"$`, func() error {
		f := s.qwen("Q4_K_M")
		s.r.member(s.t, "ghost", "qwen3-8b", &f, false) // what it claims is on record; nothing proved it
		return nil
	})
	sc.Step(`^it does not present a member certificate under the owner's root$`, func() error { return nil })
	sc.Step(`^the Tower "lab" answers with a key that does not match its pin$`, func() error {
		f := s.qwen("Q4_K_M")
		station(s.t, s.lab(), "st-1", "qwen3-8b", &f)
		other, _ := joinTowerFixture(s.t)
		wrong, err := other.JoinFingerprint()
		if err != nil {
			return err
		}
		// And a Tower added without a pin at all: plain http, on the LAN's word, also serving the name.
		plain := newRealTower(s.t)
		if err := plain.admit(client.LoadOrCreateUserKey().Public().(ed25519.PublicKey)); err != nil {
			return err
		}
		if err := plain.attach("st-2", "qwen3-8b"); err != nil {
			return err
		}
		if _, err := plain.st.SetStationFiles("st-2", map[string]servedfile.File{"qwen3-8b": f}); err != nil {
			return err
		}
		c := loadConfig()
		c.LanTowers = append(c.LanTowers, plain.addr())
		c.LanTowerPins[s.twAddr] = wrong
		lanmode.SetPins(c.LanTowerPins)
		return saveConfig(c)
	})
	sc.Step(`^the owner was shown "qwen3-8b" as one file$`, func() error {
		s.atlasServes("qwen3-8b", s.qwen("Q4_K_M"))
		return nil
	})
	sc.Step(`^before the fetch starts, the source now maps "qwen3-8b" to a different file$`, func() error {
		g := s.r.hubFileOf("lookalike/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
		s.answers, s.pending = "y\nn\n", func() { s.r.member(s.t, "atlas", "qwen3-8b", &g, true) }
		return s.run("roger model get qwen3-8b")
	})
	sc.Step(`^atlas reports a digest for "qwen3-8b" that the downloaded file does not match$`, func() error {
		s.atlasServes("qwen3-8b", s.qwen("Q4_K_M"))
		s.expected = s.qwen("Q4_K_M").SHA256
		s.changeBytes("Qwen/Qwen3-8B-GGUF", qwenQ4)()
		s.answers = "y\n"
		return s.run("roger model serve qwen3-8b")
	})
	sc.Step(`^the hub lists "qwen3-8b" from its original publisher and a lookalike name from another author$`, func() error {
		return s.run("roger model serve qwen3-8b")
	})
	sc.Step(`^the owner picked a file whose sha256 the listing gave$`, func() error {
		s.answers = "1\ny\n"
		return nil
	})
	sc.Step(`^the repository now serves different bytes under that file name$`, func() error {
		s.pending = s.changeBytes("Qwen/Qwen3-8B-GGUF", qwenQ4)
		return nil
	})
	sc.Step(`^the download finishes$`, func() error { return s.run("roger model serve qwen3-8b") })
	sc.Step(`^the picked file is larger than this machine's free disk or much larger than the others listed$`, func() error {
		modelFetcher = func() *provision.Fetcher {
			return &provision.Fetcher{FreeSpace: func(string) (int64, bool) { return 2048, true }, OnProgress: modelProgress}
		}
		s.answers = "y\n"
		return s.run("roger model get qwen3-8b-mini")
	})
	sc.Step(`^an Edge instruction arrives naming a model only by a short name$`, func() error {
		raw, _ := json.Marshal(edge.ModelTask{Action: edge.ModelServe, Model: "qwen3-8b"})
		task, refused := edge.ParseModelTask(raw)
		if refused != "" {
			s.err = fmt.Errorf("%s", refused)
			return nil
		}
		_, why := edgeModelServe(task)
		s.err = fmt.Errorf("%s", why)
		return nil
	})

	// ---- founder ruling 2026-10-11: same-repo quants on disk are picked by the quant rule --------
	sc.Step(`^"smollm2-135m-instruct" is on this disk at Q4_K_M and at Q8_0 of one repository$`, func() error {
		return s.smolOnDisk("Q4_K_M", "Q8_0")
	})
	sc.Step(`^"smollm2-135m-instruct" is on this disk at Q4_K_M only$`, func() error { return s.smolOnDisk("Q4_K_M") })
	sc.Step(`^"smollm2-135m-instruct" is on this disk from two different repositories$`, func() error {
		s.r.hub.add(&hubRepo{id: "other/SmolLM2-135M-Instruct-GGUF", downloads: 12, license: "apache-2.0", pipeline: "text-generation",
			files: map[string]*hubFile{"SmolLM2-135M-Instruct-Q4_K_M.gguf": {body: modelGGUF(3100)}}})
		for _, ref := range []string{smolRepo + ":Q4_K_M", "other/SmolLM2-135M-Instruct-GGUF:Q4_K_M"} {
			if out, err := s.r.do(s.t, "", "get", ref, "--yes"); err != nil {
				return fmt.Errorf("get %s: %v\n%s", ref, err, out)
			}
		}
		s.r.hub.fetched = nil
		return nil
	})
	sc.Step(`^the owner runs "(roger model [^"]*)" and picks (\d+)$`, func(line, n string) error {
		s.answers = n + "\n"
		return s.run(line)
	})
	sc.Step(`^roger serves the (Q4_K_M|Q8_0) file(?:, the one the quantisation rule picks for this machine)?$`, func(q string) error {
		return s.has(`on the air locally: "smollm2-135m-instruct" - ` + smolRepo + ":" + q)
	})
	sc.Step(`^it says it chose Q4_K_M as the best fit here, that Q8_0 is also on disk, and how to pick it$`, func() error {
		return s.has("using "+smolRepo+":Q4_K_M (best fit here)", "also on disk: Q8_0", "--quant Q8_0")
	})
	sc.Step(`^nothing is downloaded from the model hub$`, func() error {
		for _, f := range []string{"SmolLM2-135M-Instruct-Q4_K_M.gguf", "SmolLM2-135M-Instruct-Q8_0.gguf"} {
			if s.r.hub.downloaded(f) {
				return fmt.Errorf("%s was downloaded:\n%s", f, s.out)
			}
		}
		return nil
	})
	sc.Step(`^roger says it is already on this machine, naming the file the rule picks and the other quantisation$`, func() error {
		return s.has("already on this machine: "+smolRepo+":Q4_K_M", "also on disk: Q8_0")
	})
	sc.Step(`^roger says "(already on this machine: [^"]*)"$`, func(want string) error { return s.has(want) })
	sc.Step(`^the command succeeds$`, func() error {
		if s.err != nil {
			return fmt.Errorf("it failed: %v\n%s", s.err, s.out)
		}
		return nil
	})
	sc.Step(`^the Q8_0 file of that repository is fetched and on this disk$`, func() error {
		if _, err := s.r.home.Find(smolRepo + ":Q8_0"); err != nil {
			return fmt.Errorf("Q8_0 not on disk: %v\nout: %s\nerr: %v", err, s.out, s.err)
		}
		return nil
	})
	sc.Step(`^roger removes the (Q4_K_M|Q8_0) file(?:, says Q8_0 is also on disk and how to pick it)?$`, func(q string) error {
		if err := s.has("removed " + smolRepo + ":" + q); err != nil {
			return err
		}
		if _, err := s.r.home.Find(smolRepo + ":" + q); err == nil {
			return fmt.Errorf("%s is still on disk after rm", q)
		}
		if q == "Q4_K_M" {
			return s.has("also on disk: Q8_0", "--quant Q8_0")
		}
		return nil
	})
	sc.Step(`^the Q8_0 file of that repository is still on this disk$`, func() error {
		if _, err := s.r.home.Find(smolRepo + ":Q8_0"); err != nil {
			return fmt.Errorf("Q8_0 is gone: %v\n%s", err, s.out)
		}
		return nil
	})
	sc.Step(`^nothing of that name is on this disk$`, func() error {
		if _, err := s.r.home.Find("smollm2-135m-instruct"); err == nil {
			return fmt.Errorf("something named smollm2-135m-instruct is still on disk:\n%s", s.out)
		}
		return nil
	})
	sc.Step(`^both repositories are listed, numbered$`, func() error {
		return s.has("1) "+smolRepo+":Q4_K_M", "2) other/SmolLM2-135M-Instruct-GGUF:Q4_K_M")
	})
	sc.Step(`^roger serves the file from the second repository listed$`, func() error {
		return s.has(`on the air locally: "smollm2-135m-instruct" - other/SmolLM2-135M-Instruct-GGUF:Q4_K_M`)
	})
	sc.Step(`^roger refuses, listing both, and serves nothing$`, func() error {
		if s.err == nil {
			return fmt.Errorf("it did not refuse:\n%s", s.out)
		}
		if err := s.has(smolRepo+":Q4_K_M", "other/SmolLM2-135M-Instruct-GGUF:Q4_K_M"); err != nil {
			return err
		}
		return s.hasNot("on the air locally")
	})

	// ---- what the owner runs -------------------------------------------------------------------
	sc.Step(`^the owner runs "(roger model [^"]*)"$`, func(line string) error {
		if m := regexp.MustCompile(`^roger model serve ([^,]+), (.*)$`).FindStringSubmatch(line); m != nil {
			return s.orderCase(m[1], m[2])
		}
		switch {
		case strings.HasPrefix(line, "roger model serve ./models/"):
			p := filepath.Join(s.t.TempDir(), "models", "my-model.gguf")
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(p, modelGGUF(4000), 0o644); err != nil {
				return err
			}
			return s.run("roger model serve " + p)
		case strings.Contains(line, "qwen3-8b") && !strings.Contains(line, "nearby"):
			s.impliedNearbyFor(line)
		}
		return s.run(line)
	})
	sc.Step(`^roger runs "(roger model [^"]*)"$`, s.run)
	sc.Step(`^the owner runs "roger model serve qwen3-8b" and nothing nearby serves it$`, func() error {
		return s.run("roger model serve qwen3-8b")
	})
	sc.Step(`^the owner runs "roger model serve" with no name$`, func() error {
		return s.run("roger model serve")
	})
	sc.Step(`^roger runs "roger model serve" with no name, with or without --yes$`, func() error {
		if err := s.run("roger model serve"); err != nil {
			return err
		}
		first := s.out
		if err := s.run("roger model serve --yes"); err != nil {
			return err
		}
		s.out = first + "\n--- with --yes ---\n" + s.out
		return nil
	})
	sc.Step(`^the owner asks which file "qwen3-8b" is$`, func() error { return s.run("roger model nearby") })
	sc.Step(`^the owner confirms serving "qwen3-8b" as served by atlas$`, func() error {
		s.atlasServes("qwen3-8b", s.qwen("Q4_K_M"))
		s.answers = "y\n"
		return s.run("roger model serve qwen3-8b")
	})
	sc.Step(`^the file finishes downloading$`, func() error { return nil })
	sc.Step(`^roger lists hub matches$`, func() error { return s.run("roger model serve qwen3-8b") })
	sc.Step(`^the owner serves a model resolved from a nearby name or the hub$`, func() error {
		s.impliedNearby()
		s.answers = "y\n"
		return s.run("roger model serve qwen3-8b")
	})
	sc.Step(`^the owner serves it$`, func() error {
		s.answers = "y\n"
		return s.run("roger model serve qwen3-8b")
	})
	sc.Step(`^fetching the resolved file would exceed it$`, func() error { return s.run("roger model serve house-qwen") })
	sc.Step(`^the owner opens the get-a-model view in the console$`, func() error {
		s.impliedNearby()
		srv := webui.New(nil, webui.Options{Models: modelWebHooks(s.r.cfg())})
		hs := httptest.NewServer(srv.Handler())
		s.t.Cleanup(hs.Close)
		s.webURL, s.webToken = hs.URL, srv.Token()
		req, _ := http.NewRequest(http.MethodGet, hs.URL+"/api/models/nearby", nil)
		req.Header.Set("X-Roger-Token", s.webToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		var b strings.Builder
		_, _ = fmt.Fprint(&b, readAll(resp))
		s.webCode, s.webBody = resp.StatusCode, b.String()
		return nil
	})

	// ---- what is said ------------------------------------------------------------------------
	sc.Step(`^all three names are listed, each with its source and whose word the mapping is$`, func() error {
		return s.has("qwen3-8b", "this machine's own serve", "gpt-oss-20b", "your Edge member atlas",
			"smollm2-135m-instruct", s.label("a station on the Tower lab (its word)"))
	})
	sc.Step(`^each shows the repository, quantisation and size its source says it is serving$`, func() error {
		return s.has("Qwen/Qwen3-8B-GGUF:Q8_0  8.2 KiB", "Qwen/Qwen3-8B-GGUF:Q4_K_M  4.3 KiB", "third/Qwen3-8B-Chat-GGUF:Q4_K_M  4.5 KiB")
	})
	sc.Step(`^"llama-3.1-8b" is listed as "on the market"$`, func() error {
		return s.matches(`llama-3\.1-8b\s+on the market`)
	})
	sc.Step(`^no repository or file is shown for it$`, func() error {
		for _, l := range strings.Split(s.out, "\n") {
			if strings.Contains(l, "llama-3.1-8b") && strings.Contains(l, "/") {
				return fmt.Errorf("a file is shown: %q", l)
			}
		}
		return nil
	})
	sc.Step(`^choosing it looks the name up on the model hub instead$`, func() error {
		if err := s.run("roger model serve llama-3.1-8b"); err != nil {
			return err
		}
		return s.has("llama-3.1-8b is on the market, but a market station never chooses a file", "looking it up on the model hub")
	})
	sc.Step(`^roger says nothing is served nearby$`, func() error { return s.has("nothing is served nearby") })
	sc.Step(`^it says how to find a model: roger model search <words>, or roger model serve <name>$`, func() error {
		return s.has("roger model search <words>", "roger model serve <name>")
	})
	sc.Step(`^the mapping is labelled as (.*)$`, func(label string) error { return s.has(s.label(strings.TrimSpace(label))) })
	sc.Step(`^the record atlas reports names the repository, file, quantisation and sha256 it serves$`, func() error {
		st, err := loadEdgeState()
		if err != nil {
			return err
		}
		n, ok, err := st.fleet.Get("n_atlas" + strings.Repeat("0", 19))
		if err != nil || !ok {
			return fmt.Errorf("no record for atlas: %v", err)
		}
		m := edge.ModelOf(n)
		f := s.qwen("Q4_K_M")
		if m == nil || m.ID != "Qwen/Qwen3-8B-GGUF:Q4_K_M" || m.File != f.File || m.Quant != "Q4_K_M" || m.SHA256 != f.SHA256 {
			return fmt.Errorf("the record is %+v", m)
		}
		return nil
	})
	sc.Step(`^the Tower's offer names the repository, file, quantisation and sha256 the station reported$`, func() error {
		bands, _ := client.LANDiscover(context.Background(), client.LANInputs{Towers: []string{s.twAddr}})
		for _, b := range bands {
			if b.Model == "smollm2-135m-instruct" && len(b.Sources) == 1 && b.Sources[0].File != nil &&
				b.Sources[0].File.Repo == "third/Qwen3-8B-Chat-GGUF" && b.Sources[0].File.File == "Qwen3-8B-Chat-Q4_K_M.gguf" &&
				b.Sources[0].File.Quant == "Q4_K_M" && len(b.Sources[0].File.SHA256) == 64 {
				return nil
			}
		}
		return fmt.Errorf("the offer names no file: %+v", bands)
	})
	sc.Step(`^the Tower marks those fields as the station's report$`, func() error {
		sts, err := s.tw.Stations()
		if err != nil {
			return err
		}
		for _, st := range sts {
			if f, ok := st.Files["smollm2-135m-instruct"]; ok && f.Repo == "third/Qwen3-8B-Chat-GGUF" {
				return nil // the Tower's /discover carries it as file_by "station" (internal/localplane)
			}
		}
		return fmt.Errorf("the Tower holds no station report")
	})
	sc.Step(`^roger says which file "qwen3-8b" is, as served by atlas$`, func() error {
		return s.has("qwen3-8b = Qwen/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas")
	})
	sc.Step(`^it states the byte count and this machine's fit verdict for that file$`, func() error {
		return s.has("Qwen3-8B-Q4_K_M.gguf  4.3 KiB", "fits at 8K context")
	})
	sc.Step(`^it asks before fetching, naming the source of the bytes as the model hub$`, func() error {
		if err := s.has("the bytes come from the model hub", "fetch 4.3 KiB? [y/N]", "not fetched. Nothing changed."); err != nil {
			return err
		}
		return s.nothingDownloaded()
	})
	sc.Step(`^its sha256 is checked against the digest atlas reported$`, func() error {
		m, err := s.r.home.Find("Qwen/Qwen3-8B-GGUF:Q4_K_M")
		if err != nil {
			return err
		}
		if m.SHA256 != s.qwen("Q4_K_M").SHA256 {
			return fmt.Errorf("sha %s", m.SHA256)
		}
		return s.has("checked against the digest your Edge member atlas reported", "fetched and verified")
	})
	sc.Step(`^a mismatch is never served, naming the expected and actual digest$`, func() error {
		want := s.qwen("Q4_K_M").SHA256
		if _, err := s.r.do(s.t, "", "rm", "Qwen/Qwen3-8B-GGUF:Q4_K_M", "--yes"); err != nil {
			return err
		}
		s.changeBytes("Qwen/Qwen3-8B-GGUF", qwenQ4)()
		s.answers = "y\n"
		modelUntilDone = func(context.Context) {}
		_ = s.run("roger model serve qwen3-8b")
		if err := s.has("expected "+want[:12], "digest ", "as your Edge member atlas reported", "nothing was served"); err != nil {
			return err
		}
		return s.hasNot("on the air")
	})
	sc.Step(`^roger says atlas's Q8_0 will not fit here, with the arithmetic$`, func() error {
		return s.has("your Edge member atlas's Q8_0 will not fit here: will not fit", "weights 900.0 GiB")
	})
	sc.Step(`^it offers the quantisation of the same repository that the existing rule picks for this machine$`, func() error {
		return s.has("offering Q4_K_M of the same repository", "qwen3-8b = Qwen/Qwen3-8B-GGUF:Q4_K_M from the model hub's listing")
	})
	sc.Step(`^it says that file is not the one atlas serves, so its digest comes from the hub listing$`, func() error {
		return s.has("that file is not the one your Edge member atlas serves, so its digest comes from the model hub's listing")
	})
	sc.Step(`^roger resolves to that quantisation of the repository nearby sources name for "qwen3-8b"$`, func() error {
		return s.has("qwen3-8b = Qwen/Qwen3-8B-GGUF:Q4_K_M")
	})
	sc.Step(`^a quantisation the repository does not publish is refused, naming the ones it does$`, func() error {
		_ = s.run("roger model serve qwen3-8b:Q2_K")
		return s.has(`no servable file "Q2_K" here`, "Qwen3-8B-Q4_K_M.gguf", "Qwen3-8B-Q8_0.gguf")
	})
	sc.Step(`^nothing is fetched$`, func() error {
		if s.r.hub.downloaded(qwenQ4) {
			return fmt.Errorf("fetched")
		}
		return nil
	})
	sc.Step(`^roger serves the file on disk$`, func() error { return s.has(`on the air locally: "qwen3-8b" - Qwen/Qwen3-8B-GGUF:Q4_K_M`) })
	sc.Step(`^roger serves the owner's own file$`, func() error { return s.has(`on the air locally: "qwen3-8b" - qwen3-8b`) })
	sc.Step(`^it says a nearby source also serves a "qwen3-8b" and how to serve that one instead$`, func() error {
		return s.has(`a nearby source also serves a "qwen3-8b": Qwen/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas`,
			"roger model serve Qwen/Qwen3-8B-GGUF:Q4_K_M serves that one instead")
	})
	sc.Step(`^"qwen3-8b" resolves through the nearby mapping exactly as it does for serve$`, func() error {
		if s.sent != nil {
			if s.sent.Model != "Qwen/Qwen3-8B-GGUF:Q4_K_M" {
				return fmt.Errorf("sent %+v", s.sent)
			}
		}
		return s.has("Qwen/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas")
	})
	sc.Step(`^the instruction sent to pi-1 names the exact repository, file and sha256$`, func() error {
		if s.sent == nil || s.sent.Model != "Qwen/Qwen3-8B-GGUF:Q4_K_M" || s.sent.SHA256 != s.qwen("Q4_K_M").SHA256 {
			return fmt.Errorf("sent %+v", s.sent)
		}
		return nil
	})
	sc.Step(`^pi-1 fetches only that exact reference, as the approved rule for remote serving requires$`, func() error {
		raw, _ := json.Marshal(s.sent)
		task, refused := edge.ParseModelTask(raw)
		if refused != "" {
			return fmt.Errorf("the member would refuse it: %s", refused)
		}
		if _, _, ok := edge.ExactIndexID(task.Model); !ok {
			return fmt.Errorf("%q is not an exact reference", task.Model)
		}
		return nil
	})
	sc.Step(`^roger lists both mappings with their sources, sizes and verdicts$`, func() error {
		if err := s.matches(`1\) Qwen/Qwen3-8B-GGUF:Q4_K_M\s+4\.3 KiB\s+fits[^\n]*as served by your Edge member atlas`); err != nil {
			return err
		}
		return s.matches(`2\) lookalike/Qwen3-8B-GGUF:Q4_K_M\s+4\.4 KiB\s+fits[^\n]*as served by a station on the Tower`)
	})
	sc.Step(`^it asks the owner to choose one by number$`, func() error { return s.has("pick 1-2, or n: ") })
	sc.Step(`^nothing is fetched until the owner chooses$`, s.nothingDownloaded)
	sc.Step(`^roger shows one mapping, naming both sources$`, func() error {
		if err := s.has("as served by your Edge member atlas and your Edge member pi-1"); err != nil {
			return err
		}
		return s.hasNot("different files")
	})
	sc.Step(`^it refuses, listing both mappings$`, func() error {
		if s.err == nil {
			return fmt.Errorf("it did not refuse")
		}
		return s.has("Qwen/Qwen3-8B-GGUF:Q4_K_M", "lookalike/Qwen3-8B-GGUF:Q4_K_M", "there is no terminal to choose on")
	})
	sc.Step(`^it says how to name one exactly: the full repository reference$`, func() error {
		return s.has("name one exactly: roger model serve <owner/repo>:<quant>")
	})
	sc.Step(`^it is resolved as (.*)$`, func(kind string) error {
		switch kind {
		case "a file path":
			return s.has(`on the air locally: "my-model"`)
		case "a hub reference, the author's word":
			return s.has("source: HuggingFace - the author's word", "fetch it? [y/N]")
		case "the file on disk":
			if err := s.hasNot("as served by", "[y/N]"); err != nil {
				return err
			}
			return s.has("on the air locally")
		case "the nearby mapping, shown before fetching":
			if err := s.has("qwen3-8b = Qwen/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas; fetch"); err != nil {
				return err
			}
			return s.nothingDownloaded()
		case "a hub lookup the owner picks from (section 5)":
			return s.has("qwen3-8b is not served nearby; looking it up on the model hub", "pick 1-3")
		}
		return fmt.Errorf("unknown kind %q", kind)
	})
	sc.Step(`^roger does not use any nearby mapping$`, func() error { return s.hasNot("as served by") })
	sc.Step(`^it looks "qwen3-8b" up on the model hub as in section 5$`, func() error {
		if !s.r.hub.searchedFor("qwen3-8b") {
			return fmt.Errorf("the hub was not asked")
		}
		return s.has("looking it up on the model hub", "pick 1-3")
	})
	sc.Step(`^roger says "qwen3-8b" is not served nearby and it is looking on the model hub$`, func() error {
		return s.has("qwen3-8b is not served nearby; looking it up on the model hub, because you asked to serve it")
	})
	sc.Step(`^it lists up to three matching repositories with their size, licence and download count$`, func() error {
		if err := s.matches(`1\) Qwen/Qwen3-8B-GGUF:Q4_K_M\s+4\.3 KiB\s+licence apache-2\.0\s+912\.3K downloads`); err != nil {
			return err
		}
		return s.hasNot("4) ")
	})
	sc.Step(`^each shows the quantisation the existing rule would pick for this machine and its verdict$`, func() error {
		return s.matches(`Qwen/Qwen3-8B-GGUF:Q4_K_M[^\n]*downloads\s+fits\s+at 8K`)
	})
	sc.Step(`^it states the ranking rule$`, func() error {
		return s.has("ranked by the model hub's download count, among GGUF repositories with a file that fits this machine")
	})
	sc.Step(`^it asks the owner to pick one by number or answer no$`, func() error {
		return s.has("pick 1-3 to see it before anything is fetched, or n: ")
	})
	sc.Step(`^nothing is fetched until the owner picks$`, s.nothingDownloaded)
	sc.Step(`^the name is sent to the model hub because the owner asked to serve it$`, func() error {
		if !s.r.hub.searchedFor("qwen3-8b") {
			return fmt.Errorf("the name was not looked up")
		}
		return s.has("because you asked to serve it")
	})
	sc.Step(`^opening a screen never sends a search on its own, as the approved rule requires$`, func() error {
		before := len(s.r.hub.searched())
		if _, err := modelWebHooks(s.r.cfg()).Nearby(context.Background()); err != nil {
			return err
		}
		if _, err := s.r.do(s.t, "", "nearby"); err != nil {
			return err
		}
		if n := len(s.r.hub.searched()); n != before {
			return fmt.Errorf("opening a screen sent %d searches", n-before)
		}
		return nil
	})
	sc.Step(`^roger shows that repository with its size, licence, quantisation and verdict$`, func() error {
		return s.has("1) Qwen/Qwen3-8B-GGUF:Q4_K_M  4.3 KiB  licence apache-2.0", "Qwen3-8B-Q4_K_M.gguf  4.3 KiB", "fits at 8K context")
	})
	sc.Step(`^it asks yes or no$`, func() error {
		if err := s.has("fetch it? [y/N]"); err != nil {
			return err
		}
		return s.hasNot("pick 1-")
	})
	sc.Step(`^nothing is fetched until the owner says yes$`, s.nothingDownloaded)
	sc.Step(`^the owner picked a repository and file from the hub listing$`, func() error {
		s.answers = "1\ny\n"
		return s.run("roger model serve qwen3-8b")
	})
	sc.Step(`^the sha256 the listing gave for that file is pinned$`, func() error {
		m, err := s.r.home.Find("Qwen/Qwen3-8B-GGUF:Q4_K_M")
		if err != nil {
			return err
		}
		if m.SHA256 != s.r.hub.sha("Qwen/Qwen3-8B-GGUF", qwenQ4) {
			return fmt.Errorf("pinned %s", m.SHA256)
		}
		return s.has("checked against the digest the listing gave")
	})
	sc.Step(`^the downloaded file is checked against that pinned digest before it is served$`, func() error {
		i, j := strings.Index(s.out, "fetched and verified"), strings.Index(s.out, "on the air locally")
		if i < 0 || j < i {
			return fmt.Errorf("not verified before serving:\n%s", s.out)
		}
		return nil
	})
	sc.Step(`^each is marked as its author's word(?:, not RogerAI's and not a nearby source's)?$`, func() error {
		return s.has("each is its author's word, not RogerAI's and not a nearby source's")
	})
	sc.Step(`^it is listed as unservable with the specific reason$`, func() error {
		_ = s.run("roger model serve qwen3-8b")
		return s.has("proj/Qwen3-8B-Vision-GGUF  cannot be served: mmproj-Qwen3-8B-F16.gguf is a vision projector")
	})
	sc.Step(`^it cannot be chosen$`, func() error { return s.matches(`\n\s+-\s+proj/Qwen3-8B-Vision-GGUF`) })
	sc.Step(`^roger says nothing nearby and nothing on the model hub matches$`, func() error {
		return s.has(`nothing nearby and nothing on the model hub matches "qwen3-8b"`)
	})
	sc.Step(`^it suggests: roger model search <other words>$`, func() error { return s.has("roger model search <other words>") })
	sc.Step(`^it refuses, listing the matches with their exact references$`, func() error {
		if s.err == nil {
			return fmt.Errorf("it did not refuse")
		}
		return s.has("Qwen/Qwen3-8B-GGUF:Q4_K_M", "lookalike/Qwen3-8B-GGUF:Q4_K_M", "no terminal to pick on, so nothing was fetched")
	})
	sc.Step(`^it says how to name one exactly: roger model serve <owner/repo>:<quant>$`, func() error {
		return s.has("roger model serve <owner/repo>:<quant>")
	})
	sc.Step(`^roger says the name is not served nearby and the model hub cannot be reached$`, func() error {
		return s.has("qwen3-8b is not served nearby, and the model hub cannot be reached")
	})
	sc.Step(`^it lists what is served nearby instead$`, func() error { return s.has("served nearby instead: gpt-oss-20b") })
	sc.Step(`^roger suggests "qwen3-8b" as served by atlas, with its size and verdict$`, func() error {
		return s.matches(`1\) qwen3-8b = Qwen/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas\s+4\.3 KiB\s+fits`)
	})
	sc.Step(`^it does not suggest "gpt-oss-20b", and says it would not fit here$`, func() error {
		if err := s.matches(`-\s+gpt-oss-20b = [^\n]*would not fit here`); err != nil {
			return err
		}
		return s.hasNot("2) ")
	})
	sc.Step(`^it asks the owner to pick or answer no$`, func() error {
		if strings.Contains(s.out, "pick 1 to serve it, or n: ") || strings.Contains(s.out, "pick 1-3 to see it before anything is fetched, or n: ") {
			return nil
		}
		return fmt.Errorf("no pick asked:\n%s", s.out)
	})
	sc.Step(`^it lists up to three hub models that fit this machine comfortably$`, func() error {
		if err := s.matches(`1\) Qwen/Qwen3-8B-GGUF:Q4_K_M[^\n]*fits`); err != nil {
			return err
		}
		return s.hasNot("4) ", "Huge", "Vision", "Rerank", "Tight")
	})
	sc.Step(`^it states the rule: the most-downloaded chat GGUFs on the model hub with a file that fits$`, func() error {
		return s.has("the most-downloaded chat GGUFs on the model hub with a file that fits this machine comfortably")
	})
	sc.Step(`^roger says nothing is served nearby and the model hub cannot be reached$`, func() error {
		return s.has("nothing is served nearby, and the model hub cannot be reached now")
	})
	sc.Step(`^it suggests nothing$`, func() error {
		if err := s.hasNot("1) "); err != nil {
			return err
		}
		return s.has("roger suggests nothing")
	})
	sc.Step(`^roger says it cannot judge what fits, naming the missing reading$`, func() error {
		return s.has("roger cannot judge what fits this machine: ")
	})
	sc.Step(`^it lists what is nearby with each one's needs instead of picking one$`, func() error {
		if err := s.matches(`-\s+qwen3-8b = Qwen/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas\s+4\.3 KiB\s+needs `); err != nil {
			return err
		}
		return s.hasNot("1) ", "pick 1")
	})
	sc.Step(`^it prints what it would suggest and exits without fetching$`, func() error {
		if s.err != nil {
			return s.err
		}
		if strings.Count(s.out, "no terminal, so nothing is fetched") != 2 {
			return fmt.Errorf("both runs did not say so:\n%s", s.out)
		}
		return s.nothingDownloaded()
	})
	sc.Step(`^it shows the same nearby suggestions the terminal does, from the same code$`, func() error {
		if s.webCode != http.StatusOK {
			return fmt.Errorf("%d %s", s.webCode, s.webBody)
		}
		var nv modelview.NearbyView
		if err := json.Unmarshal([]byte(s.webBody), &nv); err != nil {
			return err
		}
		if err := s.run("roger model serve"); err != nil {
			return err
		}
		if len(nv.Fits) != 1 || !strings.Contains(s.out, strings.TrimSuffix(nv.Fits[0].Ask, "; fetch 4.3 KiB?")) {
			return fmt.Errorf("the console %+v and the terminal differ:\n%s", nv, s.out)
		}
		return nil
	})
	sc.Step(`^choosing one is the same consented fetch$`, func() error {
		f := s.qwen("Q4_K_M")
		post := func(body string) int {
			req, _ := http.NewRequest(http.MethodPost, s.webURL+"/api/models/fetch-nearby", strings.NewReader(body))
			req.Header.Set("X-Roger-Token", s.webToken)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", s.webURL)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return 0
			}
			resp.Body.Close()
			return resp.StatusCode
		}
		pick := fmt.Sprintf(`"name":"qwen3-8b","repo":%q,"file":%q,"sha256":%q`, f.Repo, f.File, f.SHA256)
		if code := post("{" + pick + "}"); code != http.StatusPreconditionRequired {
			return fmt.Errorf("a fetch without the owner's yes answered %d", code)
		}
		if code := post(`{"confirm":true,` + pick + `}`); code != http.StatusOK {
			return fmt.Errorf("the confirmed fetch answered %d", code)
		}
		return nil
	})
	sc.Step(`^its licence is shown before the fetch and recorded against the file$`, func() error {
		i, j := strings.Index(s.out, "licence: apache-2.0"), strings.Index(s.out, "[y/N]")
		if i < 0 || j < i {
			return fmt.Errorf("the licence is not shown before asking:\n%s", s.out)
		}
		m, err := s.r.home.Find("Qwen/Qwen3-8B-GGUF:Q4_K_M")
		if err != nil {
			return err
		}
		if m.License != "apache-2.0" {
			return fmt.Errorf("recorded %q", m.License)
		}
		return nil
	})
	sc.Step(`^the licence never prevents fetching, serving or going on air$`, func() error { return s.has("on the air locally") })
	sc.Step(`^the runtime's answer wins and is said, as for any other model$`, func() error {
		return s.has("will not fit at 8K context by llama.cpp's own check")
	})
	sc.Step(`^roger says what it would remove first, before the fetch, as it does today$`, func() error {
		i, j := strings.Index(s.out, "removes first"), strings.Index(s.out, "[y/N]")
		if i < 0 || j < i {
			return fmt.Errorf("the removal is not said before asking:\n%s", s.out)
		}
		return s.has("Qwen/Qwen3-8B-GGUF:Q8_0")
	})
	sc.Step(`^it never removes a model being served or one the owner placed by hand$`, func() error {
		line := s.out[strings.Index(s.out, "removes first"):]
		line = line[:strings.Index(line, "\n")]
		if strings.Contains(line, "mine") {
			return fmt.Errorf("a model placed by hand is named for removal: %s", line)
		}
		return nil
	})
	sc.Step(`^the nearby bands are listed$`, func() error {
		return s.has("qwen3-8b", "smollm2-135m-instruct", "your Edge member atlas")
	})
	sc.Step(`^serving one says the file must be fetched from the model hub, which cannot be reached now$`, func() error {
		_ = s.run("roger model serve qwen3-8b")
		return s.has("the file must come from the model hub, but the model hub cannot be reached now")
	})
	sc.Step(`^the market station's claim is ignored for choosing a file$`, func() error { return s.hasNot("evil/", "as served by") })
	sc.Step(`^roger goes to the hub fallback and the owner picks from the hub listing$`, func() error {
		return s.has("looking it up on the model hub", "pick 1-3")
	})
	sc.Step(`^its mapping is not used and its band is not listed as nearby$`, func() error {
		_ = s.run("roger model nearby")
		if err := s.hasNot("qwen3-8b"); err != nil {
			return err
		}
		_ = s.run("roger model serve qwen3-8b")
		return s.hasNot("as served by")
	})
	sc.Step(`^its stations' bands are not listed as nearby and their mappings are not used$`, func() error {
		_ = s.run("roger model nearby")
		if err := s.hasNot("qwen3-8b"); err != nil {
			return err
		}
		_ = s.run("roger model serve qwen3-8b")
		return s.hasNot("as served by")
	})
	sc.Step(`^roger does not fetch the new file under the old consent$`, func() error {
		if s.r.hub.downloaded(qwenQ4) {
			return fmt.Errorf("a file was fetched")
		}
		return nil
	})
	sc.Step(`^it shows the new mapping and asks again$`, func() error {
		return s.has("the file qwen3-8b maps to changed since you said yes: now lookalike/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas; asking again",
			"qwen3-8b = lookalike/Qwen3-8B-GGUF:Q4_K_M as served by your Edge member atlas; fetch")
	})
	sc.Step(`^the file is not served$`, func() error { return s.hasNot("on the air") })
	sc.Step(`^roger names the expected and actual digest and which source reported the expected one$`, func() error {
		return s.has("expected "+s.expected[:12], "digest ", "as your Edge member atlas reported")
	})
	sc.Step(`^both are shown with their authors, download counts and licences$`, func() error {
		if err := s.matches(`Qwen/Qwen3-8B-GGUF:Q4_K_M\s+4\.3 KiB\s+licence apache-2\.0\s+912\.3K downloads`); err != nil {
			return err
		}
		return s.matches(`lookalike/Qwen3-8B-GGUF:Q4_K_M\s+4\.4 KiB\s+licence mit\s+120 downloads`)
	})
	sc.Step(`^roger never picks one for the owner$`, func() error {
		if err := s.has("pick 1-3"); err != nil {
			return err
		}
		return s.nothingDownloaded()
	})
	sc.Step(`^the ranking rule is applied as stated, not by name similarity alone$`, func() error {
		// "third/Qwen3-8B-Chat-GGUF" is a worse name match than "lookalike/Qwen3-8B-GGUF" and ranks below
		// it only by downloads; the stated rule is said.
		i, j := strings.Index(s.out, "1) Qwen/"), strings.Index(s.out, "2) lookalike/")
		if i < 0 || j < i {
			return fmt.Errorf("not ranked by downloads:\n%s", s.out)
		}
		return s.has("ranked by the model hub's download count")
	})
	sc.Step(`^it fails the pinned digest and is not served$`, func() error {
		if err := s.has("what arrived is not what was promised", "expected "+s.r.hub.repo("Qwen/Qwen3-8B-GGUF").files[qwenQ4].listSHA[:12]); err != nil {
			return err
		}
		return s.hasNot("on the air")
	})
	sc.Step(`^roger states the byte count and the free space before asking$`, func() error {
		return s.matches(`(?s)Qwen3-8B-Mini-Q4_K_M\.gguf  4\.6 KiB.*free on this disk: 2\.0 KiB.*fetch it\? \[y/N\]`)
	})
	sc.Step(`^a file that cannot fit on disk is refused before the fetch, with the numbers$`, func() error {
		if err := s.has("it needs", "is free on"); err != nil {
			return err
		}
		if s.r.hub.downloaded("Qwen3-8B-Mini-Q4_K_M.gguf") {
			return fmt.Errorf("bytes moved")
		}
		return nil
	})
	sc.Step(`^the member refuses it, because members fetch only an exact reference$`, func() error {
		return s.has("searches nowhere for a model", "owner/repo:QUANT")
	})
}

// orderCase sets up one row of "the order roger resolves a name in" and runs it.
func (s *shortNamesBDD) orderCase(name, cond string) error {
	switch cond {
	case "already on this disk":
		if _, err := s.r.do(s.t, "", "get", "Qwen/Qwen3-8B-GGUF:Q4_K_M", "--yes"); err != nil {
			return err
		}
		f := s.r.hubFileOf("lookalike/Qwen3-8B-GGUF", qwenQ4, "Q4_K_M")
		s.r.member(s.t, "atlas", name, &f, true) // a nearby one too: the disk still wins
	case "served nearby":
		s.atlasServes(name, s.qwen("Q4_K_M"))
	case "nowhere nearby":
	default:
		return fmt.Errorf("unknown condition %q", cond)
	}
	return s.run("roger model serve " + name)
}

// impliedNearbyFor gives a command that names "qwen3-8b" the nearby source its scenario relies on.
func (s *shortNamesBDD) impliedNearbyFor(line string) {
	if strings.Contains(line, "--hub") || strings.Contains(line, ":Q") {
		if !s.nearby {
			s.atlasServes("qwen3-8b", s.qwen("Q8_0"))
		}
		return
	}
	if !s.nearby && !strings.HasPrefix(line, "roger model serve qwen3-8b") {
		s.impliedNearby()
	}
	if strings.HasPrefix(line, "roger model get") || strings.Contains(line, "--add") || strings.Contains(line, "--on") {
		s.impliedNearby()
		s.answers = "y\n"
	}
}

func readAll(resp *http.Response) string {
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			return b.String()
		}
	}
}

// thisMachineServes puts a model roger fetched on the air here, as its serve record says.
func (s *shortNamesBDD) thisMachineServes(band string) error {
	if _, err := s.r.do(s.t, "", "get", "Qwen/Qwen3-8B-GGUF:Q8_0", "--yes"); err != nil {
		return err
	}
	f := s.qwen("Q8_0")
	rec, _ := json.Marshal(localmodel.Serving{Supervisor: os.Getpid(), Band: band, ID: "Qwen/Qwen3-8B-GGUF:Q8_0",
		File: f.File, SHA256: f.SHA256, Quant: "Q8_0"})
	s.nearby = true
	return os.WriteFile(filepath.Join(s.r.home.Dir, "serving.json"), rec, 0o600)
}

func TestShortNamesNearbyBDD(t *testing.T) {
	s := &shortNamesBDD{t: t, fitExe: modelFitExe}
	oldInvoke := modelEdgeInvoke
	t.Cleanup(func() { modelEdgeInvoke = oldInvoke })
	suite := godog.TestSuite{
		ScenarioInitializer: s.steps,
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Tags: "~@tui",
			Paths: []string{shortNamesFeature}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("features/models/short_names_nearby.feature failed")
	}
}
