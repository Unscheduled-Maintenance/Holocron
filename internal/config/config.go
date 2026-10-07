// Package config locates Holocron's files and loads the optional TOML
// configuration file.
//
// Holocron works with no configuration file at all; every setting has a
// default. The configuration file never holds secrets.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
	"github.com/Unscheduled-Maintenance/Holocron/internal/timerange"
)

// Environment variables that override file locations.
const (
	EnvConfig = "HOLOCRON_CONFIG"
	EnvDB     = "HOLOCRON_DB"
	EnvData   = "HOLOCRON_DATA_DIR"
)

// Config is the user's configuration.
type Config struct {
	// Database is an explicit database path. Empty means the platform default.
	Database string `toml:"database"`
	// Editor overrides $VISUAL/$EDITOR for Holocron only.
	Editor string `toml:"editor"`
	// WeekStart is the first day of the week ("monday" or "sunday").
	WeekStart string `toml:"week_start"`
	// Clock is "24h" or "12h".
	Clock string `toml:"clock"`

	// TypeAliases maps shorthand names to entry types ("win" =
	// "accomplishment"). They add to the built-in aliases; an empty value
	// removes one.
	TypeAliases map[string]string `toml:"type_aliases"`

	Capture CaptureConfig `toml:"capture"`
	Reports ReportsConfig `toml:"reports"`
	TUI     TUIConfig     `toml:"tui"`
	Git     GitConfig     `toml:"git"`
	AI      AIConfig      `toml:"ai"`
	Sync    SyncConfig    `toml:"sync"`
	Backup  BackupConfig  `toml:"backup"`
}

// BackupConfig controls `holocron backup` (docs/adr/0008-encrypted-backups.md).
type BackupConfig struct {
	// EncryptTo encrypts backups by default, to "passphrase" (asked for each
	// time), "sync-key", or SSH public key files ("~/.ssh/id_ed25519.pub").
	// A passphrase cannot be combined with anything else.
	EncryptTo []string `toml:"encrypt_to"`
}

// CaptureConfig controls `holocron add`.
type CaptureConfig struct {
	// Shorthand enables +project and #tag parsing in entry text.
	Shorthand *bool `toml:"shorthand"`
	// CreateProjects creates unknown projects on first use instead of failing.
	CreateProjects *bool `toml:"create_projects"`
}

// ReportsConfig controls deterministic reports.
type ReportsConfig struct {
	// MaxItems caps the bullets in each report section.
	MaxItems int `toml:"max_items"`
	// OneOnOneRange is the default range for the one-on-one report.
	OneOnOneRange string `toml:"one_on_one_range"`
	// StaffRange is the default range for the staff report.
	StaffRange string `toml:"staff_range"`
	// StaffEarlyDays makes a this-week staff report cover last week instead
	// when run within this many days of the start of the week. 0 disables it.
	StaffEarlyDays int `toml:"staff_early_days"`
	// OpenLookback is how far back to look for unresolved problems and follow-ups.
	OpenLookback string `toml:"open_lookback"`
}

// TUIConfig controls the interactive interface.
type TUIConfig struct {
	// DefaultRange is the range shown when the TUI starts.
	DefaultRange string `toml:"default_range"`
}

// GitConfig controls `holocron import git`.
type GitConfig struct {
	// AuthorEmails are additional emails that identify the user's commits.
	AuthorEmails []string `toml:"author_emails"`
}

// SyncConfig controls multi-device sync (docs/adr/0007-multi-device-sync.md).
// Where an archive syncs is kept in the archive itself, set by
// `holocron sync init` or `join`; these are this computer's preferences.
type SyncConfig struct {
	// KeyCommand prints the sync key, for example from a password manager:
	// "op read op://Private/Holocron/sync-key". It runs only when the OS
	// keychain does not have the key.
	KeyCommand string `toml:"key_command"`
	// Auto pulls before and publishes after every command. Default true.
	Auto *bool `toml:"auto"`
}

// SyncAutoEnabled reports whether commands sync automatically.
func (c Config) SyncAutoEnabled() bool { return c.Sync.Auto == nil || *c.Sync.Auto }

// AIConfig selects an optional AI provider. API keys are never stored here.
type AIConfig struct {
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	// APIKeyEnv names the environment variable holding the API key.
	APIKeyEnv string `toml:"api_key_env"`
	// BaseURL overrides the provider endpoint (for proxies or gateways).
	BaseURL string `toml:"base_url"`
}

