package main

// THE EMPTY EDGE'S j PROMPT, host side (features/edge/empty_edge_simple.feature §4): one line
// that is either an invite or an authority's address, with the rules of `roger join` (ruled
// 2026-10-11: a roger:// string goes to the invite parser, anything else is an address). An
// invite enrolls at once, through the same root-proof check `roger join <invite>` runs. An
// address asks, shows the matching phrase, and waits for the owner there; approval enrolls
// with the root the phrase covered. Every failure is one line from internal/look, so the TUI
// and the console say the same thing; the raw error is never shown and the text is never logged.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/edgeauth"
	"rogerai.fm/roger/v7/internal/joininvite"
	"rogerai.fm/roger/v7/internal/lanmode"
	"rogerai.fm/roger/v7/internal/look"
	"rogerai.fm/roger/v7/internal/protocol"
)

// edgeJoinPromptHooks is the prompt's backend for the TUI and the console.
func edgeJoinPromptHooks(cfg config) *edge.JoinPrompt {
	return &edge.JoinPrompt{Start: func(text string) (*edge.JoinAsk, error) { return edgeJoinPromptStart(cfg, text) }}
}

func edgeJoinPromptStart(cfg config, text string) (*edge.JoinAsk, error) {
	text = strings.TrimSpace(text)
	if strings.Contains(text, "://") {
		return nil, edgeJoinByInvite(cfg, text)
	}
	return edgeJoinByAddress(cfg, text)
}

// edgeJoinByInvite is `roger join <invite>` for an Edge invite: local addresses only, then the
// enrollment with the invite's pin and code vouching for the root.
func edgeJoinByInvite(cfg config, text string) error {
	in, err := joininvite.Parse(text)
	if err != nil {
		return errors.New(look.JoinSaysNotInvite)
	}
	if in.Kind != joininvite.Edge {
		return errors.New(look.JoinSaysTower)
	}
	local := joinLocalAddrs(in)
	if len(local) == 0 {
		return errors.New(look.JoinSaysNotLocal)
	}
	_, err = edgeEnrollJoin(cfg, "", "http://"+local[0], client.LinkedLogin(), edgeJoinTrust{Pin: in.FP, Code: in.Code})
	return edgeJoinPromptSays(err)
}

// joinLocalAddrs is the invite's addresses that resolve to this machine's own network, each
// resolved ONCE (features/join/adversarial.feature); shared with cmdJoin.
func joinLocalAddrs(in joininvite.Invite) []string {
	var local []string
	for _, a := range in.Addrs {
		addr, perr := lanmode.ParseAddr(a)
		if perr != nil {
			continue
		}
		if dial, v := lanmode.Resolve(context.Background(), addr, joinResolve); v.Local {
			if addr.Literal() {
				dial = a
			}
			local = append(local, dial)
		}
	}
	return local
}

// edgeJoinByAddress asks the Edge authority at text to let this machine in: the pairing's
// phrase is shown, the owner approves there, and the enrollment pins the root the phrase covered.
func edgeJoinByAddress(cfg config, text string) (*edge.JoinAsk, error) {
	addr, err := lanmode.ParseAddr(text)
	if err != nil {
		return nil, errors.New(look.JoinSaysNotAddress)
	}
	if v := lanmode.Classify(context.Background(), addr, nil); !v.Local {
		return nil, errors.New(look.JoinSaysNotLocal)
	}
	base := "http://" + text
	root := edgeRootAt(base)
	if root == nil {
		return nil, errors.New(look.JoinSaysNoAnswer)
	}
	// ONE EDGE, ONE AUTHORITY: a root this machine already holds that is not this one is
	// another Edge, and the prompt is not the place to move (D11: that is --replace-root).
	if mine := edgeHeldRoot(edgeIdentityStore()); mine != nil && !mine.Equal(root) {
		return nil, errors.New(look.JoinSaysOtherEdge)
	}
	rootFP := edgeauth.RootFingerprint(root)
	key := edgeUserKey()
	pubHex := hex.EncodeToString(key.Public().(ed25519.PublicKey))
	peer := pairPeer{called: "the Edge at " + text, key: rootFP,
		post: func(path string, body []byte, timeout time.Duration) (int, []byte, error) {
			return signedPost(&http.Client{Timeout: timeout}, base, key, pubHex, path, body, protocol.NewNonce())
		}}
	name, _ := os.Hostname()
	p, err := pairStart(text, peer, key, "client", name, nil)
	if err != nil {
		return nil, edgeJoinPromptSays(err)
	}
	enroll := func() error {
		_, err := edgeEnrollJoin(cfg, "", base, client.LinkedLogin(), edgeJoinTrust{Pin: rootFP})
		return edgeJoinPromptSays(err)
	}
	if p.already != nil {
		return nil, enroll()
	}
	stop := make(chan struct{})
	var once sync.Once
	return &edge.JoinAsk{
		Phrase: p.phrase,
		Until:  time.Now().Add(pairWaitFor),
		Wait: func() error {
			if _, err := p.wait(stop); err != nil {
				return edgeJoinPromptSays(err)
			}
			return enroll()
		},
		Cancel: func() { once.Do(func() { close(stop) }) },
	}, nil
}

// edgeJoinPromptSays maps what the join path returned onto the prompt's one-line outcomes. The
// authority does not say whether a spent code was used or expired (both are "no live code"), so
// those two read as one line here; the rest are told apart by their typed errors.
func edgeJoinPromptSays(err error) error {
	if err == nil {
		return nil
	}
	var refusal *joinRefusal
	msg := err.Error()
	switch {
	case errors.Is(err, errPairCancelled):
		return err
	case errors.Is(err, edgeauth.ErrNoRootProof):
		return errors.New(look.JoinSaysUsedOrExpired)
	case errors.Is(err, edgeauth.ErrPinMismatch), errors.Is(err, edgeauth.ErrRootProofMismatch):
		return errors.New(look.JoinSaysOtherEdge)
	case strings.Contains(msg, "did not answer in 2 minutes"):
		return errors.New(look.JoinSaysTimeout)
	case errors.As(err, &refusal):
		if strings.Contains(msg, "refused the request") {
			return errors.New(look.JoinSaysRefused)
		}
		return errors.New(firstLine(msg))
	case strings.Contains(msg, "can't reach") || strings.Contains(msg, "could not reach") ||
		strings.Contains(msg, "connection refused") || strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "nothing answered"):
		return errors.New(look.JoinSaysNoAnswer)
	}
	return errors.New(firstLine(msg))
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return strings.TrimSpace(s)
}
