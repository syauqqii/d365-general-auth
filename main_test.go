package main

import (
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

	cfg, err := config.Load("example.config.toml")
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

	for _, p := range []string{"BC", "FO"} {
		t.Setenv(p+"_TENANT_ID", "tenant")
		t.Setenv(p+"_CLIENT_ID", "client")
		t.Setenv(p+"_CLIENT_SECRET", "secret")
	}
	t.Setenv("FO_ENV_HOST", "contoso.operations.dynamics.com")
	for _, f := range []string{"example.connector-bc.toml", "example.connector-fo.toml"} {
		c, err := d365.Load(f, f)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Endpoints) == 0 {
			t.Fatalf("%s has no endpoints", f)
		}
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
