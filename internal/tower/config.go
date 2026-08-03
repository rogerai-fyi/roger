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
	Observability   ObservConfig   `yaml:"observability"`
	Limits          LimitsConfig   `yaml:"limits"`
	Storage         *StorageConfig `yaml:"storage,omitempty"`
	Payout          *PayoutConfig  `yaml:"payout,omitempty"`

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

// ListenAddresses is every address this Tower will bind, so a caller (and `doctor`) can
// assert the loopback guarantee without reaching into each listener.
func (c *Config) ListenAddresses() []string {
	return []string{
		c.StationListener.Address,
		c.AdminListener.Address,
		c.Observability.MetricsAddress,
	}
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
	fmt.Fprintf(&b, "publicAdvertisement: %v\n", c.AdvertisesPublicly())
	if c.Storage != nil && c.Storage.URLFile != "" {
		fmt.Fprintf(&b, "storage.urlFile: %s (contents not read)\n", c.Storage.URLFile)
	}
	return b.String()
}
