// Package tower is the core of `roger-tower`, the self-hosted relay an operator runs to
// serve local Stations (standalone mode) or to join the public RogerAI network as an
// untrusted child relay (joined mode).
//
// It is deliberately NOT the broker. The broker combines relay with identity, policy,
// money, admin and platform signing; a Tower gets none of that. See
// docs/tower-network-plan.md and the approved specs under features/tower/.
package tower

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Mode is the Tower's network mode. There are exactly two, and a data directory is
// initialized as one of them for life: changing mode in place would carry a trust root,
// identity or Station registry across a boundary that exists precisely to separate them.
type Mode string

const (
	// ModeJoined is an untrusted child relay of the public RogerAI network. Roger Core
	// remains the admission, routing, policy, settlement and revocation authority.
	ModeJoined Mode = "joined"
	// ModeStandalone is a self-governed local network with its own trust root. It has
	// no path to public RogerAI discovery, settlement, or advertisement - and that is
	// structural, not a setting.
	ModeStandalone Mode = "standalone"
)

// ParseMode accepts exactly the two supported modes, spelled exactly. It is deliberately
// strict about case and whitespace: a Tower that guesses what "Joined " meant is a Tower
// that could guess wrong about which network it belongs to.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeJoined, ModeStandalone:
		return Mode(s), nil
	default:
		return "", fmt.Errorf("mode must be %q or %q", ModeJoined, ModeStandalone)
	}
}

// Supported API version and kind. A config that does not name both is rejected rather
// than assumed, so a future incompatible schema cannot be silently half-read.
const (
	APIVersion = "tower.rogerai.fm/v1alpha1"
	Kind       = "Tower"
)

// Default loopback listeners. Standalone binds loopback unless the operator explicitly
// asks for LAN or cluster serving; nothing is exposed by omission.
const (
	DefaultStationAddress = "127.0.0.1:7070"
	DefaultAdminAddress   = "127.0.0.1:7071"
	DefaultMetricsAddress = "127.0.0.1:9090"
)

// Config is the whole of a Tower's configuration. Decoding is strict: an unknown field
// is an error, so a typo silently disabling a control is not possible.
type Config struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Mode       Mode   `yaml:"mode"`

	Identity        IdentityConfig `yaml:"identity"`
	Joined          *JoinedConfig  `yaml:"joined,omitempty"`
	Standalone      *LocalConfig   `yaml:"standalone,omitempty"`
	StationListener ListenerConfig `yaml:"stationListener"`
	AdminListener   ListenerConfig `yaml:"adminListener"`
	Relay           *RelayConfig   `yaml:"relay,omitempty"`
	Hub             *HubConfig     `yaml:"hub,omitempty"`
	Observability   ObservConfig   `yaml:"observability"`
	Limits          LimitsConfig   `yaml:"limits"`
	Storage         *StorageConfig `yaml:"storage,omitempty"`
	Payout          *PayoutConfig  `yaml:"payout,omitempty"`

	// RequireOperator makes readiness insist this network has admitted its local
	// operator. Off by default so a freshly initialized durable Tower can be checked
	// before anyone has been admitted.
	RequireOperator bool `yaml:"requireOperator,omitempty"`

	// PublicAdvertisement is expressible only so it can be REJECTED in standalone mode
	// with a clear error. A standalone Tower has no public advertisement path at all.
	PublicAdvertisement bool `yaml:"publicAdvertisement,omitempty"`
}

type IdentityConfig struct {
	Dir string `yaml:"dir,omitempty"`
	// Key exists only to reject it: private material is supplied as an owner-only file
	// in Dir, never inline where it would reach shell history or a config backup.
	Key string `yaml:"key,omitempty"`
}

type JoinedConfig struct {
	Authority           string `yaml:"authority,omitempty"`
	EnrollmentTokenFile string `yaml:"enrollmentTokenFile,omitempty"`
	CertificateFile     string `yaml:"certificateFile,omitempty"`
	// EnrollmentToken exists only to reject an inline secret.
	EnrollmentToken string `yaml:"enrollmentToken,omitempty"`
}

type LocalConfig struct {
	OfflineRootFile      string `yaml:"offlineRootFile,omitempty"`
	TrustPublicationFile string `yaml:"trustPublicationFile,omitempty"`
	SettlementSignerFile string `yaml:"settlementSignerFile,omitempty"`
}

