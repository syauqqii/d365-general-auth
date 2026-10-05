package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/joho/godotenv"
)

// APIPrefix is where every API route lives. Versioning the path lets a
// future /api/v2 run next to v1 while clients migrate.
const APIPrefix = "/api/v1"

type Config struct {
	App        App               `toml:"app"`
	JWT        JWT               `toml:"jwt"`
	Cookie     Cookie            `toml:"cookie"`
	Database   Database          `toml:"database"`
	Security   Security          `toml:"security"`
	Account    Account           `toml:"account"`
	RBAC       RBAC              `toml:"rbac"`
	Connectors map[string]string `toml:"connectors"`
	Seed       Seed              `toml:"seed"`

	dir string
}

type App struct {
	Name           string   `toml:"name"`
	Env            string   `toml:"env"`
	Host           string   `toml:"host"`
	Port           Int      `toml:"port"`
	BodyLimitMB    int      `toml:"body_limit_mb"`
	ReadTimeout    Duration `toml:"read_timeout"`
	WriteTimeout   Duration `toml:"write_timeout"`
	IdleTimeout    Duration `toml:"idle_timeout"`
	CORSOrigins    []string `toml:"cors_origins"`
	ProxyHeader    string   `toml:"proxy_header"`
	TrustedProxies []string `toml:"trusted_proxies"`
}

func (a App) Addr() string { return net.JoinHostPort(a.Host, strconv.Itoa(int(a.Port))) }

func (a App) IsProduction() bool { return strings.EqualFold(a.Env, "production") }

type JWT struct {
	Issuer   string       `toml:"issuer"`
	Audience string       `toml:"audience"`
	Access   AccessToken  `toml:"access"`
	Refresh  RefreshToken `toml:"refresh"`
}

type AccessToken struct {
	Secret string   `toml:"secret"`
	TTL    Duration `toml:"ttl"`
}

type RefreshToken struct {
	Secret    string   `toml:"secret"`
	WebTTL    Duration `toml:"web_ttl"`
	MobileTTL Duration `toml:"mobile_ttl"`
	// MaxSessionAge is an absolute limit: after it a session ends even if
	// it was refreshed all along. 0 disables it.
	MaxSessionAge Duration `toml:"max_session_age"`
}

// Cookie configures how web clients receive the refresh token: in an
// HttpOnly cookie that JavaScript cannot read.
type Cookie struct {
	Enabled  bool   `toml:"enabled"`
	Name     string `toml:"name"`
	Path     string `toml:"path"`
	Domain   string `toml:"domain"`
	Secure   bool   `toml:"secure"`
	SameSite string `toml:"same_site"`
}

type Database struct {
	Driver   string `toml:"driver"`
	DSN      string `toml:"dsn"`
	Host     string `toml:"host"`
	Port     Int    `toml:"port"`
	User     string `toml:"user"`
	Password string `toml:"password"`
	Name     string `toml:"name"`
	// Params holds extra DSN parameters per driver name, e.g.
	// [database.params.postgres] sslmode = "require". Only the active
	// driver's table is used.
	Params          map[string]map[string]string `toml:"params"`
	MaxOpenConns    int                          `toml:"max_open_conns"`
	MaxIdleConns    int                          `toml:"max_idle_conns"`
	ConnMaxLifetime Duration                     `toml:"conn_max_lifetime"`
	ConnMaxIdleTime Duration                     `toml:"conn_max_idle_time"`
	AutoMigrate     bool                         `toml:"auto_migrate"`
}

type Security struct {
	BcryptCost             int      `toml:"bcrypt_cost"`
	PasswordMinLength      int      `toml:"password_min_length"`
	LoginRateLimit         int      `toml:"login_rate_limit"`
	LoginRateWindow        Duration `toml:"login_rate_window"`
	MaxFailedLogins        int      `toml:"max_failed_logins"`
	LockoutDuration        Duration `toml:"lockout_duration"`
	SessionCleanupInterval Duration `toml:"session_cleanup_interval"`
}

