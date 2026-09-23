package main

// THE EDGE SETUP BACKEND (features/edge/onboard.feature). One implementation of "start a
// new Edge" and "join an existing Edge", shared by the TUI wizard (through the Hooks seam,
// as LoginBegin/LoginPoll are) and by `roger edge setup`. It is a thin, non-printing layer
// over the same edgeEnrollCore / edgeDesignateCore the `roger edge` commands use, so nothing
// is re-implemented and nothing drifts.

import (
	"errors"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/tui"
)

// edgeSetupHooks fills the TUI's setup seams from this machine's real enrollment backend.
// A machine can always be set up (these never fail to construct); the individual steps
// return errors the wizard shows.
func edgeSetupHooks(cfg config) tui.EdgeSetup {
	return tui.EdgeSetup{
		DefaultName: edgeDefaultName,
		UserKeyHex:  client.UserPubHex,
		// NewEdge: this machine becomes the authority AND enrolls itself, no network. It is
		// idempotent across the two steps: if this machine already roots an Edge (a previous
		// attempt designated it but the enroll failed), it skips re-designating and just
		// finishes the enroll, so an in-place retry recovers instead of hitting ErrRootExists.
		NewEdge: func(name string) error {
			if err := edge.ValidName(name); err != nil {
				return err
			}
			if _, isAuthority, err := edgeauth.OpenLocal(edgeAuthDir()); err == nil && isAuthority {
				_, err := edgeEnrollCore(cfg, name, "", client.LinkedLogin())
				return err
			}
			if _, err := edgeDesignateCore(edgeAuthDir(), name); err != nil {
				return err
			}
			_, err := edgeEnrollCore(cfg, name, "", client.LinkedLogin())
			return err
		},
		// Join: enroll against the authority's LAN address. A refusal because this machine
		// is not on the allow-list comes back as tui.ErrNotAllowedYet so the wizard shows
		// the allow instructions; every other refusal is shown in the authority's words.
		Join: func(name, authorityAddr string) error {
			if err := edge.ValidName(name); err != nil {
				return err
			}
			_, err := edgeEnrollCore(cfg, name, authorityAddr, client.LinkedLogin())
			if errors.Is(err, edgeauth.ErrUnknownMachine) {
				return tui.ErrNotAllowedYet
			}
			// A real refusal stays in the authority's words; an unreachable authority is named
			// as such, with the address, rather than shown as a raw dial error.
			return edgeSetupJoinError(authorityAddr, err)
		},
		// Finish: this machine already designated an authority but never enrolled itself.
		EnrollSelf: func() error {
			name := edgeDefaultName()
			if d, _, _ := edgeIdentityStore().Descriptor(); d.Where != "" {
				name = d.Where
			}
			_, err := edgeEnrollCore(cfg, name, "", client.LinkedLogin())
			return err
		},
	}
}

// edgeSetupState tells the wizard what half-done state this machine is in, so it can start
// at the right step (the audit's finding #4: authority-here-but-not-enrolled is the one
// real "finish this" gap).
func edgeSetupState() tui.EdgeSetupState {
	st := edgeSelfStatus(nil, edgeDiscoveryFactsFromEnv())
	return tui.EdgeSetupState{
		Enrolled:      st.Enrolled,
		AuthorityHere: st.AuthorityHere,
		AuthorityAddr: st.AuthorityAddr,
	}
}
