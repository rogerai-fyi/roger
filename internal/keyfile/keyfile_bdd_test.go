package keyfile_test

// Executable spec: features/packaging/one_binary.feature section 7.5, one key loader. Both
// identity keys load through keyfile.LoadOrCreate; a key written before the upgrade loads
// unchanged; a damaged one is refused with one message naming its path and is never
// overwritten, and roger refuses at startup before anything signs.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/keyfile"
)

func repoRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", err
	}
	return filepath.Dir(strings.TrimSpace(string(out))), nil
}

type keyBDD struct {
	t       *testing.T
	root    string
	path    string
	before  []byte
	refusal string
}

func (s *keyBDD) bothCallLoadOrCreate() error {
	for file, fn := range map[string]string{
		"internal/agent/agent.go":     "loadOrCreateKey",
		"internal/client/identity.go": "LoadOrCreateUserKey",
	} {
		src, err := os.ReadFile(filepath.Join(s.root, file))
		if err != nil {
			return err
		}
		body := regexp.MustCompile(`(?s)func ` + fn + `\(\).*?\n}\n`).FindString(string(src))
		if !strings.Contains(body, "keyfile.") {
			return fmt.Errorf("%s in %s does not load through keyfile", fn, file)
		}
		if strings.Contains(body, "GenerateKey(") || strings.Contains(body, "os.WriteFile(") {
			return fmt.Errorf("%s in %s still generates or writes the key itself", fn, file)
		}
	}
	return nil
}

func (s *keyBDD) legacyLoadsUnchanged() error {
	dir := s.t.TempDir()
	p := filepath.Join(dir, "user.key")
	_, priv, _ := ed25519.GenerateKey(nil)
	// The previous release wrote the key as hex text, mode 0600.
	if err := os.WriteFile(p, []byte(hex.EncodeToString(priv)), 0o600); err != nil {
		return err
	}
	got, err := keyfile.LoadOrCreate(p)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, priv) {
		return fmt.Errorf("a key written before the upgrade loaded as a different key")
	}
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	if fi.Mode().Perm() != 0o600 {
		return fmt.Errorf("the key's permissions changed to %v", fi.Mode().Perm())
	}
	return nil
}

func (s *keyBDD) truncated() error {
	s.path = filepath.Join(s.t.TempDir(), "node.key")
	_, priv, _ := ed25519.GenerateKey(nil)
	s.before = []byte(hex.EncodeToString(priv)[:40]) // cut short
	return os.WriteFile(s.path, s.before, 0o600)
}

func (s *keyBDD) eachLoads() error {
	_, err := keyfile.LoadOrCreate(s.path)
	if err == nil {
		return fmt.Errorf("a truncated key loaded")
	}
	if err2 := keyfile.Check(s.path); err2 == nil || err2.Error() != err.Error() {
		return fmt.Errorf("the startup check and the loader refuse differently: %v / %v", err2, err)
	}
	s.refusal = err.Error()
	// roger (and so roger share and roger tower, its subcommands) checks both identity keys at
	// startup, before anything signs.
	src, rerr := os.ReadFile(filepath.Join(s.root, "cmd/rogerai/main.go"))
	if rerr != nil {
		return rerr
	}
	body := regexp.MustCompile(`(?s)func run\(.*?\n}\n`).FindString(string(src))
	if !strings.Contains(body, "checkIdentityKeys(") {
		return fmt.Errorf("roger does not check its identity keys at startup")
	}
	return nil
}

func (s *keyBDD) refusesNamingPath() error {
	if !strings.Contains(s.refusal, s.path) {
		return fmt.Errorf("the refusal does not name the path: %q", s.refusal)
	}
	after, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	if !bytes.Equal(after, s.before) {
		return fmt.Errorf("the damaged key was overwritten")
	}
	return nil
}

func TestOneKeyLoaderBDD(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	s := &keyBDD{t: t, root: root}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.path, s.before, s.refusal = "", nil, ""
				return ctx, nil
			})
			sc.Step(`^agent\.loadOrCreateKey and client\.LoadOrCreateUserKey$`, func() error { return nil })
			sc.Step(`^both call keyfile\.LoadOrCreate\(path\)$`, s.bothCallLoadOrCreate)
			sc.Step(`^files written before the upgrade load unchanged, with the same permissions \(0600\)$`, s.legacyLoadsUnchanged)
			sc.Step(`^a truncated key file$`, s.truncated)
			sc.Step(`^roger, roger share or roger tower loads it$`, s.eachLoads)
			sc.Step(`^each refuses with the same message naming the path, and never overwrites it$`, s.refusesNamingPath)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{
				"../../features/packaging/one_binary.feature:518",
				"../../features/packaging/one_binary.feature:523",
			},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("one_binary.feature section 7.5 failed")
	}
}
