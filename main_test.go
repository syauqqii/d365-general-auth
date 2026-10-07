package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"general-auth/internal/config"
	"general-auth/internal/d365"
	"general-auth/internal/rbac"
)

// The example files are what new installs copy, so they must always load.
func TestExampleFilesAreValid(t *testing.T) {
	t.Setenv("JWT_ACCESS_SECRET", strings.Repeat("a", 32))
	t.Setenv("JWT_REFRESH_SECRET", strings.Repeat("r", 32))
	t.Setenv("JWT_REFRESH_MOBILE_TTL", "1w")

	cfg, err := config.Load("example.config.toml", false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JWT.Refresh.MobileTTL.Duration != 7*24*time.Hour {
		t.Fatalf("env override not applied: %v", cfg.JWT.Refresh.MobileTTL)
	}
	if got := cfg.Database.Params["postgres"]["sslmode"]; got == "" {
		t.Fatal("postgres params not decoded")
	}

	if _, found, err := rbac.Load("example.rbac.toml"); err != nil || !found {
		t.Fatalf("example.rbac.toml: found=%v err=%v", found, err)
	}

	t.Setenv("D365_TENANT_ID", "tenant")
	t.Setenv("D365_CLIENT_ID", "client")
	t.Setenv("D365_CLIENT_SECRET", "secret")
	t.Setenv("D365_CONNECTOR", "example.connector-bc.toml, example.connector-fo.toml")
	cfg, err = config.Load("example.config.toml", false)
	if err != nil {
		t.Fatal(err)
	}
	conns, err := d365.LoadAll(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"bc": "https://api.businesscentral.dynamics.com/.default",
		"fo": "https://your-env.operations.dynamics.com/.default",
	}
	if len(conns) != len(want) {
		t.Fatalf("got %d connectors, want %d", len(conns), len(want))
	}
	for name, scope := range want {
		c := conns[name]
		if c == nil || c.Instance != name || c.Scope != scope || len(c.Endpoints) == 0 {
			t.Fatalf("connector %s not loaded as expected: %+v", name, c)
		}
	}
}

// config.toml is optional; when present it only overrides the keys it sets.
func TestConfigFileIsOptional(t *testing.T) {
	t.Setenv("JWT_ACCESS_SECRET", strings.Repeat("a", 32))
	t.Setenv("JWT_REFRESH_SECRET", strings.Repeat("r", 32))
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	if _, err := config.Load(path, false); err == nil {
		t.Fatal("expected an error for a required, missing config file")
	}
	cfg, err := config.Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.App.Port != 3000 || cfg.Security.BcryptCost != 12 || cfg.RBAC.File != "rbac.toml" {
		t.Fatalf("built-in defaults not applied: %+v", cfg.App)
	}

	if err := os.WriteFile(path, []byte("[security]\nmax_failed_logins = 9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.MaxFailedLogins != 9 || cfg.Security.LockoutDuration.Duration != 15*time.Minute {
		t.Fatalf("override not merged with defaults: %+v", cfg.Security)
	}
}

// An empty D365_CONNECTOR runs the server with auth only.
func TestNoConnectorDisablesD365(t *testing.T) {
	t.Setenv("JWT_ACCESS_SECRET", strings.Repeat("a", 32))
	t.Setenv("JWT_REFRESH_SECRET", strings.Repeat("r", 32))
	t.Setenv("D365_CONNECTOR", "")
	cfg, err := config.Load("example.config.toml", false)
	if err != nil {
		t.Fatal(err)
	}
	conns, err := d365.LoadAll(cfg)
	if err != nil || len(conns) != 0 {
		t.Fatalf("conns=%d err=%v", len(conns), err)
	}
}

func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"15m": 15 * time.Minute, "7d": 7 * 24 * time.Hour, "2w": 14 * 24 * time.Hour,
		"1d12h": 36 * time.Hour, "0": 0,
	}
	for in, want := range cases {
		got, err := config.ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := config.ParseDuration("5x"); err == nil {
		t.Error("expected error for 5x")
	}
}
