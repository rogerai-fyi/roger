package tower

// Durable startup: a Tower that cannot keep its state must refuse service rather than
// serve and silently lose it.
//
// Contract: features/tower/modes.feature. The spec names six dependency classes, and the
// reason it names six rather than saying "check the dependencies" is that each has a
// DIFFERENT repair. A readiness probe reporting only "not ready" sends an operator
// hunting through logs; every problem here carries an instruction.
//
// What this does NOT claim: the durable profile verifies the dependencies a durable Tower
// rests on, but Tower state still lives in the data directory. Moving it into PostgreSQL
// is separate work, and pretending otherwise would be exactly the silent data loss this
// file exists to prevent.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Profile is the durability contract an operator has chosen.
type Profile string

const (
	// ProfileDevelopment keeps state where a crash or a container restart can take it.
	// Usable, and never quiet about what it is.
	ProfileDevelopment Profile = "development"
	// ProfileDurable promises the state survives a restart, so every dependency that
	// promise rests on is checked before the Tower will serve.
	ProfileDurable Profile = "durable"
)

// Dependency names what failed, so a caller can act on it rather than parse prose.
type Dependency string

const (
	DepIdentityVolume Dependency = "identity volume"
	DepTrustRoot      Dependency = "offline root and trust history"
	DepOperator       Dependency = "bootstrap verifier and local operator"
	DepReceiptSigner  Dependency = "local receipt-ledger signing key"
	DepDatabase       Dependency = "database"
)

// Problem is one unmet dependency and what to do about it.
type Problem struct {
	Dependency Dependency
	Detail     string // what is wrong
	Repair     string // what to DO - deliberately not a restatement of Detail
}

// Readiness is the answer to "may this Tower serve?".
type Readiness struct {
	Profile  Profile
	OK       bool
	Problems []Problem
	Warnings []string
}

// Profile returns the configured durability contract, defaulting to development so an
// operator who says nothing gets the honest label rather than an unearned promise.
func (c *Config) Profile() Profile {
	if c.Storage == nil || c.Storage.Profile == "" {
		return ProfileDevelopment
	}
	return Profile(c.Storage.Profile)
}

// IsDurable reports whether this Tower has promised its state survives a restart.
func (c *Config) IsDurable() bool { return c.Profile() == ProfileDurable }

// Ready checks everything the configured profile depends on.
func Ready(c *Config) Readiness {
	r := Readiness{Profile: c.Profile(), OK: true}

	// A joined Tower keeps no local trust root or admission history - Roger Core holds
	// the state that matters, so there is no local durability contract to verify.
	if c.Mode != ModeStandalone {
		return r
	}

	if !c.IsDurable() {
		r.Warnings = append(r.Warnings,
			"this Tower runs the development profile: identity, admission state and attached Stations may be LOST on restart")
		return r
	}

	dir := c.Identity.Dir
	if dir == "" {
		r.fail(DepIdentityVolume, "no identity directory is configured",
			"set identity.dir to a durable volume, then run `roger-tower init --dir <that path> --mode standalone`")
		return r.done()
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		r.fail(DepIdentityVolume, fmt.Sprintf("%s is not a readable directory", dir),
			"mount a durable volume at that path, then run `roger-tower init --dir "+dir+" --mode standalone`")
		return r.done()
	}

	// The pinned root is what every locally admitted client checks on reconnect. Without
	// it this is not the same network any more.
	if _, err := os.Stat(filepath.Join(dir, offlineRoot)); err != nil {
		r.fail(DepTrustRoot, "the pinned offline root is missing from the identity directory",
			"restore the identity volume from backup; a standalone network cannot be re-rooted without invalidating every admitted client")
	}
	if _, err := os.Stat(filepath.Join(dir, identityKey)); err != nil {
		r.fail(DepReceiptSigner, "the local signing key is missing from the identity directory",
			"restore the identity volume from backup; receipts already issued cannot be verified without it")
	}

	// Admission history. A durable Tower with no operator has nobody who can attach a
	// Station or route a request, so serving would be theatre.
	if c.RequireOperator {
		st, err := Open(dir)
		if err != nil {
			r.fail(DepOperator, "the Tower state file is unreadable",
				"restore the identity volume from backup, or initialize a new data directory if this network is being rebuilt")
		} else if _, err := st.LocalOperator(); err != nil {
			r.fail(DepOperator, "this network has no local operator",
				"run `roger-tower invite --dir "+dir+" --client <key hash>` and redeem it with `roger-tower admit`")
		}
	}

	// The database secret is read as a FILE; a missing one is a deployment mistake with a
	// precise fix, not a mysterious startup failure.
	if c.Storage != nil && c.Storage.URLFile != "" {
		if _, err := os.ReadFile(c.Storage.URLFile); err != nil {
			r.fail(DepDatabase, "the database URL file cannot be read: "+c.Storage.URLFile,
				"mount the secret at that path with owner-only permissions, or remove storage.urlFile if this Tower has no database")
		}
	}
	return r.done()
}

func (r *Readiness) fail(dep Dependency, detail, repair string) {
	r.Problems = append(r.Problems, Problem{Dependency: dep, Detail: detail, Repair: repair})
}

func (r Readiness) done() Readiness {
	r.OK = len(r.Problems) == 0
	return r
}

// String renders the report for a terminal or a log line.
func (r Readiness) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "profile: %s\n", r.Profile)
	if r.Profile == ProfileDevelopment {
		fmt.Fprintf(&b, "durability: NOT DURABLE - state may be lost on restart\n")
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	for _, p := range r.Problems {
		fmt.Fprintf(&b, "\n%s: %s\n", p.Dependency, p.Detail)
		fmt.Fprintf(&b, "  repair: %s\n", p.Repair)
	}
	if r.OK {
		fmt.Fprintf(&b, "\nreadiness: READY\n")
	} else {
		fmt.Fprintf(&b, "\nreadiness: NOT READY - refusing to serve rather than lose state silently\n")
	}
	return b.String()
}
