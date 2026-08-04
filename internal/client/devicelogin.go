package client

// Broker-mediated device login: the CLI talks only to RogerAI, and the human chooses
// their provider on our page.
//
// Contract: features/auth/broker_mediated_login.feature.
//
// This replaces calling a provider's device endpoint directly. Three things change for
// the better: any provider we support works with no CLI change; the CLI's only outbound
// host is the broker; and adding or rotating a provider stops needing a new binary.
//
// The requests are SIGNED with this machine's key, and that key is what the broker binds
// at issue - so approval decides which ACCOUNT signs in, never which device.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DeviceLogin is what the CLI shows a person: open this, type that.
type DeviceLogin struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

type devicePoll struct {
	Status   string `json:"status"`
	Account  string `json:"account"`
	Interval int    `json:"interval"`
}

// ErrLoginDenied is returned when the human refused the request. It is a normal outcome,
// not a failure to retry.
var ErrLoginDenied = errors.New("the sign-in was denied")

// ErrLoginExpired is returned when nobody approved in time.
var ErrLoginExpired = errors.New("the sign-in expired before it was approved")

// DeviceLoginBegin asks the broker to start a login.
func DeviceLoginBegin(broker string) (DeviceLogin, error) {
	var out DeviceLogin
	if err := signedJSON(broker+"/auth/device/start", map[string]any{}, &out); err != nil {
		return DeviceLogin{}, err
	}
	if out.DeviceCode == "" || out.UserCode == "" {
		return DeviceLogin{}, errors.New("the broker did not start a login")
	}
	return out, nil
}

// DeviceLoginPoll waits for a human to approve, and returns the account signed in.
//
// It honours the broker's interval, including a raised one: polling faster than asked is
// what makes a flow look like an attack, and the broker slows a caller down rather than
// failing them.
func DeviceLoginPoll(broker string, d DeviceLogin) (string, error) {
	interval := time.Duration(max(d.Interval, 1)) * time.Second
	deadline := time.Now().Add(time.Duration(max(d.ExpiresIn, 60)) * time.Second)

	for time.Now().Before(deadline) {
		time.Sleep(interval)
		var out devicePoll
		if err := signedJSON(broker+"/auth/device/token", map[string]any{"device_code": d.DeviceCode}, &out); err != nil {
			if worthRetrying(err) {
				// The broker could not answer; that is not a verdict on this code. Keep
				// polling to the deadline rather than ending a login that is still valid -
				// a person is typically away finding their mail while this runs, and a
				// single blip should not cost them the whole flow.
				continue
			}
			return "", err
		}
		switch out.Status {
		case "approved":
			return out.Account, nil
		case "denied":
			return "", ErrLoginDenied
		case "expired":
			return "", ErrLoginExpired
		case "slow_down":
			if out.Interval > 0 {
				interval = time.Duration(out.Interval) * time.Second
			} else {
				interval += time.Second
			}
		}
	}
	return "", ErrLoginExpired
}

// DeviceLoginRun is the whole flow, printing what a person needs and waiting.
func DeviceLoginRun(broker string) (string, error) {
	d, err := DeviceLoginBegin(broker)
	if err != nil {
		return "", err
	}
	fmt.Printf("\nTo sign in, open:  %s\n", d.VerificationURI)
	fmt.Printf("And enter code:    %s\n\n", d.UserCode)
	fmt.Println("You can sign in with any method your account supports.")
	fmt.Println("waiting for approval...")

	return DeviceLoginComplete(broker, d)
}

// DeviceLoginComplete waits for approval and persists the resulting account. Split from
// DeviceLoginRun so the wait-and-store half can be driven by a test without the printing.
func DeviceLoginComplete(broker string, d DeviceLogin) (string, error) {
	login, err := DeviceLoginPoll(broker, d)
	if err != nil {
		return "", err
	}
	if err := saveAuth(authState{GitHubLogin: login}); err != nil {
		return "", err
	}
	return login, nil
}

// signedJSON posts a signed JSON request and decodes the reply.
func signedJSON(url string, in any, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	signRequest(req, body)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = fmt.Sprintf("the broker replied %d", resp.StatusCode)
		}
		return &httpError{status: resp.StatusCode, msg: e.Error}
	}
	return json.Unmarshal(raw, out)
}

// httpError carries the STATUS alongside the message, so a caller can tell "your request
// was wrong" from "we are briefly unable to answer". Without the status the two are one
// opaque string, and the poll loop below has to treat them alike.
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

// worthRetrying reports whether an error is the broker being momentarily unable to answer
// rather than a verdict on this login. It defers to the failover policy already used for
// relay outcomes (failover.go): a 5xx or a transport failure is worth waiting out, a 4xx is
// the caller's fault and retrying it only spins until the code expires.
func worthRetrying(err error) bool {
	var he *httpError
	if errors.As(err, &he) {
		return retryable(he.status, nil)
	}
	// A transport failure never reached the broker at all, so it says nothing about the
	// login. A person mid-approval should not lose it to one dropped connection.
	return retryable(0, err)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
