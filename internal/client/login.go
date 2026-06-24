package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GitHub OAuth Device Authorization Grant endpoints. The CLI uses ONLY the public
// Client ID - no client secret ever lives in the CLI (the secret is the broker's,
// for the web flow). The device flow degrades to "type a code on your phone", so
// it works over SSH / on headless GPU boxes where providers run.
const (
	ghDeviceCodeURL  = "https://github.com/login/device/code"
	ghAccessTokenURL = "https://github.com/login/oauth/access_token"
	ghDeviceGrant    = "urn:ietf:params:oauth:grant-type:device_code"
)

// authState is the persisted login: the GitHub login the signing key is bound to.
// We do NOT persist the GitHub access token (it was only needed once, to prove the
// identity to the broker); the durable credential is the local Ed25519 user key.
type authState struct {
	GitHubLogin string `json:"github_login"`
	GitHubID    int64  `json:"github_id"`
	BoundAt     int64  `json:"bound_at"`
}

func authPath() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "rogerai", "auth.json")
}

func loadAuth() (authState, bool) {
	b, err := os.ReadFile(authPath())
	if err != nil {
		return authState{}, false
	}
	var a authState
	if json.Unmarshal(b, &a) != nil || a.GitHubLogin == "" {
		return authState{}, false
	}
	return a, true
}

func saveAuth(a authState) error {
	_ = os.MkdirAll(filepath.Dir(authPath()), 0700)
	b, _ := json.MarshalIndent(a, "", "  ")
	return os.WriteFile(authPath(), b, 0600)
}

// Login runs the GitHub device flow with the public client id, then binds the
// resulting identity to the local signing pubkey via the broker's POST /auth/github.
// Owners log in to monetize; consumers never need this.
func Login(broker, clientID string) error {
	if clientID == "" {
		return fmt.Errorf("no GitHub client id configured (set GITHUB_OAUTH_CLIENT_ID or build with the default)")
	}
	dev, err := startDeviceFlow(clientID)
	if err != nil {
		return err
	}
	fmt.Printf("\nTo log in, open:  %s\n", dev.VerificationURI)
	fmt.Printf("And enter code:   %s\n\n", dev.UserCode)
	if dev.VerificationURIComplete != "" {
		fmt.Printf("(or open this pre-filled link: %s)\n\n", dev.VerificationURIComplete)
	}
	fmt.Println("waiting for authorization...")

	token, err := pollDeviceToken(clientID, dev)
	if err != nil {
		return err
	}
	// Hand the GitHub token to the broker, which verifies it server-side and binds
	// github_id<->login<->our signing pubkey. The CLI signs this request so the
	// broker knows which pubkey to bind.
	login, err := bindToken(broker, token)
	if err != nil {
		return err
	}
	// Binding collapses the CLI keypair onto the account wallet: this keypair now
	// spends/tops-up/reads the SAME wallet as the web session (one wallet per account),
	// and earning as a provider is unlocked.
	fmt.Printf("\nlogged in as @%s, wallet ready - this keypair now shares one wallet with your account (and can earn as a provider).\n", login)
	// First login lands the $1 starter credit on the account wallet (the broker seeds
	// once per account). Surface it so a new user knows they can try a paid model right
	// away; a re-login is a no-op so this line is harmless if the seed was already given.
	fmt.Println("  + $1 starter credit on your wallet - enough to try a paid model. `rogerai topup` adds more.")
	return nil
}

// LinkedLogin returns the locally-linked GitHub login, or "" if not logged in.
func LinkedLogin() string {
	if a, ok := loadAuth(); ok {
		return a.GitHubLogin
	}
	return ""
}

// LoginReturn runs Login and returns the resulting GitHub login (the data form for
// the in-TUI /login flow).
func LoginReturn(broker, clientID string) (string, error) {
	if err := Login(broker, clientID); err != nil {
		return "", err
	}
	return LinkedLogin(), nil
}

// Device is the public, display-ready view of a started device flow: the
// verification URL the user opens and the short code they type. The TUI renders
// these in its own panel (and auto-opens the URL) instead of relying on the CLI's
// stdout, which is hidden behind the full-screen TUI. Handle is the opaque
// continuation passed back to LoginPoll.
type Device struct {
	VerificationURI string // the URL to open (github.com/login/device)
	UserCode        string // the short code to type (e.g. FD9D-8F33)
	Handle          any    // opaque; pass back to LoginPoll
}

// LoginBegin starts the GitHub device flow and returns the URL + code to show,
// WITHOUT polling. The TUI calls this, renders the panel, auto-opens the URL,
// then calls LoginPoll to wait for the user to authorize. Splitting begin/poll
// lets the in-TUI login render its own clean panel rather than printing to the
// terminal hidden behind it.
func LoginBegin(broker, clientID string) (Device, error) {
	if clientID == "" {
		return Device{}, fmt.Errorf("no GitHub client id configured (set GITHUB_OAUTH_CLIENT_ID or build with the default)")
	}
	dev, err := startDeviceFlow(clientID)
	if err != nil {
		return Device{}, err
	}
	return Device{VerificationURI: dev.VerificationURI, UserCode: dev.UserCode, Handle: dev}, nil
}

