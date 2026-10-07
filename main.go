package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"general-auth/internal/auth"
	"general-auth/internal/config"
	"general-auth/internal/d365"
	"general-auth/internal/database"
	"general-auth/internal/rbac"
	"general-auth/internal/seed"
	"general-auth/internal/server"
	"general-auth/internal/store"
)

const usage = `general-auth: auth gateway between web/mobile apps and D365 BC / F&O

Usage:
  general-auth [command] [-config config.toml]

Commands:
  serve                 start the API server (default)
  migrate               apply database migrations
  seed [-update]        create users from init/users.json ([seed] users_file);
                        -update overwrites users that already exist
  hash-password <pass>  print a bcrypt hash for "password_hash" in users.json
  check                 validate config, rbac and connector files, then exit
`

func main() {
	if err := run(); err != nil {
		var missing *seed.MissingFileError
		if errors.As(err, &missing) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func run() error {
	cmd, args := "serve", os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	configPath := fs.String("config", envOr("CONFIG_PATH", "config.toml"), "path to the config file")
	update := fs.Bool("update", false, "seed: overwrite users that already exist")
	fs.Parse(args)

	switch cmd {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "hash-password":
		if fs.NArg() != 1 {
			return errors.New("usage: general-auth hash-password <password>")
		}
		h, err := bcrypt.GenerateFromPassword([]byte(fs.Arg(0)), bcrypt.DefaultCost+2)
		if err != nil {
			return err
		}
		fmt.Println(string(h))
		return nil
	}

	// The default config.toml is optional (built-in defaults + .env); a file
	// named explicitly with -config or CONFIG_PATH must exist.
	explicit := os.Getenv("CONFIG_PATH") != ""
	fs.Visit(func(f *flag.Flag) { explicit = explicit || f.Name == "config" })
	cfg, err := config.Load(*configPath, !explicit)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "serve":
		return serve(ctx, cfg)
	case "migrate":
		db, err := openDB(ctx, cfg, true)
		if err != nil {
			return err
		}
		defer db.Close()
		fmt.Printf("migrations applied (%s)\n", db.Dialect.Name)
		return nil
	case "seed":
		return runSeed(ctx, cfg, *update)
	case "check":
		if _, err := loadPolicy(cfg); err != nil {
			return err
		}
		conns, err := d365.LoadAll(cfg)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(conns))
		for _, c := range conns {
			names = append(names, c.Name+"("+c.Instance+")")
		}
		slices.Sort(names)
		fmt.Printf("config OK: env=%s db=%s connectors=[%s]\n", cfg.App.Env, cfg.Database.Driver, strings.Join(names, " "))
		return nil
	}
	fmt.Fprint(os.Stderr, usage)
	return fmt.Errorf("unknown command %q", cmd)
}

func openDB(ctx context.Context, cfg *config.Config, migrate bool) (*database.DB, error) {
	db, err := database.Open(ctx, cfg.Database, cfg.Resolve)
	if err != nil {
		return nil, err
	}
	if migrate {
		if err := db.Migrate(ctx); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

func loadPolicy(cfg *config.Config) (*rbac.Policy, error) {
	path := cfg.Resolve(cfg.RBAC.File)
	policy, found, err := rbac.Load(path)
	if err != nil {
		return nil, err
	}
	if !found {
		if cfg.App.IsProduction() {
			return nil, fmt.Errorf("rbac file %s not found (copy example.rbac.toml)", path)
		}
		slog.Warn("rbac file not found, using all-access defaults (copy example.rbac.toml)", "path", path)
	}
	return policy, nil
}

func newAuthService(cfg *config.Config, db *database.DB, policy *rbac.Policy) (*auth.Service, error) {
	return auth.NewService(cfg, store.NewUsers(db), store.NewSessions(db), policy)
}

func runSeed(ctx context.Context, cfg *config.Config, update bool) error {
	path := cfg.Resolve(cfg.Seed.UsersFile)
	if err := seed.Check(path); err != nil {
		return err
	}
	policy, err := loadPolicy(cfg)
	if err != nil {
		return err
	}
	db, err := openDB(ctx, cfg, true)
	if err != nil {
		return err
	}
	defer db.Close()
	svc, err := newAuthService(cfg, db, policy)
	if err != nil {
		return err
	}
	fmt.Printf("seeding users from %s\n", path)
	return seed.Run(ctx, svc, path, update, os.Stdout)
}

func serve(ctx context.Context, cfg *config.Config) error {
	policy, err := loadPolicy(cfg)
	if err != nil {
		return err
	}
	connectors, err := d365.LoadAll(cfg)
	if err != nil {
		return err
	}
	db, err := openDB(ctx, cfg, cfg.Database.AutoMigrate)
	if err != nil {
		return err
	}
	defer db.Close()
	svc, err := newAuthService(cfg, db, policy)
	if err != nil {
		return err
	}

	go cleanupSessions(ctx, svc, cfg.Security.SessionCleanupInterval.Duration)

	srv := server.New(cfg, svc, policy, connectors)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Listen() }()
	slog.Info("server started", "addr", cfg.App.Addr(), "env", cfg.App.Env,
		"db", db.Dialect.Name, "connectors", len(connectors))

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func cleanupSessions(ctx context.Context, svc *auth.Service, every time.Duration) {
	if every <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := svc.CleanupSessions(ctx); err != nil {
				slog.Warn("session cleanup failed", "err", err)
			} else if n > 0 {
				slog.Info("removed stale sessions", "count", n)
			}
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