// Default returns the built-in defaults.
func Default() Config {
	shorthand, create := true, true
	return Config{
		WeekStart:   "monday",
		Clock:       "24h",
		TypeAliases: journal.DefaultTypeAliases(),
		Capture:     CaptureConfig{Shorthand: &shorthand, CreateProjects: &create},
		Reports: ReportsConfig{
			MaxItems:       8,
			OneOnOneRange:  "14d",
			StaffRange:     "this-week",
			StaffEarlyDays: 1,
			OpenLookback:   "90d",
		},
		TUI: TUIConfig{DefaultRange: "this-week"},
	}
}

// ShorthandEnabled reports whether +project/#tag parsing is on.
func (c Config) ShorthandEnabled() bool {
	return c.Capture.Shorthand == nil || *c.Capture.Shorthand
}

// CreateProjectsEnabled reports whether unknown projects are created on use.
func (c Config) CreateProjectsEnabled() bool {
	return c.Capture.CreateProjects == nil || *c.Capture.CreateProjects
}

// Use12HourClock reports whether times render as 2:05pm rather than 14:05.
func (c Config) Use12HourClock() bool { return strings.EqualFold(c.Clock, "12h") }

// Validate checks values that cannot be checked by the TOML decoder.
func (c Config) Validate() error {
	var errs []error
	if _, err := timerange.ParseWeekday(c.WeekStart); err != nil {
		errs = append(errs, fmt.Errorf("week_start: %w", err))
	}
	switch strings.ToLower(c.Clock) {
	case "", "24h", "12h":
	default:
		errs = append(errs, fmt.Errorf("clock: must be \"24h\" or \"12h\", got %q", c.Clock))
	}
	if _, err := journal.NewTypeAliases(c.TypeAliases); err != nil {
		errs = append(errs, fmt.Errorf("type_aliases: %w", err))
	}
	if c.Reports.MaxItems < 0 {
		errs = append(errs, fmt.Errorf("reports.max_items: must not be negative"))
	}
	if c.Reports.StaffEarlyDays < 0 || c.Reports.StaffEarlyDays > 6 {
		errs = append(errs, fmt.Errorf("reports.staff_early_days: must be between 0 and 6"))
	}
	probe := timerange.NewClock(timeNow(), nil, 0)
	for name, v := range map[string]string{
		"reports.one_on_one_range": c.Reports.OneOnOneRange,
		"reports.staff_range":      c.Reports.StaffRange,
		"reports.open_lookback":    c.Reports.OpenLookback,
		"tui.default_range":        c.TUI.DefaultRange,
	} {
		if v == "" {
			continue
		}
		if _, err := probe.Parse(v); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	for _, r := range c.Backup.EncryptTo {
		switch {
		case r == "":
			errs = append(errs, errors.New("backup.encrypt_to: empty entry"))
		case (r == "passphrase" || r == "sync-key") && len(c.Backup.EncryptTo) > 1:
			errs = append(errs, fmt.Errorf("backup.encrypt_to: %q cannot be combined with other recipients", r))
		}
	}
	switch strings.ToLower(c.AI.Provider) {
	case "", "none", "anthropic":
	default:
		errs = append(errs, fmt.Errorf("ai.provider: unknown provider %q (supported: anthropic)", c.AI.Provider))
	}
	return errors.Join(errs...)
}

// Paths describes where Holocron keeps its files.
type Paths struct {
	ConfigFile string
	DataDir    string
	Database   string
	BackupDir  string
}

// Load resolves file locations and reads the configuration file, if any.
// configOverride and dbOverride come from command-line flags and win over
// environment variables, which win over the configuration file.
func Load(configOverride, dbOverride string) (Config, Paths, error) {
	cfg := Default()
	var p Paths

	cfgFile, err := configPath(configOverride)
	if err != nil {
		return cfg, p, err
	}
	p.ConfigFile = cfgFile

	// A configuration problem is reported after the paths are resolved, so
	// callers such as `config path`, `config edit` and `doctor` can still
	// say where everything lives.
	var cfgErr error
	data, err := os.ReadFile(cfgFile)
	switch {
	case err == nil:
		if err := decode(data, &cfg); err != nil {
			cfg = Default()
			cfgErr = fmt.Errorf("reading configuration %s: %w (fix it with `holocron config edit`)", cfgFile, err)
		}
	case errors.Is(err, fs.ErrNotExist):
		if configOverride != "" || os.Getenv(EnvConfig) != "" {
			cfgErr = fmt.Errorf("configuration file %s does not exist; create it with `holocron config edit`, or remove --config / %s to use the defaults", cfgFile, EnvConfig)
		}
	default:
		cfgErr = fmt.Errorf("reading configuration %s: %w", cfgFile, err)
	}

	dataDir, err := dataDir()
	if err != nil {
		return cfg, p, err
	}
	p.DataDir = dataDir
	p.BackupDir = filepath.Join(dataDir, "backups")

	switch {
	case dbOverride != "":
		p.Database = dbOverride
	case os.Getenv(EnvDB) != "":
		p.Database = os.Getenv(EnvDB)
	case cfg.Database != "":
		p.Database = ExpandHome(cfg.Database)
	default:
		p.Database = filepath.Join(dataDir, "holocron.db")
	}
	if abs, err := filepath.Abs(p.Database); err == nil {
		p.Database = abs
	}
	return cfg, p, cfgErr
}

func decode(data []byte, cfg *Config) error {
	md, err := toml.NewDecoder(bytes.NewReader(data)).Decode(cfg)
	if err != nil {
		return err
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		keys := make([]string, len(undec))
		for i, k := range undec {
			keys[i] = k.String()
		}
		return fmt.Errorf("unknown setting(s): %s", strings.Join(keys, ", "))
	}
	return cfg.Validate()
}

func configPath(override string) (string, error) {
	if override != "" {
		return ExpandHome(override), nil
	}
	if env := os.Getenv(EnvConfig); env != "" {
		return ExpandHome(env), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine the configuration directory: %w (set %s)", err, EnvConfig)
	}
	return filepath.Join(dir, "holocron", "config.toml"), nil
}

// dataDir returns the platform's conventional location for application data:
//
//	Windows  %LOCALAPPDATA%\holocron   (local, not roaming: SQLite and roaming profiles do not mix)
//	macOS    ~/Library/Application Support/holocron
//	Linux    $XDG_DATA_HOME/holocron or ~/.local/share/holocron
func dataDir() (string, error) {
	if env := os.Getenv(EnvData); env != "" {
		return ExpandHome(env), nil
	}
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "holocron"), nil
		}
	case "darwin":
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, "Library", "Application Support", "holocron"), nil
		}
	default:
		if d := os.Getenv("XDG_DATA_HOME"); d != "" && filepath.IsAbs(d) {
			return filepath.Join(d, "holocron"), nil
		}
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, ".local", "share", "holocron"), nil
		}
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine a data directory: %w (set %s)", err, EnvData)
	}
	return filepath.Join(dir, "holocron"), nil
}

