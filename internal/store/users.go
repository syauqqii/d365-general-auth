package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"general-auth/internal/database"
)

var ErrNotFound = errors.New("not found")

// ConflictError reports which unique field is already taken.
type ConflictError struct{ Field string }

func (e *ConflictError) Error() string { return e.Field + " is already in use" }

type User struct {
	ID                string     `db:"id" json:"id"`
	Username          string     `db:"username" json:"username"`
	Email             *string    `db:"email" json:"email"`
	Code              *string    `db:"code" json:"code"`
	Name              string     `db:"name" json:"name"`
	PasswordHash      string     `db:"password_hash" json:"-"`
	Role              string     `db:"role" json:"role"`
	IsActive          bool       `db:"is_active" json:"is_active"`
	CanChangeUsername bool       `db:"can_change_username" json:"can_change_username"`
	CanChangeEmail    bool       `db:"can_change_email" json:"can_change_email"`
	CanChangePassword bool       `db:"can_change_password" json:"can_change_password"`
	FailedLoginCount  int        `db:"failed_login_count" json:"failed_login_count"`
	LockedUntil       *time.Time `db:"locked_until" json:"locked_until"`
	PasswordChangedAt *time.Time `db:"password_changed_at" json:"password_changed_at"`
	LastLoginAt       *time.Time `db:"last_login_at" json:"last_login_at"`
	CreatedAt         time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time  `db:"updated_at" json:"updated_at"`
}

// IsLocked reports whether the account is temporarily locked after too
// many failed logins.
func (u *User) IsLocked(now time.Time) bool {
	return u.LockedUntil != nil && now.Before(*u.LockedUntil)
}

const userColumns = `id, username, email, code, name, password_hash, role, is_active,
	can_change_username, can_change_email, can_change_password,
	failed_login_count, locked_until, password_changed_at, last_login_at, created_at, updated_at`

// userColumnsPrefixed is userColumns qualified with the "u." alias.
var userColumnsPrefixed = "u." + strings.Join(strings.Fields(strings.ReplaceAll(userColumns, ",", " ")), ", u.")

func (u *User) scanTargets() []any {
	return []any{&u.ID, &u.Username, &u.Email, &u.Code, &u.Name, &u.PasswordHash, &u.Role, &u.IsActive,
		&u.CanChangeUsername, &u.CanChangeEmail, &u.CanChangePassword,
		&u.FailedLoginCount, &u.LockedUntil, &u.PasswordChangedAt, &u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt}
}

type Users struct{ db *database.DB }

func NewUsers(db *database.DB) *Users { return &Users{db: db} }

func (s *Users) one(ctx context.Context, where string, arg any) (*User, error) {
	var u User
	err := s.db.GetContext(ctx, &u, s.db.Rebind("SELECT "+userColumns+" FROM users WHERE "+where), arg)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Users) ByID(ctx context.Context, id string) (*User, error) {
	return s.one(ctx, "id = ?", id)
}

func (s *Users) ByUsername(ctx context.Context, v string) (*User, error) {
	return s.one(ctx, "username = ?", strings.ToLower(strings.TrimSpace(v)))
}

func (s *Users) ByEmail(ctx context.Context, v string) (*User, error) {
	return s.one(ctx, "email = ?", strings.ToLower(strings.TrimSpace(v)))
}

func (s *Users) ByCode(ctx context.Context, v string) (*User, error) {
	return s.one(ctx, "code = ?", strings.TrimSpace(v))
}

func (s *Users) checkConflict(ctx context.Context, u *User) error {
	var rows []struct {
		Username string  `db:"username"`
		Email    *string `db:"email"`
		Code     *string `db:"code"`
	}
	q := s.db.Rebind(`SELECT username, email, code FROM users
		WHERE (username = ? OR email = ? OR code = ?) AND id <> ?`)
	if err := s.db.SelectContext(ctx, &rows, q, u.Username, nullable(u.Email), nullable(u.Code), u.ID); err != nil {
		return err
	}
	for _, r := range rows {
		switch {
		case r.Username == u.Username:
			return &ConflictError{Field: "username"}
		case u.Email != nil && r.Email != nil && *r.Email == *u.Email:
			return &ConflictError{Field: "email"}
		case u.Code != nil && r.Code != nil && *r.Code == *u.Code:
			return &ConflictError{Field: "code"}
		}
	}
	return nil
}

