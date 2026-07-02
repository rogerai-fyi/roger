package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Grant keys client (GRANT-KEYS-DESIGN section 6.1). These call the broker's
// owner-auth /grants endpoints (signed + GitHub-bound, gated like priced share).
// The lean surface a typical provider sees is `grant create --name <label>`;
// everything else is defaulted or behind --advanced (see cmd/rogerai).

// GrantCreateOpts is the full create payload. The CLI fills Name + (Free or a
// price) by default and tucks the rest behind --advanced.
type GrantCreateOpts struct {
	Name       string
	Free       bool
	FreeSet    bool // whether --free was explicitly passed (vs price-derived default)
	PriceIn    float64
	PriceOut   float64
	Models     []string
	Nodes      []string
	RPM        float64
	Burst      float64
	DailyCap   int64
	MonthlyCap int64
	ExpiresAt  int64
	Self       bool
}

// grantJSON is the broker's secret-free grant view.
type grantJSON struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Nodes      []string `json:"nodes"`
	Models     []string `json:"models"`
	Free       bool     `json:"free"`
	Self       bool     `json:"self"`
	Price      string   `json:"price"`
	RPM        float64  `json:"rpm"`
	DailyCap   int64    `json:"daily_cap"`
	MonthlyCap int64    `json:"monthly_cap"`
	ExpiresAt  int64    `json:"expires_at"`
	Status     string   `json:"status"`
	Usage      struct {
		DayTokens   int64 `json:"day_tokens"`
		MonthTokens int64 `json:"month_tokens"`
	} `json:"usage"`
}

// GrantCreate mints a grant and prints the secret once + ready-to-paste env lines.
func GrantCreate(broker string, o GrantCreateOpts) error {
	free := o.Free
	if !o.FreeSet && (o.PriceIn > 0 || o.PriceOut > 0) {
		free = false
	}
	payload := map[string]any{
		"name": o.Name, "free": free,
		"price_in": o.PriceIn, "price_out": o.PriceOut,
		"models": o.Models, "nodes": o.Nodes,
		"rpm": o.RPM, "burst": o.Burst,
		"daily_cap": o.DailyCap, "monthly_cap": o.MonthlyCap,
		"expires_at": o.ExpiresAt, "self": o.Self,
	}
	resp, err := postSigned(broker+"/grants", payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		OK            bool      `json:"ok"`
		Grant         grantJSON `json:"grant"`
		Secret        string    `json:"secret"`
		OpenAIAPIBase string    `json:"openai_api_base"`
		Error         struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK || !out.OK {
		if out.Error.Message != "" {
			return fmt.Errorf("%s", out.Error.Message)
		}
		return fmt.Errorf("broker rejected the grant (status %d)", resp.StatusCode)
	}
	kind := "free"
	if out.Grant.Self {
		kind = "self ($0 on your own boxes)"
	} else if !out.Grant.Free {
		kind = "priced " + out.Grant.Price
	}
	fmt.Printf("\ncreated grant %q  (%s)\n\n", out.Grant.Name, kind)
	fmt.Printf("  %s\n", out.Secret)
	fmt.Printf("  save it now - it is shown only once.\n\n")
	fmt.Printf("  point any OpenAI app at your models with no login:\n")
	fmt.Printf("    OPENAI_API_BASE=%s\n", out.OpenAIAPIBase)
	fmt.Printf("    OPENAI_API_KEY=%s\n", out.Secret)
	if out.Grant.DailyCap > 0 {
		fmt.Printf("\n  daily cap: %d tokens/day  (roger grant show %s)\n", out.Grant.DailyCap, out.Grant.Name)
	}
	return nil
}

// GrantInfo is a compact grant summary for programmatic callers (the in-TUI list).
type GrantInfo struct {
	Name, Price, Status string
}

// GrantCreateSecret mints a grant and returns ONLY the secret (the data form of
// GrantCreate, for the in-TUI /grant create flow). free=true makes it a free key.
func GrantCreateSecret(broker, name string, free bool) (string, error) {
	resp, err := postSigned(broker+"/grants", map[string]any{"name": name, "free": free})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool   `json:"ok"`
		Secret string `json:"secret"`
		Error  struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK || out.Secret == "" {
		if out.Error.Message != "" {
			return "", fmt.Errorf("%s", out.Error.Message)
		}
		return "", fmt.Errorf("grant create failed (status %d)", resp.StatusCode)
	}
	return out.Secret, nil
}

// GrantListRows returns the owner's grants as compact rows (the in-TUI /grant list).
func GrantListRows(broker string) ([]GrantInfo, error) {
	gs, err := fetchGrants(broker)
	if err != nil {
		return nil, err
	}
	rows := make([]GrantInfo, 0, len(gs))
	for _, g := range gs {
		rows = append(rows, GrantInfo{Name: g.Name, Price: priceLabel(g), Status: g.Status})
	}
	return rows, nil
}

// GrantList prints the caller-owner's grants as a table.
func GrantList(broker string) error {
	gs, err := fetchGrants(broker)
	if err != nil {
		return err
	}
	if len(gs) == 0 {
		fmt.Println("no grants yet - `roger grant create --name my-bots` mints a free key for your bots/family.")
		return nil
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].Name < gs[j].Name })
	fmt.Printf("%-16s %-8s %-14s %-18s %-10s %s\n", "NAME", "PRICE", "CAPS(rpm/day)", "USED(day/month)", "EXPIRES", "STATUS")
	for _, g := range gs {
		caps := fmt.Sprintf("%s/%s", numOrDash(g.RPM), capOrDash(g.DailyCap))
		used := fmt.Sprintf("%d/%d", g.Usage.DayTokens, g.Usage.MonthTokens)
		fmt.Printf("%-16s %-8s %-14s %-18s %-10s %s\n",
			trunc(g.Name, 16), priceLabel(g), caps, used, expiresLabel(g.ExpiresAt), g.Status)
	}
	return nil
}

