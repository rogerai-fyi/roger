package client

// acctkeys.go: account API keys (`rog-key_...`) over the broker's /account/keys endpoints,
// signed with the local user key - the same endpoints and fields the web account page uses
// (features/auth/key_guardrails.feature). A minted secret is returned to the caller exactly
// once and is never logged or stored here.

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
)

// AccountKey is one key as GET /account/keys shows it (never the secret).
type AccountKey struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Hint           string   `json:"hint"`
	LimitUSD       float64  `json:"limit_usd"`
	LimitRemaining *float64 `json:"limit_remaining"`
	Reset          string   `json:"reset"`
	ExpiresAt      *string  `json:"expires_at"`
	Expired        bool     `json:"expired"`
	Disabled       bool     `json:"disabled"`
	Usage          float64  `json:"usage"`
	UsageDaily     float64  `json:"usage_daily"`
	UsageWeekly    float64  `json:"usage_weekly"`
	UsageMonthly   float64  `json:"usage_monthly"`
	AllowedModels  []string `json:"allowed_models"`
	AllowedNodes   []string `json:"allowed_nodes"`
}

// keysCall runs one signed /account/keys request and decodes a 2xx body into out (nil = none).
func keysCall(method, broker, path string, body any, out any) error {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	resp, err := signedDo(method, broker, path, raw)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBrokerUnreachable, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("log in first - run `roger login` (keys are per account)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			return fmt.Errorf("%s", e.Error.Message)
		}
		return fmt.Errorf("broker returned status %d", resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// KeysList reads the account's keys (newest first, as the broker orders them).
func KeysList(broker string) ([]AccountKey, error) {
	var out struct {
		Keys []AccountKey `json:"keys"`
	}
	err := keysCall(http.MethodGet, broker, "/account/keys", nil, &out)
	return out.Keys, err
}

// KeysMint mints a key with the given fields and returns it with its secret (shown once).
func KeysMint(broker string, fields map[string]any) (AccountKey, string, error) {
	var out struct {
		AccountKey
		Secret string `json:"secret"`
	}
	err := keysCall(http.MethodPost, broker, "/account/keys", fields, &out)
	return out.AccountKey, out.Secret, err
}

// KeysSet patches the named fields of a key and returns the updated key.
func KeysSet(broker, id string, fields map[string]any) (AccountKey, error) {
	var out AccountKey
	err := keysCall(http.MethodPatch, broker, "/account/keys/"+id, fields, &out)
	return out, err
}

// KeysRevoke deletes (revokes) a key.
func KeysRevoke(broker, id string) error {
	return keysCall(http.MethodDelete, broker, "/account/keys/"+id, nil, nil)
}

// WriteKeysTable prints keys as the mono table `roger keys list` shows: id, name, hint,
// limit, used (spend in the limit's current window), resets, expires and state.
func WriteKeysTable(w io.Writer, keys []AccountKey) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tHINT\tLIMIT\tUSED\tRESETS\tEXPIRES\tSTATE")
	for _, k := range keys {
		limit := "unlimited"
		if k.LimitUSD > 0 {
			limit = fmt.Sprintf("$%.2f", k.LimitUSD)
		}
		expires := "never"
		if k.ExpiresAt != nil && *k.ExpiresAt != "" {
			expires = *k.ExpiresAt
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t$%.2f\t%s\t%s\t%s\n", k.ID, k.Name, k.Hint, limit, k.windowUsage(), k.Reset, expires, k.State())
	}
	_ = tw.Flush()
}

// windowUsage is the spend in the window the limit counts: today, this week, this month, or
// the key's whole life for reset "none".
func (k AccountKey) windowUsage() float64 {
	switch k.Reset {
	case "daily":
		return k.UsageDaily
	case "weekly":
		return k.UsageWeekly
	case "monthly":
		return k.UsageMonthly
	}
	return k.Usage
}

// State is "disabled", "expired" or "active".
func (k AccountKey) State() string {
	switch {
	case k.Disabled:
		return "disabled"
	case k.Expired:
		return "expired"
	}
	return "active"
}

// KeyTransportOK refuses to carry an account key in the clear: the broker must be https, or
// plain http only to this machine (a local Tower or a test broker).
func KeyTransportOK(broker string) error {
	u, err := url.Parse(broker)
	if err != nil {
		return fmt.Errorf("broker %q: %v", broker, err)
	}
	if u.Scheme == "https" {
		return nil
	}
	if ip := net.ParseIP(u.Hostname()); u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("an account key is sent only over https (or to this machine), not to %s", broker)
}

// IsAccountKey reports whether s looks like an account key secret.
func IsAccountKey(s string) bool { return strings.HasPrefix(s, "rog-key_") && len(s) > len("rog-key_") }
