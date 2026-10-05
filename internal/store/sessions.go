package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"general-auth/internal/database"
)

// Session is one login on one device. It lives as long as its refresh token
// keeps being rotated, up to the configured absolute maximum age.
type Session struct {
	ID         string     `db:"id" json:"id"`
	UserID     string     `db:"user_id" json:"user_id"`
	Client     string     `db:"client" json:"client"`
	RefreshJTI *string    `db:"refresh_jti" json:"-"`
	UserAgent  string     `db:"user_agent" json:"user_agent"`
	IP         string     `db:"ip" json:"ip"`
	CreatedAt  time.Time  `db:"created_at" json:"created_at"`
	LastUsedAt time.Time  `db:"last_used_at" json:"last_used_at"`
	ExpiresAt  time.Time  `db:"expires_at" json:"expires_at"`
	RevokedAt  *time.Time `db:"revoked_at" json:"revoked_at,omitempty"`
}

const sessionColumns = "id, user_id, client, refresh_jti, user_agent, ip, created_at, last_used_at, expires_at, revoked_at"

type Sessions struct{ db *database.DB }

func NewSessions(db *database.DB) *Sessions { return &Sessions{db: db} }

func (s *Sessions) Create(ctx context.Context, x *Session) error {
	q := s.db.Rebind(`INSERT INTO sessions (` + sessionColumns + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`)
	_, err := s.db.ExecContext(ctx, q, x.ID, x.UserID, x.Client, nullable(x.RefreshJTI), x.UserAgent, x.IP,
		x.CreatedAt, x.LastUsedAt, x.ExpiresAt)
	return err
}

// GetWithUser loads a session and its owner in one round trip; it runs on
// every authenticated request.
func (s *Sessions) GetWithUser(ctx context.Context, id string) (*Session, *User, error) {
	q := s.db.Rebind(`SELECT s.id, s.user_id, s.client, s.refresh_jti, s.user_agent, s.ip,
		s.created_at, s.last_used_at, s.expires_at, s.revoked_at, ` + userColumnsPrefixed + `
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id = ?`)
	var x Session
	var u User
	targets := append([]any{&x.ID, &x.UserID, &x.Client, &x.RefreshJTI, &x.UserAgent, &x.IP,
		&x.CreatedAt, &x.LastUsedAt, &x.ExpiresAt, &x.RevokedAt}, u.scanTargets()...)
	err := s.db.QueryRowContext(ctx, q, id).Scan(targets...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	return &x, &u, nil
}

// ListActive returns a user's sessions that are neither revoked nor expired.
func (s *Sessions) ListActive(ctx context.Context, userID string, now time.Time) ([]Session, error) {
	out := []Session{}
	q := s.db.Rebind(`SELECT ` + sessionColumns + ` FROM sessions
		WHERE user_id = ? AND revoked_at IS NULL AND expires_at > ? ORDER BY last_used_at DESC`)
	err := s.db.SelectContext(ctx, &out, q, userID, now)
	return out, err
}

// Rotate swaps the refresh token id only if oldJTI is still the current one,
// so two concurrent refreshes with the same token cannot both succeed.
func (s *Sessions) Rotate(ctx context.Context, id, oldJTI, newJTI string, expiresAt, now time.Time) (bool, error) {
	q := s.db.Rebind(`UPDATE sessions SET refresh_jti = ?, expires_at = ?, last_used_at = ?
		WHERE id = ? AND refresh_jti = ? AND revoked_at IS NULL`)
	res, err := s.db.ExecContext(ctx, q, newJTI, expiresAt, now, id, oldJTI)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Sessions) Revoke(ctx context.Context, id string, at time.Time) error {
	q := s.db.Rebind(`UPDATE sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`)
	_, err := s.db.ExecContext(ctx, q, at, id)
	return err
}

// RevokeOwned revokes a session only if it belongs to userID.
func (s *Sessions) RevokeOwned(ctx context.Context, id, userID string, at time.Time) (bool, error) {
	q := s.db.Rebind(`UPDATE sessions SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL`)
	res, err := s.db.ExecContext(ctx, q, at, id, userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// RevokeAllForUser revokes every active session of a user except keepID
// (pass "" to revoke all).
func (s *Sessions) RevokeAllForUser(ctx context.Context, userID, keepID string, at time.Time) (int64, error) {
	q := s.db.Rebind(`UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND id <> ? AND revoked_at IS NULL`)
	res, err := s.db.ExecContext(ctx, q, at, userID, keepID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteStale removes expired and revoked sessions.
func (s *Sessions) DeleteStale(ctx context.Context, now time.Time) (int64, error) {
	q := s.db.Rebind(`DELETE FROM sessions WHERE expires_at < ? OR revoked_at IS NOT NULL`)
	res, err := s.db.ExecContext(ctx, q, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
