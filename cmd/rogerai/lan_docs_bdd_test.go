package main

// Executable spec: features/lan/trust_and_honesty.feature (@docs). The public manual's LAN mode
// section, read from the page source the site is built from.

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"rogerai.fm/roger/v6/internal/bddrun"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

// manualSection is the plain text of the manual section with the given id.
func manualSection(id string) (string, error) {
	b, err := os.ReadFile("../../web/src/manual.html")
	if err != nil {
		return "", err
	}
	page := string(b)
	start := strings.Index(page, `<section class="man-sec" id="`+id+`">`)
	if start < 0 {
		return "", fmt.Errorf("the manual has no section %q", id)
	}
	end := strings.Index(page[start:], "</section>")
	txt := regexp.MustCompile(`<[^>]+>`).ReplaceAllString(page[start:start+end], " ")
	txt = strings.NewReplacer("&rsquo;", "'", "&amp;", "&", "&nbsp;", " ", "&mdash;", "-").Replace(txt)
	return strings.Join(strings.Fields(txt), " "), nil
}

func TestLANDocsBDD(t *testing.T) {
	var text string
	has := func(words ...string) error {
		for _, w := range words {
			if !strings.Contains(strings.ToLower(text), strings.ToLower(w)) {
				return fmt.Errorf("the manual's LAN section does not say %q:\n%s", w, text)
			}
		}
		return nil
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { text = ""; return ctx, nil })
			sc.Step(`^LAN mode is on$`, func() error { return nil })
			sc.Step(`^the manual's LAN mode section is read$`, func() (err error) { text, err = manualSection("sLAN"); return err })
			sc.Step(`^it says LAN mode filters bands to local sources and is not an offline mode$`, func() error {
				return has("only bands served on your local network", "not an offline mode")
			})
			sc.Step(`^it says Towers are trusted because they are on the LAN, with the plain-http exposure$`, func() error {
				return has("trusted because", "plain http", "any device on the same network", "read what you send")
			})
			sc.Step(`^it says Edge members are verified by certificate$`, func() error { return has("verified by certificate") })
			sc.Step(`^it says how to keep sensitive work on Edge members only$`, func() error {
				return has("sensitive", "roger --edge-only", "lan-edge-only", "roger lan rm")
			})
		},
		Options: &godog.Options{Format: "pretty", Strict: true, TestingT: t,
			Paths: []string{"../../features/lan/trust_and_honesty.feature:57"}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("LAN docs scenario failed")
	}
}
