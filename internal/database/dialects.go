package database

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "modernc.org/sqlite"

	"general-auth/internal/config"
)

func init() {
	sqlx.BindDriver("sqlite", sqlx.QUESTION)
}

// Dialect holds everything that differs between databases. migrations[i]
// is schema version i+1; to change the schema, append a new version to
// every dialect. Never edit a version that has been released.
type Dialect struct {
	Name            string
	Driver          string
	buildDSN        func(cfg config.Database, params map[string]string, resolve func(string) string) (string, error)
	paginate        func(limit, offset int) string
	migrationsTable string
	migrations      [][]string
}

var aliases = map[string]string{
	"postgres": "postgres", "postgresql": "postgres", "pg": "postgres",
	"mysql": "mysql", "mariadb": "mysql",
	"sqlite": "sqlite", "sqlite3": "sqlite",
	"sqlserver": "sqlserver", "mssql": "sqlserver",
}

// Lookup returns the dialect for a configured driver name.
func Lookup(driver string) (*Dialect, error) {
	name, ok := aliases[strings.ToLower(strings.TrimSpace(driver))]
	if !ok {
		return nil, fmt.Errorf("unsupported database.driver %q (use postgres, mysql, sqlite or sqlserver)", driver)
	}
	return dialects[name], nil
}