// GrantShow prints one grant's full scope + caps + usage (never the secret).
func GrantShow(broker, name string) error {
	g, ok, err := findGrant(broker, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no grant named %q", name)
	}
	fmt.Printf("grant %q  [%s]\n", g.Name, g.Status)
	fmt.Printf("  id        %s\n", g.ID)
	fmt.Printf("  price     %s\n", priceLabel(g))
	fmt.Printf("  nodes     %s\n", scopeLabel(g.Nodes))
	fmt.Printf("  models    %s\n", scopeLabel(g.Models))
	fmt.Printf("  rpm       %s\n", numOrDash(g.RPM))
	fmt.Printf("  daily     %s tokens/day\n", capOrDash(g.DailyCap))
	fmt.Printf("  monthly   %s tokens/month\n", capOrDash(g.MonthlyCap))
	fmt.Printf("  expires   %s\n", expiresLabel(g.ExpiresAt))
	fmt.Printf("  used      %d tokens today, %d this month\n", g.Usage.DayTokens, g.Usage.MonthTokens)
	return nil
}

// GrantRevoke revokes a grant by name (DELETE /grants/{id}).
func GrantRevoke(broker, name string) error {
	g, ok, err := findGrant(broker, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no grant named %q", name)
	}
	req, _ := http.NewRequest(http.MethodDelete, broker+"/grants/"+g.ID, nil)
	signRequest(req, nil)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("revoke failed (status %d)", resp.StatusCode)
	}
	fmt.Printf("revoked %q - the next request with its key is rejected.\n", name)
	return nil
}

// fetchGrants lists the owner's grants (signed GET /grants).
func fetchGrants(broker string) ([]grantJSON, error) {
	req, _ := http.NewRequest(http.MethodGet, broker+"/grants", nil)
	signRequest(req, nil)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		// NEVER suggest "link GitHub" here: an Apple-funded wallet would be stranded by the
		// GitHub-wins precedence. Grants need a linked account of EITHER kind.
		return nil, fmt.Errorf("grants require a linked operator account - sign in (GitHub or Apple) first")
	}
	var out struct {
		Grants []grantJSON `json:"grants"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Grants, nil
}

// findGrant resolves a grant by its human name (label).
func findGrant(broker, name string) (grantJSON, bool, error) {
	gs, err := fetchGrants(broker)
	if err != nil {
		return grantJSON{}, false, err
	}
	for _, g := range gs {
		if g.Name == name {
			return g, true, nil
		}
	}
	return grantJSON{}, false, nil
}

func priceLabel(g grantJSON) string {
	if g.Self {
		return "self"
	}
	if g.Free {
		return "free"
	}
	return g.Price
}

func scopeLabel(s []string) string {
	if len(s) == 0 {
		return "any"
	}
	return strings.Join(s, ",")
}

func numOrDash(v float64) string {
	if v <= 0 {
		return "-"
	}
	return fmt.Sprintf("%g", v)
}

func capOrDash(v int64) string {
	if v <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", v)
}

func expiresLabel(unix int64) string {
	if unix == 0 {
		return "never"
	}
	return time.Unix(unix, 0).Format("2006-01-02")
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}