func (s *Users) Create(ctx context.Context, u *User) error {
	if err := s.checkConflict(ctx, u); err != nil {
		return err
	}
	q := s.db.Rebind(`INSERT INTO users (` + userColumns + `)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err := s.db.ExecContext(ctx, q, u.ID, u.Username, nullable(u.Email), nullable(u.Code), u.Name,
		u.PasswordHash, u.Role, u.IsActive, u.CanChangeUsername, u.CanChangeEmail, u.CanChangePassword,
		u.FailedLoginCount, nullableTime(u.LockedUntil), nullableTime(u.PasswordChangedAt),
		nullableTime(u.LastLoginAt), u.CreatedAt, u.UpdatedAt)
	return err
}

// Update writes the editable fields. Login counters are changed only by
// the dedicated methods below.
func (s *Users) Update(ctx context.Context, u *User) error {
	if err := s.checkConflict(ctx, u); err != nil {
		return err
	}
	q := s.db.Rebind(`UPDATE users SET username = ?, email = ?, code = ?, name = ?, password_hash = ?,
		role = ?, is_active = ?, can_change_username = ?, can_change_email = ?, can_change_password = ?,
		password_changed_at = ?, updated_at = ? WHERE id = ?`)
	res, err := s.db.ExecContext(ctx, q, u.Username, nullable(u.Email), nullable(u.Code), u.Name,
		u.PasswordHash, u.Role, u.IsActive, u.CanChangeUsername, u.CanChangeEmail, u.CanChangePassword,
		nullableTime(u.PasswordChangedAt), u.UpdatedAt, u.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordFailedLogin increments the failure counter atomically and locks the
// account once it reaches maxFailed (0 = never lock). locked_until is
// assigned first because MySQL evaluates SET clauses left to right.
func (s *Users) RecordFailedLogin(ctx context.Context, id string, maxFailed int, lockUntil time.Time) error {
	if maxFailed <= 0 {
		return nil
	}
	q := s.db.Rebind(`UPDATE users SET
		locked_until = CASE WHEN failed_login_count + 1 >= ? THEN ? ELSE locked_until END,
		failed_login_count = CASE WHEN failed_login_count + 1 >= ? THEN 0 ELSE failed_login_count + 1 END
		WHERE id = ?`)
	_, err := s.db.ExecContext(ctx, q, maxFailed, lockUntil, maxFailed, id)
	return err
}

// RecordLogin clears the lockout state and stamps last_login_at.
func (s *Users) RecordLogin(ctx context.Context, id string, at time.Time) error {
	q := s.db.Rebind(`UPDATE users SET failed_login_count = 0, locked_until = NULL, last_login_at = ? WHERE id = ?`)
	_, err := s.db.ExecContext(ctx, q, at, id)
	return err
}

func (s *Users) Unlock(ctx context.Context, id string) error {
	q := s.db.Rebind(`UPDATE users SET failed_login_count = 0, locked_until = NULL WHERE id = ?`)
	_, err := s.db.ExecContext(ctx, q, id)
	return err
}

type UserFilter struct {
	Query  string // matches username, email, code or name
	Role   string
	Active *bool
	Limit  int
	Offset int
}

func (s *Users) List(ctx context.Context, f UserFilter) ([]User, int, error) {
	var where []string
	var args []any
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + strings.NewReplacer(`\`, "", "%", "", "_", "").Replace(strings.ToLower(q)) + "%"
		where = append(where, "(LOWER(username) LIKE ? OR LOWER(email) LIKE ? OR LOWER(code) LIKE ? OR LOWER(name) LIKE ?)")
		args = append(args, like, like, like, like)
	}
	if f.Role != "" {
		where = append(where, "role = ?")
		args = append(args, f.Role)
	}
	if f.Active != nil {
		where = append(where, "is_active = ?")
		args = append(args, *f.Active)
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := s.db.GetContext(ctx, &total, s.db.Rebind("SELECT COUNT(*) FROM users"+cond), args...); err != nil {
		return nil, 0, err
	}
	users := []User{}
	q := s.db.Rebind("SELECT " + userColumns + " FROM users" + cond + " ORDER BY created_at, id" + s.db.Paginate(f.Limit, f.Offset))
	if err := s.db.SelectContext(ctx, &users, q, args...); err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}
	return users, total, nil
}

// nullable turns a nil pointer into a SQL NULL; some drivers do not
// dereference pointer arguments on their own.
func nullable(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullableTime(p *time.Time) any {
	if p == nil {
		return nil
	}
	return *p
}
