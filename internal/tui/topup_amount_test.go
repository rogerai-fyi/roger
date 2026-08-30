package tui

import (
	"strings"
	"testing"

	"rogerai.fm/roger/v6/internal/client"
)

// The TUI's /topup carried a third private copy of the amount reader, with the original
// bug: ParseFloat on the raw argument, no "$" strip, silent fallback to $10. `/topup $25`
// opened checkout for $10 and said nothing. It reads through client.ParseTopupAmount now,
// like the two CLI spellings.

func TestTUITopupReadsTheAmountTheSameWayTheCLIDoes(t *testing.T) {
	for _, c := range []struct {
		args []string
		want float64
	}{
		{nil, client.DefaultTopupUSD},
		{[]string{"25"}, 25},
		{[]string{"$25"}, 25},
		{[]string{"12.50"}, 12.5},
	} {
		var got float64
		m := model{broker: "http://broker.invalid", user: "u"}
		m.hooks.TopupURL = func(_, _ string, usd float64) (string, error) {
			got = usd
			return "https://checkout.invalid/session", nil
		}
		_, cmd := m.doTopup(c.args)
		if cmd == nil {
			t.Fatalf("/topup %q produced no command", c.args)
		}
		cmd() // the hook runs inside the returned tea.Cmd
		if got != c.want {
			t.Errorf("/topup %q charged $%v, want $%v", c.args, got, c.want)
		}
	}
}

func TestTUITopupRefusesAnUnreadableAmountInsteadOfCharging(t *testing.T) {
	for _, args := range [][]string{
		{"bogus"},
		{"0"},
		{"-5"},
		{"NaN"},
	} {
		called := false
		m := model{broker: "http://broker.invalid", user: "u"}
		m.hooks.TopupURL = func(_, _ string, usd float64) (string, error) {
			called = true
			return "", nil
		}
		next, cmd := m.doTopup(args)
		if cmd != nil {
			cmd()
		}
		if called {
			t.Errorf("/topup %q opened checkout instead of refusing", args)
		}
		nm, ok := next.(model)
		if !ok {
			t.Fatalf("/topup %q did not return a model", args)
		}
		if strings.TrimSpace(nm.status) == "" {
			t.Errorf("/topup %q refused silently - the operator is told nothing", args)
			continue
		}
		// It must READ as a refusal. stDim is the hint style ("opening checkout…"); a
		// money-path refusal wearing it is indistinguishable from progress.
		if !strings.Contains(nm.status, "!") {
			t.Errorf("/topup %q refused in the hint style, not the error style: %q", args, nm.status)
		}
	}
}