type ListenerConfig struct {
	Address string `yaml:"address,omitempty"`
}

// HubConfig is the TOPOLOGY-2 DATA PLANE: the hub listener where consumers submit sealed
// work and this tower's self-attached `roger share` nodes poll for it. The payload is
// sealed end-to-end; TLS here covers the node polling tokens and grant metadata. Flags
// (--hub, --hub-tls-cert, --hub-tls-key) win when both are given, like the relay's.
type HubConfig struct {
	Address string `yaml:"address,omitempty"`
	TLSCert string `yaml:"tlsCert,omitempty"`
	TLSKey  string `yaml:"tlsKey,omitempty"`
}

// RelayConfig described the RETIRED TLS-splice data plane. Only Public survives as live
// configuration - it is the address Core advertises for whichever plane serves, which is now
// the hub. Address and Stations are kept ONLY so an existing operator's file still parses
// (decoding is strict: deleting them would turn a running Tower's config into a hard error on
// upgrade). They are reported by Unenforced so an operator is told they do nothing, rather
// than believing a relay is configured.
//
// This is the only listener this build actually binds, and it is the one most likely to be
// public - so it is the one an operator most needs `doctor` to talk about. See relay.go in
// cmd/roger-tower: nothing here terminates TLS, so the address is a routing decision rather
// than a place secrets live.
type RelayConfig struct {
	// Address is DEAD: the TLS-splice relay was removed. See Unenforced.
	Address string `yaml:"address,omitempty"`
	// Public is the host:port CONSUMERS reach this relay at, advertised to Roger Core on the
	// link. The listen address is very often not it - ":8443" is not dialable by anyone -
	// and without a public address Core will not route edge consumers here at all.
	Public string `yaml:"public,omitempty"`
	// Stations mapped a Station ID to where this Tower reached it. DEAD: nodes self-attach
	// and poll the hub; a Tower dials nobody. See Unenforced.
	Stations map[string]string `yaml:"stations,omitempty"`
}

type ObservConfig struct {
	LogFormat      string `yaml:"logFormat,omitempty"`
	MetricsAddress string `yaml:"metricsAddress,omitempty"`
}

type LimitsConfig struct {
	MaxStations      int `yaml:"maxStations,omitempty"`
	MaxInflight      int `yaml:"maxInflight,omitempty"`
	MaxAudioInflight int `yaml:"maxAudioInflight,omitempty"`
}

type StorageConfig struct {
	// Profile is the durability contract: "development" (state may be lost) or
	// "durable" (checked before the Tower will serve). Empty means development, so an
	// operator who says nothing gets the honest label rather than an unearned promise.
	Profile string `yaml:"profile,omitempty"`
	URLFile string `yaml:"urlFile,omitempty"`
	// URL exists only to reject a DSN carrying an inline password.
	URL string `yaml:"url,omitempty"`
}

type PayoutConfig struct {
	Wallet string `yaml:"wallet,omitempty"`
}

// ParseConfig decodes and fully validates Tower configuration. It never returns a
// partially valid Config: either the whole document is coherent for its mode, or the
// caller gets an error and nothing else.
func ParseConfig(b []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true) // an unknown field is an error, not a silently ignored control
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("invalid Tower configuration: %w", err)
	}
	if c.APIVersion != APIVersion {
		return nil, fmt.Errorf("apiVersion must be %q", APIVersion)
	}
	if c.Kind != Kind {
		return nil, fmt.Errorf("kind must be %q", Kind)
	}
	if _, err := ParseMode(string(c.Mode)); err != nil {
		return nil, err
	}
	if err := c.rejectInlineSecrets(); err != nil {
		return nil, err
	}
	if err := c.validateForMode(); err != nil {
		return nil, err
	}
	if c.Storage != nil && c.Storage.Profile != "" {
		switch Profile(c.Storage.Profile) {
		case ProfileDevelopment, ProfileDurable:
		default:
			return nil, fmt.Errorf("storage.profile must be %q or %q", ProfileDevelopment, ProfileDurable)
		}
	}
	c.applyDefaults()
	return &c, nil
}