// ExpandHome expands a leading ~ to the user's home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// Encode renders a configuration as TOML.
func Encode(cfg Config) (string, error) {
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	enc.Indent = ""
	if err := enc.Encode(cfg); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Template is written by `holocron config edit` when no file exists yet.
const Template = `# Holocron configuration.
# Every setting is optional; delete anything you do not need.
# Secrets (such as AI API keys) never belong in this file.

# database = "~/holocron/holocron.db"   # default: the platform data directory
# editor = "code --wait"                # default: $VISUAL, then $EDITOR
# week_start = "monday"                 # or "sunday"
# clock = "24h"                         # or "12h"

[capture]
# shorthand = true          # parse +project and #tag in entry text
# create_projects = true    # create unknown projects on first use

[type_aliases]
# Shorthand for entry types, usable wherever a type is typed ("Win: ...",
# --type win). Entries always store the real type. These add to the
# built-in aliases below; set one to "" to remove it.
# win = "accomplishment"    # built in
# look = "investigation"    # built in
# ship = "accomplishment"

[reports]
# max_items = 8             # bullets per report section
# staff_range = "this-week"
# staff_early_days = 1     # early in the week, a this-week staff report covers last week (0 = off)
# one_on_one_range = "14d"
# open_lookback = "90d"     # how far back to look for open problems and follow-ups

[tui]
# default_range = "this-week"

[git]
# author_emails = ["me@example.com"]   # in addition to git's user.email

[sync]
# key_command = "op read op://Private/Holocron/sync-key"   # prints the sync key when the keychain lacks it
# auto = true               # sync before and after every command (set up with "holocron sync init" or "join")

[backup]
# encrypt_to = ["sync-key"]   # encrypt "holocron backup" by default: "passphrase", "sync-key" or SSH public key files

[ai]
# provider = "anthropic"                 # empty disables AI; nothing is sent anywhere
# model = "claude-sonnet-5-5"
# api_key_env = "ANTHROPIC_API_KEY"      # name of the environment variable holding the key
# base_url = ""                         # optional proxy or gateway URL
`

var timeNow = time.Now
