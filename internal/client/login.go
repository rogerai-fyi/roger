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
	resp, err := postSigned(broker+"/auth/github", map[string]string{"access_token": token})
	if err != nil {
		return err
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
			return fmt.Errorf("broker rejected the login: %s", out.Error.Message)
		}
		return fmt.Errorf("broker rejected the login (status %d)", resp.StatusCode)
	}
	_ = saveAuth(authState{GitHubLogin: out.GitHubLogin, GitHubID: out.GitHubID, BoundAt: time.Now().Unix()})
	fmt.Printf("\nlogged in as GitHub @%s - this machine can now earn as a provider.\n", out.GitHubLogin)
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
	fmt.Println("logged out (local GitHub link forgotten; your signing key is kept).")
	return nil
}

// Whoami prints the signed identity (always present) and the linked GitHub owner
// (if logged in).
func Whoami() error {
	fmt.Printf("signed identity: %s\n", SignedUserID())
	fmt.Printf("  pubkey:        %s\n", UserPubHex())
	if a, ok := loadAuth(); ok {
		fmt.Printf("github:          @%s (id %d)\n", a.GitHubLogin, a.GitHubID)
	} else {
		fmt.Printf("github:          not linked (run `rogerai login` to monetize)\n")
	}
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