// rejectInlineSecrets fails on any secret supplied as a scalar. The error names the
// FIELD and never echoes the value, so a rejection cannot itself leak the secret into a
// log or a terminal scrollback.
func (c *Config) rejectInlineSecrets() error {
	if c.Identity.Key != "" {
		return errors.New("identity.key must not be set inline: supply private material as an owner-only file under identity.dir")
	}
	if c.Joined != nil && c.Joined.EnrollmentToken != "" {
		return errors.New("joined.enrollmentToken must not be set inline: use joined.enrollmentTokenFile")
	}
	if c.Storage != nil && c.Storage.URL != "" {
		return errors.New("storage.url must not be set inline (it carries a password): use storage.urlFile")
	}
	return nil
}

// validateForMode enforces the structural separation between the two modes. This is the
// load-bearing function of the whole package: standalone isolation is real only because
// the fields that could reach the public network are rejected here rather than defaulted
// off somewhere a later edit could flip.
func (c *Config) validateForMode() error {
	switch c.Mode {
	case ModeStandalone:
		if c.Joined != nil {
			return errors.New("standalone mode accepts no joined configuration: a standalone Tower has no public authority, enrollment token, or joined certificate")
		}
		if c.PublicAdvertisement {
			return errors.New("standalone mode cannot advertise publicly: a standalone Tower has no public directory path")
		}
		if c.Payout != nil {
			return errors.New("standalone mode has no RogerAI credit or payout: local routing is free and locally accounted in v1")
		}
	case ModeJoined:
		if c.Standalone != nil {
			return errors.New("joined mode accepts no standalone authority configuration: Roger Core is the trust root and settlement authority, not this Tower")
		}
		if c.Joined == nil || c.Joined.Authority == "" {
			return errors.New("joined mode requires joined.authority")
		}
	}
	return nil
}

// applyDefaults fills the effective values a redacted print must be able to show. Every
// default is loopback: nothing becomes reachable because a field was omitted.
// applyDefaults fills in what an operator left unsaid.
//
// The defaults stay for the fields this build does not yet enforce, so that a configuration
// written against the full spec still round-trips and `doctor` can show what WOULD be used.
// Unenforced is what keeps that from reading as a promise: a field left at its default is
// not reported as ignored, because the operator did not ask for anything.
func (c *Config) applyDefaults() {
	if c.StationListener.Address == "" {
		c.StationListener.Address = DefaultStationAddress
	}
	if c.AdminListener.Address == "" {
		c.AdminListener.Address = DefaultAdminAddress
	}
	if c.Observability.MetricsAddress == "" {
		c.Observability.MetricsAddress = DefaultMetricsAddress
	}
	if c.Observability.LogFormat == "" {
		c.Observability.LogFormat = "json"
	}
}

// ListenAddresses is every address this Tower ACTUALLY BINDS.
//
// It used to return the station, admin and metrics addresses. This build binds none of
// those - see Unenforced - and `doctor` was giving a loopback verdict on three listeners
// that did not exist while saying nothing about the relay, which does exist and is meant to
// face the public internet. A security assessment of imaginary ports is worse than none,
// because an operator reads "all listeners loopback" and stops looking.
func (c *Config) ListenAddresses() []string {
	// HUB ONLY. relay.address is dead configuration (see Unenforced) and listing it here
	// would put doctor right back in the failure its own comment warns about: assessing a
	// port nothing opens, while the operator reads a verdict about a listener that does not
	// exist. What this build binds is the hub, or nothing.
	if c.Hub == nil || c.Hub.Address == "" {
		return nil
	}
	return []string{c.Hub.Address}
}