// LoginPoll blocks until the user authorizes the device started by LoginBegin (or
// it times out / is denied), then binds the GitHub identity to the local signing
// key via the broker and persists it. It returns the linked GitHub login. d.Handle
// must be the value returned by LoginBegin.
func LoginPoll(broker, clientID string, d Device) (string, error) {
	dev, ok := d.Handle.(deviceFlow)
	if !ok {
		return "", fmt.Errorf("invalid login handle")
	}
	token, err := pollDeviceToken(clientID, dev)
	if err != nil {
		return "", err
	}
	login, err := bindToken(broker, token)
	if err != nil {
		return "", err
	}
	return login, nil
}

// bindToken hands the GitHub token to the broker, which verifies it server-side
// and binds github_id<->login<->our signing pubkey, then persists the local auth
// record. Returns the bound GitHub login. Shared by Login and LoginPoll.
func bindToken(broker, token string) (string, error) {
	resp, err := postSigned(broker+"/auth/github", map[string]string{"access_token": token})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		OK          bool   `json:"ok"`
		GitHubLogin string `json:"github_login"`
		GitHubID    int64  `json:"github_id"`
		Error       struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK || !out.OK {
		if out.Error.Message != "" {
			return "", fmt.Errorf("broker rejected the login: %s", out.Error.Message)
		}
		return "", fmt.Errorf("broker rejected the login (status %d)", resp.StatusCode)
	}
	_ = saveAuth(authState{GitHubLogin: out.GitHubLogin, GitHubID: out.GitHubID, BoundAt: time.Now().Unix()})
	return out.GitHubLogin, nil
}

// LogoutReturn forgets the local GitHub binding (the in-TUI logout). It mirrors
// Logout but stays silent (no stdout) so the TUI owns the on-screen feedback.
func LogoutReturn() error {
	if _, ok := loadAuth(); !ok {
		return nil
	}
	if err := os.Remove(authPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Logout forgets the local GitHub binding record (the broker binding persists
// server-side and is re-established on the next login; the signing key is kept).
func Logout() error {
	if _, ok := loadAuth(); !ok {
		fmt.Println("not logged in.")
		return nil
	}
	if err := os.Remove(authPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Println("logged out - GitHub link forgotten locally; your signing keypair is kept (now anonymous). Run `rogerai login` to use your wallet again.")
	return nil
}

// Whoami states plainly whether you are LOGGED IN (GitHub-linked, one account
// wallet) or ANONYMOUS (a bare signing keypair, free models + grant keys only), then
// shows the signing pubkey. The wallet/balance line is shown only when logged in.
func Whoami() error {
	if a, ok := loadAuth(); ok {
		fmt.Printf("logged in as @%s (github id %d)\n", a.GitHubLogin, a.GitHubID)
		fmt.Printf("  wallet:  your account wallet (one wallet: CLI + web)\n")
		fmt.Printf("  pubkey:  %s\n", UserPubHex())
		return nil
	}
	fmt.Println("anonymous - not logged in")
	fmt.Println("  free models and grant keys work; run `rogerai login` to use your wallet + earn")
	fmt.Printf("  pubkey:  %s\n", UserPubHex())
	return nil
}

type deviceFlow struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// startDeviceFlow requests a device + user code from GitHub (scope read:user).
func startDeviceFlow(clientID string) (deviceFlow, error) {
	form := url.Values{"client_id": {clientID}, "scope": {"read:user"}}
	req, _ := http.NewRequest(http.MethodPost, ghDeviceCodeURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return deviceFlow{}, err
	}
	defer resp.Body.Close()
	var d deviceFlow
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil || d.DeviceCode == "" {
		return deviceFlow{}, fmt.Errorf("github device-code request failed (status %d)", resp.StatusCode)
	}
	if d.Interval <= 0 {
		d.Interval = 5
	}
	return d, nil
}

// pollDeviceToken polls GitHub until the user approves (or it expires), honoring
// authorization_pending and slow_down per RFC 8628.
func pollDeviceToken(clientID string, dev deviceFlow) (string, error) {
	interval := time.Duration(dev.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(maxInt(dev.ExpiresIn, 300)) * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(interval)
		form := url.Values{
			"client_id":   {clientID},
			"device_code": {dev.DeviceCode},
			"grant_type":  {ghDeviceGrant},
		}
		req, _ := http.NewRequest(http.MethodPost, ghAccessTokenURL, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}
		var r struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&r)
		resp.Body.Close()
		switch {
		case r.AccessToken != "":
			return r.AccessToken, nil
		case r.Error == "authorization_pending":
			// keep polling
		case r.Error == "slow_down":
			interval += 5 * time.Second
		case r.Error == "expired_token":
			return "", fmt.Errorf("the login code expired - run `rogerai login` again")
		case r.Error == "access_denied":
			return "", fmt.Errorf("login denied")
		case r.Error != "":
			return "", fmt.Errorf("github: %s", r.Error)
		}
	}
	return "", fmt.Errorf("login timed out - run `rogerai login` again")
}

// postSigned posts a JSON body to url with the user-key request signature.
func postSigned(url string, payload any) (*http.Response, error) {
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	signRequest(req, body)
	return (&http.Client{Timeout: 15 * time.Second}).Do(req)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
