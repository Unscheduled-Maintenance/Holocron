package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(EnvConfig, "")
	t.Setenv(EnvDB, "")
	t.Setenv(EnvData, filepath.Join(dir, "data"))
	return dir
}

func TestLoadDefaultsWithoutFile(t *testing.T) {
	dir := isolate(t)
	cfg, p, err := Load(filepath.Join(dir, "missing.toml"), "")
	if err == nil {
		t.Fatal("explicit missing config file should be an error")
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("APPDATA", filepath.Join(dir, "cfg"))
	t.Setenv("HOME", dir)
	cfg, p, err = Load("", "")
	if err != nil {
		t.Fatalf("Load without file: %v", err)
	}
	if cfg.Reports.MaxItems != 8 || !cfg.ShorthandEnabled() || !cfg.CreateProjectsEnabled() {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if want := filepath.Join(dir, "data", "holocron.db"); p.Database != want {
		t.Fatalf("database = %q, want %q", p.Database, want)
	}
	if p.BackupDir != filepath.Join(dir, "data", "backups") {
		t.Fatalf("backup dir = %q", p.BackupDir)
	}
}

func TestLoadFileAndPrecedence(t *testing.T) {
	dir := isolate(t)
	file := filepath.Join(dir, "config.toml")
	content := `
database = "` + filepath.ToSlash(filepath.Join(dir, "from-config.db")) + `"
week_start = "sunday"
clock = "12h"
[capture]
shorthand = false
[reports]
max_items = 3
[git]
author_emails = ["me@example.com"]
`
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, p, err := Load(file, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ShorthandEnabled() || !cfg.CreateProjectsEnabled() || cfg.Reports.MaxItems != 3 || !cfg.Use12HourClock() {
		t.Fatalf("file values not applied: %+v", cfg)
	}
	if cfg.Reports.StaffRange != "this-week" {
		t.Fatalf("unset values should keep defaults, got %q", cfg.Reports.StaffRange)
	}
	if filepath.Base(p.Database) != "from-config.db" {
		t.Fatalf("config database not used: %s", p.Database)
	}

	t.Setenv(EnvDB, filepath.Join(dir, "from-env.db"))
	_, p, _ = Load(file, "")
	if filepath.Base(p.Database) != "from-env.db" {
		t.Fatalf("env should beat config: %s", p.Database)
	}
	_, p, _ = Load(file, filepath.Join(dir, "from-flag.db"))
	if filepath.Base(p.Database) != "from-flag.db" {
		t.Fatalf("flag should beat env: %s", p.Database)
	}
}

func TestLoadRejectsBadConfig(t *testing.T) {
	dir := isolate(t)
	cases := map[string]string{
		"unknown key":  "colour = \"red\"\n",
		"bad weekday":  "week_start = \"caturday\"\n",
		"bad clock":    "clock = \"36h\"\n",
		"bad range":    "[tui]\ndefault_range = \"fortnight\"\n",
		"bad provider": "[ai]\nprovider = \"skynet\"\n",
		"syntax error": "week_start = \n",
		"wrong type":   "[reports]\nmax_items = \"lots\"\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".toml")
			if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := Load(file, ""); err == nil {
				t.Fatalf("Load accepted %q", content)
			}
		})
	}
}

func TestTemplateIsValid(t *testing.T) {
	cfg := Default()
	if err := decode([]byte(Template), &cfg); err != nil {
		t.Fatalf("template does not parse: %v", err)
	}
	if _, err := Encode(Default()); err != nil {
		t.Fatal(err)
	}
}