func hostPort(host string, port, def int) string {
	if host == "" {
		host = "localhost"
	}
	if port == 0 {
		port = def
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func limitOffset(limit, offset int) string { return fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset) }

// usersTable renders the users table with per-dialect column types.
func usersTable(str, boolT, ts, suffix string, uniqueInline bool) string {
	uq := ""
	if uniqueInline {
		uq = " UNIQUE"
	}
	return `CREATE TABLE users (
		id ` + str + `(36) NOT NULL PRIMARY KEY,
		username ` + str + `(100) NOT NULL UNIQUE,
		email ` + str + `(255) NULL` + uq + `,
		code ` + str + `(100) NULL` + uq + `,
		name ` + str + `(255) NOT NULL DEFAULT '',
		password_hash ` + str + `(255) NOT NULL,
		role ` + str + `(50) NOT NULL,
		is_active ` + boolT + ` NOT NULL,
		can_change_username ` + boolT + ` NOT NULL,
		can_change_email ` + boolT + ` NOT NULL,
		can_change_password ` + boolT + ` NOT NULL,
		failed_login_count INTEGER NOT NULL DEFAULT 0,
		locked_until ` + ts + ` NULL,
		password_changed_at ` + ts + ` NULL,
		last_login_at ` + ts + ` NULL,
		created_at ` + ts + ` NOT NULL,
		updated_at ` + ts + ` NOT NULL
	)` + suffix
}

func sessionsTable(str, ts, suffix string) string {
	return `CREATE TABLE sessions (
		id ` + str + `(36) NOT NULL PRIMARY KEY,
		user_id ` + str + `(36) NOT NULL,
		client ` + str + `(10) NOT NULL,
		refresh_jti ` + str + `(64) NULL,
		user_agent ` + str + `(255) NOT NULL DEFAULT '',
		ip ` + str + `(64) NOT NULL DEFAULT '',
		created_at ` + ts + ` NOT NULL,
		last_used_at ` + ts + ` NOT NULL,
		expires_at ` + ts + ` NOT NULL,
		revoked_at ` + ts + ` NULL,
		CONSTRAINT fk_sessions_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	)` + suffix
}

var dialects = map[string]*Dialect{
	"postgres": {
		Name:   "postgres",
		Driver: "pgx",
		buildDSN: func(c config.Database, params map[string]string, _ func(string) string) (string, error) {
			u := url.URL{
				Scheme: "postgres",
				User:   url.UserPassword(c.User, c.Password),
				Host:   hostPort(c.Host, int(c.Port), 5432),
				Path:   "/" + c.Name,
			}
			q := url.Values{}
			for k, v := range params {
				q.Set(k, v)
			}
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		paginate:        limitOffset,
		migrationsTable: `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL)`,
		migrations: [][]string{
			{ // 1: users + sessions
				usersTable("VARCHAR", "BOOLEAN", "TIMESTAMPTZ", "", true),
				sessionsTable("VARCHAR", "TIMESTAMPTZ", ""),
				`CREATE INDEX idx_sessions_user_id ON sessions(user_id)`,
				`CREATE INDEX idx_sessions_expires_at ON sessions(expires_at)`,
			},
		},
	},

	"mysql": {
		Name:   "mysql",
		Driver: "mysql",
		buildDSN: func(c config.Database, params map[string]string, _ func(string) string) (string, error) {
			mc := mysql.NewConfig()
			mc.User = c.User
			mc.Passwd = c.Password
			mc.Net = "tcp"
			mc.Addr = hostPort(c.Host, int(c.Port), 3306)
			mc.DBName = c.Name
			mc.ParseTime = true
			mc.Loc = time.UTC
			if len(params) > 0 {
				mc.Params = params
			}
			return mc.FormatDSN(), nil
		},
		paginate:        limitOffset,
		migrationsTable: `CREATE TABLE IF NOT EXISTS schema_migrations (version INT NOT NULL PRIMARY KEY, applied_at DATETIME(6) NOT NULL) ENGINE=InnoDB`,
		migrations: [][]string{
			{
				usersTable("VARCHAR", "TINYINT(1)", "DATETIME(6)", " ENGINE=InnoDB DEFAULT CHARSET=utf8mb4", true),
				sessionsTable("VARCHAR", "DATETIME(6)", " ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"),
				`CREATE INDEX idx_sessions_expires_at ON sessions(expires_at)`,
			},
		},
	},

	"sqlite": {
		Name:   "sqlite",
		Driver: "sqlite",
		buildDSN: func(c config.Database, params map[string]string, resolve func(string) string) (string, error) {
			path := c.Name
			if path == "" {
				return "", fmt.Errorf("database.name must be the sqlite file path (e.g. data/general-auth.db)")
			}
			if path != ":memory:" {
				path = filepath.ToSlash(resolve(path))
			}
			q := url.Values{}
			q.Add("_pragma", "foreign_keys(1)")
			q.Add("_pragma", "busy_timeout(5000)")
			q.Add("_pragma", "journal_mode(WAL)")
			for k, v := range params {
				q.Add(k, v)
			}
			return path + "?" + q.Encode(), nil
		},
		paginate:        limitOffset,
		migrationsTable: `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at DATETIME NOT NULL)`,
		migrations: [][]string{
			{
				usersTable("VARCHAR", "INTEGER", "DATETIME", "", true),
				sessionsTable("VARCHAR", "DATETIME", ""),
				`CREATE INDEX idx_sessions_user_id ON sessions(user_id)`,
				`CREATE INDEX idx_sessions_expires_at ON sessions(expires_at)`,
			},
		},
	},

	"sqlserver": {
		Name:   "sqlserver",
		Driver: "sqlserver",
		buildDSN: func(c config.Database, params map[string]string, _ func(string) string) (string, error) {
			q := url.Values{}
			q.Set("database", c.Name)
			for k, v := range params {
				q.Set(k, v)
			}
			u := url.URL{
				Scheme:   "sqlserver",
				User:     url.UserPassword(c.User, c.Password),
				Host:     hostPort(c.Host, int(c.Port), 1433),
				RawQuery: q.Encode(),
			}
			return u.String(), nil
		},
		paginate: func(limit, offset int) string {
			return fmt.Sprintf(" OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", offset, limit)
		},
		migrationsTable: `IF OBJECT_ID(N'schema_migrations', N'U') IS NULL
			CREATE TABLE schema_migrations (version INT NOT NULL PRIMARY KEY, applied_at DATETIME2 NOT NULL)`,
		migrations: [][]string{
			{
				// SQL Server unique constraints allow a single NULL, so
				// email/code use filtered unique indexes instead.
				usersTable("NVARCHAR", "BIT", "DATETIME2", "", false),
				`CREATE UNIQUE INDEX ux_users_email ON users(email) WHERE email IS NOT NULL`,
				`CREATE UNIQUE INDEX ux_users_code ON users(code) WHERE code IS NOT NULL`,
				sessionsTable("NVARCHAR", "DATETIME2", ""),
				`CREATE INDEX idx_sessions_user_id ON sessions(user_id)`,
				`CREATE INDEX idx_sessions_expires_at ON sessions(expires_at)`,
			},
		},
	},
}