// Account holds the defaults for new users' self-service permissions. Each
// user can be changed individually afterwards.
type Account struct {
	DefaultCanChangeUsername bool `toml:"default_can_change_username"`
	DefaultCanChangeEmail    bool `toml:"default_can_change_email"`
	DefaultCanChangePassword bool `toml:"default_can_change_password"`
}

type RBAC struct {
	File string `toml:"file"`
}

type Seed struct {
	UsersFile string `toml:"users_file"`
}

func defaults() *Config {
	return &Config{
		App: App{
			Name:         "general-auth",
			Env:          "development",
			Host:         "0.0.0.0",
			Port:         3000,
			BodyLimitMB:  10,
			ReadTimeout:  Duration{30 * time.Second},
			WriteTimeout: Duration{90 * time.Second},
			IdleTimeout:  Duration{120 * time.Second},
		},
		JWT: JWT{
			Issuer:   "general-auth",
			Audience: "general-auth-api",
			Access:   AccessToken{TTL: Duration{15 * time.Minute}},
			Refresh: RefreshToken{
				WebTTL:        Duration{7 * 24 * time.Hour},
				MobileTTL:     Duration{90 * 24 * time.Hour},
				MaxSessionAge: Duration{180 * 24 * time.Hour},
			},
		},
		Cookie: Cookie{
			Enabled:  true,
			Name:     "ga_refresh",
			Path:     APIPrefix + "/auth",
			Secure:   true,
			SameSite: "Strict",
		},
		Database: Database{
			Driver:          "postgres",
			MaxOpenConns:    25,
			MaxIdleConns:    25,
			ConnMaxLifetime: Duration{30 * time.Minute},
			ConnMaxIdleTime: Duration{5 * time.Minute},
			AutoMigrate:     true,
		},
		Security: Security{
			BcryptCost:             12,
			PasswordMinLength:      8,
			LoginRateLimit:         10,
			LoginRateWindow:        Duration{time.Minute},
			MaxFailedLogins:        5,
			LockoutDuration:        Duration{15 * time.Minute},
			SessionCleanupInterval: Duration{time.Hour},
		},
		Account: Account{
			DefaultCanChangeUsername: false,
			DefaultCanChangeEmail:    true,
			DefaultCanChangePassword: true,
		},
		RBAC: RBAC{File: "rbac.toml"},
		Seed: Seed{UsersFile: "init/users.json"},
	}
}

// Load reads the TOML config at path. A .env file next to it (if present) is
// loaded first so ${VAR} references can be resolved; real environment
// variables always win over .env values.
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(abs)
	if err := godotenv.Load(filepath.Join(dir, ".env")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load .env: %w", err)
	}

	raw, err := os.ReadFile(abs)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("config file %s not found (copy example.config.toml to config.toml)", path)
	}
	if err != nil {
		return nil, err
	}

	cfg := defaults()
	md, err := toml.Decode(string(ExpandEnv(raw)), cfg)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		return nil, fmt.Errorf("parse %s: unknown keys %v", path, keys)
	}
	cfg.dir = dir
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s:\n%w", path, err)
	}
	for _, w := range cfg.warnings() {
		slog.Warn(w)
	}
	return cfg, nil
}

// Resolve turns a path from the config file into an absolute path, relative
// to the directory that holds the config file.
func (c *Config) Resolve(p string) string {
	if p == "" || filepath.IsAbs(p) || c.dir == "" {
		return p
	}
	return filepath.Join(c.dir, p)
}

// RefreshTTL returns the refresh token lifetime for a client type.
func (c *Config) RefreshTTL(client string) time.Duration {
	if client == "mobile" {
		return c.JWT.Refresh.MobileTTL.Duration
	}
	return c.JWT.Refresh.WebTTL.Duration
}

