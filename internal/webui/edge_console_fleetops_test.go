package webui

// THE BROWSER HALF of the three fleet operations the console gained: the tag editor on a node card,
// the ALARMS section that says what raises an alarm, and the service actions on a member that
// declares maintenance. Pinned on the SHIPPED assets, the way the other console-drawing tests are,
// because the rule these obey is a drawing rule: an action is offered exactly when it would work,
// and never when it would come back 501 or 400.

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// tags.feature: the console drew the owner's words and could not change them. The editor sends the
// same four shapes the CLI takes, and it goes to the tags endpoint - not to rename, not to the store.
func TestConsoleDrawsATagEditorOnAMemberCard(t *testing.T) {
	js := consoleJS(t)
	prompt := regexp.MustCompile(`function edgeTagPrompt\(card, n\) \{[\s\S]*?\n  \}`).FindString(js)
	require.NotEmpty(t, prompt, "edgeTagPrompt is the inline tag editor")
	// It is an inline edit like rename, so the poll does not wipe it and Enter/Esc release the input
	// before the re-render that follows (the 2026-09-27 audit's rule, which every form here obeys).
	require.Contains(t, prompt, "input.blur()")
	require.Contains(t, js, `apiPost("/api/edge/tags"`)
	// The editor is offered on a MEMBER the owner manages, never on a candidate that is not theirs yet.
	detail := regexp.MustCompile(`function edgeRenderDetail\(d\) \{[\s\S]*?\n  \}`).FindString(js)
	require.Contains(t, detail, "edgeTagPrompt")
	require.Contains(t, detail, "tags", "the tags row is still drawn")
}

// health.feature / manage_web.feature §3: the console could show which rules were FIRING and not say
// what fires. The ALARMS section lists them in the host's words, and offers the actions only when
// the host wired them (agents.feature: a build that cannot perform an action shows no dead button).
func TestConsoleDrawsTheAlarmRulesAndGatesTheActions(t *testing.T) {
	js := consoleJS(t)
	render := regexp.MustCompile(`function edgeRenderRules\(d\) \{[\s\S]*?\n  \}`).FindString(js)
	require.NotEmpty(t, render, "edgeRenderRules draws what raises an alarm")
	// It prints the words and the scope the SERVER worded, and infers neither in the browser.
	require.Contains(t, render, ".words")
	require.Contains(t, render, ".of")
	require.Contains(t, render, "rules_manage", "the actions are gated on the host having wired them")
	for _, path := range []string{"/api/edge/rule", "/api/edge/rule/off", "/api/edge/rule/remove"} {
		require.Contains(t, js, `apiPost("`+path+`"`)
	}
	// Removing a rule stops an alarm the owner asked for: it is confirmed, never a stray click.
	remove := regexp.MustCompile(`function edgeRemoveRule\([\s\S]*?\n  \}`).FindString(js)
	require.NotEmpty(t, remove)
	require.Contains(t, remove, "window.confirm")

	html, err := os.ReadFile("assets/console.html")
	require.NoError(t, err)
	require.Contains(t, string(html), `id="edge-rules"`, "the ALARMS section has a home in the shell")
	require.Contains(t, string(html), `id="edge-rule-form"`)

	// Every control this pass adds is styled by the console's own stylesheet - no unstyled panel,
	// and no class that exists only in a comment.
	css, err := os.ReadFile("assets/console.css")
	require.NoError(t, err)
	for _, cls := range []string{".edge-rule-form", ".edge-tag-input", ".edge-service-agent", ".edge-log-tail", ".edge-log-line"} {
		require.Contains(t, string(css), cls)
	}
}

// maintenance.feature + logs.feature: start, stop, restart an agent on a member, and read its log
// tail. Offered ONLY where they can work - the host wired the action AND that machine declares
// maintenance - because the owner's switch lives there and no console can turn it on from here.
func TestConsoleOffersServiceOnlyWhereMaintenanceIsOn(t *testing.T) {
	js := consoleJS(t)
	svc := regexp.MustCompile(`function edgeServiceControls\(card, d, n\) \{[\s\S]*?\n  \}`).FindString(js)
	require.NotEmpty(t, svc, "edgeServiceControls draws the maintenance actions")
	require.Contains(t, js, "if (d.maintain_manage && n.maintain) edgeServiceControls(card, d, n);")
	require.Contains(t, js, `apiPost("/api/edge/maintain"`)
	for _, act := range []string{"start", "stop", "restart", "logs"} {
		require.Contains(t, svc+js, act)
	}
	// Stopping and restarting signal a running process: both are confirmed.
	maint := regexp.MustCompile(`function edgeMaintain\([\s\S]*?\n  \}`).FindString(js)
	require.NotEmpty(t, maint)
	require.Contains(t, maint, "window.confirm")
	// Reboot and shutdown are refused by name server-side and are not offered here either.
	require.NotContains(t, js, `"reboot"`)
	require.NotContains(t, js, `"shutdown"`)
}

// A log line is text from ANOTHER machine. It is redacted on the serving side, and it is still put
// on the page as text and never as markup: the tail must not be a way to run script in the console.
func TestConsoleDrawsLogLinesAsTextNeverAsMarkup(t *testing.T) {
	js := consoleJS(t)
	tail := regexp.MustCompile(`function edgeShowLogTail\([\s\S]*?\n  \}`).FindString(js)
	require.NotEmpty(t, tail, "edgeShowLogTail draws the returned lines")
	require.NotContains(t, tail, "innerHTML", "a log line is text, never markup")
	require.Contains(t, tail, "el(", "lines go through the el() helper, which sets textContent")
	require.True(t, strings.Contains(tail, "held") || strings.Contains(tail, "says"),
		"the tail says what the ring holds, so a short answer is not mistaken for the whole log")
}

// A CONFIRMED BUG, found while adding the tag editor and pinned here for good.
//
// console.js declared edgeName TWICE in the one IIFE scope: edgeName(n), the display helper added by
// "ids never drawn as names", and edgeName(id, name), the rename action that was already there. A
// function declaration hoists, so the LAST one wins for the whole scope - every edgeName(n) call in
// the renderer was really calling the rename action with a node object for an id. The visible result
// was a node card titled "NODE - undefined" and a POST to /api/edge/name on every single render,
// which the server refused with 400 because it carried no name. One name, one meaning.
func TestConsoleHasOneEdgeNameAndItIsTheDisplayHelper(t *testing.T) {
	js := consoleJS(t)
	decls := regexp.MustCompile(`(?m)^  function edgeName\(`).FindAllString(js, -1)
	require.Len(t, decls, 1, "edgeName is declared once: a second declaration silently replaces the first")
	require.Contains(t, js, "function edgeName(n) {", "the surviving edgeName is the display helper")
	// The rename ACTION still exists, under a name of its own, and is what the form calls.
	require.Contains(t, js, "function edgeRename(id, name) {")
	require.Contains(t, js, `apiPost("/api/edge/name"`)
	rename := regexp.MustCompile(`function edgeRenamePrompt\(card, n\) \{[\s\S]*?\n  \}`).FindString(js)
	require.Contains(t, rename, "edgeRename(n.id, name)")
}