// Unenforced names every field this build decodes and validates but does not act on.
//
// IT IS A TABLE RATHER THAN PROSE so it cannot drift from the truth quietly: wiring one of
// these up means deleting its line here, and the test that pins this list fails until
// somebody does. A configuration control that is accepted, echoed back by `doctor`, and
// then ignored is worse than one that is missing - the operator believes a limit is in
// force and stops thinking about it.
func (c *Config) Unenforced() []string {
	var out []string
	add := func(cond bool, name, what string) {
		if cond {
			out = append(out, name+": "+what)
		}
	}
	add(c.StationListener.Address != "" && c.StationListener.Address != DefaultStationAddress,
		"stationListener.address", "not bound by this build; a joined Tower dials out and does not accept Station connections")
	add(c.AdminListener.Address != "" && c.AdminListener.Address != DefaultAdminAddress,
		"adminListener.address", "not bound by this build; there is no admin API yet")
	add(c.Observability.MetricsAddress != "" && c.Observability.MetricsAddress != DefaultMetricsAddress,
		"observability.metricsAddress", "not bound by this build; no metrics endpoint is served")
	add(c.Observability.LogFormat != "" && c.Observability.LogFormat != "json",
		"observability.logFormat", "not applied by this build; logs are plain lines")
	if c.Relay != nil {
		add(c.Relay.Address != "", "relay.address",
			"the TLS-splice relay was removed; serve the sealed hub instead (hub.address, or --hub)")
		add(len(c.Relay.Stations) > 0, "relay.stations",
			"a Tower no longer dials Stations; nodes run `roger share --tower` and poll the hub")
	}
	add(c.Limits.MaxStations > 0, "limits.maxStations", "not enforced by this build")
	add(c.Limits.MaxInflight > 0, "limits.maxInflight", "not enforced by this build")
	add(c.Limits.MaxAudioInflight > 0, "limits.maxAudioInflight", "not enforced by this build")
	if c.Payout != nil {
		add(c.Payout.Wallet != "", "payout.wallet", "not used by this build; Tower work is not yet compensated")
	}
	if c.Standalone != nil {
		add(c.Standalone.OfflineRootFile != "", "standalone.offlineRootFile", "not read by this build")
		add(c.Standalone.TrustPublicationFile != "", "standalone.trustPublicationFile", "not read by this build")
		add(c.Standalone.SettlementSignerFile != "", "standalone.settlementSignerFile", "not read by this build")
	}
	return out
}

// PublicAuthority is the RogerAI endpoint this Tower will dial, or "" when there is
// none. A standalone Tower always returns "" - it has nowhere public to dial.
func (c *Config) PublicAuthority() string {
	if c.Mode != ModeJoined || c.Joined == nil {
		return ""
	}
	return c.Joined.Authority
}

// AdvertisesPublicly reports whether this Tower may appear in the public directory.
func (c *Config) AdvertisesPublicly() bool {
	return c.Mode == ModeJoined && c.PublicAdvertisement
}

// PrintRedacted renders the EFFECTIVE configuration, defaults included, with secret
// paths shown but never read. An operator must be able to see exactly what the Tower
// will do without the printout becoming a way to exfiltrate a key.
func (c *Config) PrintRedacted() string {
	var b strings.Builder
	fmt.Fprintf(&b, "apiVersion: %s\n", c.APIVersion)
	fmt.Fprintf(&b, "kind: %s\n", c.Kind)
	fmt.Fprintf(&b, "mode: %s\n", c.Mode)
	if c.Identity.Dir != "" {
		fmt.Fprintf(&b, "identity.dir: %s\n", c.Identity.Dir)
	}
	if c.Joined != nil {
		fmt.Fprintf(&b, "joined.authority: %s\n", c.Joined.Authority)
		if c.Joined.EnrollmentTokenFile != "" {
			fmt.Fprintf(&b, "joined.enrollmentTokenFile: %s (contents not read)\n", c.Joined.EnrollmentTokenFile)
		}
		if c.Joined.CertificateFile != "" {
			fmt.Fprintf(&b, "joined.certificateFile: %s (contents not read)\n", c.Joined.CertificateFile)
		}
	}
	if c.Standalone != nil {
		for label, path := range map[string]string{
			"standalone.offlineRootFile":      c.Standalone.OfflineRootFile,
			"standalone.trustPublicationFile": c.Standalone.TrustPublicationFile,
			"standalone.settlementSignerFile": c.Standalone.SettlementSignerFile,
		} {
			if path != "" {
				fmt.Fprintf(&b, "%s: %s (contents not read)\n", label, path)
			}
		}
	}
	fmt.Fprintf(&b, "stationListener.address: %s\n", c.StationListener.Address)
	fmt.Fprintf(&b, "adminListener.address: %s\n", c.AdminListener.Address)
	fmt.Fprintf(&b, "observability.metricsAddress: %s\n", c.Observability.MetricsAddress)
	fmt.Fprintf(&b, "observability.logFormat: %s\n", c.Observability.LogFormat)
	fmt.Fprintf(&b, "storage.profile: %s\n", c.Profile())
	fmt.Fprintf(&b, "publicAdvertisement: %v\n", c.AdvertisesPublicly())
	if c.Storage != nil && c.Storage.URLFile != "" {
		fmt.Fprintf(&b, "storage.urlFile: %s (contents not read)\n", c.Storage.URLFile)
	}
	return b.String()
}