func (c *Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	prod := c.App.IsProduction()

	if c.App.Port < 1 || c.App.Port > 65535 {
		add("app.port must be between 1 and 65535")
	}
	if prod && slices.Contains(c.App.CORSOrigins, "*") {
		add(`app.cors_origins must list exact origins in production, not "*"`)
	}

	for name, secret := range map[string]string{"access": c.JWT.Access.Secret, "refresh": c.JWT.Refresh.Secret} {
		if len(secret) < 32 {
			add("jwt.%s.secret must be at least 32 characters", name)
		}
		if prod && strings.Contains(strings.ToLower(secret), "change-me") {
			add("jwt.%s.secret still uses the example value", name)
		}
	}
	if c.JWT.Access.Secret == c.JWT.Refresh.Secret {
		add("jwt.access.secret and jwt.refresh.secret must be different")
	}
	if c.JWT.Issuer == "" || c.JWT.Audience == "" {
		add("jwt.issuer and jwt.audience are required")
	}
	access := c.JWT.Access.TTL.Duration
	if access <= 0 {
		add("jwt.access.ttl must be positive")
	}
	if c.JWT.Refresh.WebTTL.Duration <= access || c.JWT.Refresh.MobileTTL.Duration <= access {
		add("jwt.refresh.web_ttl and jwt.refresh.mobile_ttl must be longer than jwt.access.ttl")
	}
	if m := c.JWT.Refresh.MaxSessionAge.Duration; m < 0 || (m > 0 && m <= access) {
		add("jwt.refresh.max_session_age must be 0 (off) or longer than jwt.access.ttl")
	}

	if c.Cookie.Enabled {
		switch c.Cookie.SameSite {
		case "Strict", "Lax":
		case "None":
			if !c.Cookie.Secure {
				add(`cookie.same_site = "None" requires cookie.secure = true`)
			}
		default:
			add(`cookie.same_site must be "Strict", "Lax" or "None"`)
		}
		if c.Cookie.Name == "" || !strings.HasPrefix(c.Cookie.Path, "/") {
			add("cookie.name is required and cookie.path must start with /")
		}
		if prod && !c.Cookie.Secure {
			add("cookie.secure must be true in production")
		}
	}

	if c.Database.Driver == "" {
		add("database.driver is required")
	}
	if c.Database.MaxOpenConns < 1 || c.Database.MaxIdleConns < 0 {
		add("database.max_open_conns must be >= 1 and max_idle_conns >= 0")
	}

	s := c.Security
	if s.BcryptCost < 10 || s.BcryptCost > 16 {
		add("security.bcrypt_cost must be between 10 and 16")
	}
	if s.PasswordMinLength < 8 || s.PasswordMinLength > 72 {
		add("security.password_min_length must be between 8 and 72")
	}
	if s.LoginRateLimit < 1 || s.LoginRateWindow.Duration <= 0 {
		add("security.login_rate_limit and login_rate_window must be positive")
	}
	if s.MaxFailedLogins < 0 || (s.MaxFailedLogins > 0 && s.LockoutDuration.Duration <= 0) {
		add("security.max_failed_logins must be >= 0 and lockout_duration positive when it is set")
	}
	return errors.Join(errs...)
}

func (c *Config) warnings() []string {
	var w []string
	if c.App.IsProduction() && c.Database.AutoMigrate {
		w = append(w, "database.auto_migrate is on in production; prefer running `general-auth migrate` as a deploy step")
	}
	if c.Cookie.Enabled && slices.Contains(c.App.CORSOrigins, "*") {
		w = append(w, `app.cors_origins is "*": browsers on other origins cannot send the refresh cookie; list exact origins if the web app is on another domain`)
	}
	for name, secret := range map[string]string{"access": c.JWT.Access.Secret, "refresh": c.JWT.Refresh.Secret} {
		if strings.Contains(strings.ToLower(secret), "change-me") {
			w = append(w, "jwt."+name+".secret is the example value from example.env; generate one with `openssl rand -hex 32`")
		}
	}
	if c.Security.MaxFailedLogins == 0 {
		w = append(w, "security.max_failed_logins = 0: account lockout is disabled")
	}
	return w
}
