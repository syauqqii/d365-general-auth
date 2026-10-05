package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"general-auth/internal/config"
	"general-auth/internal/database"
	"general-auth/internal/rbac"
	"general-auth/internal/store"
)

// Client is the app type declared at login. Web and mobile use the same
// endpoints and token format; the client only decides the refresh token
// lifetime, how the server hands it out (cookie vs body) and which roles
// may log in with it (rbac [clients]).
type Client string

const (
	Web    Client = "web"
	Mobile Client = "mobile"
)

func ParseClient(s string) (Client, error) {
	switch c := Client(strings.ToLower(strings.TrimSpace(s))); c {
	case "":
		return Web, nil
	case Web, Mobile:
		return c, nil
	}
	return "", &ValidationError{`client must be "web" or "mobile"`}
}

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUserInactive       = errors.New("user is inactive")
	ErrInvalidToken       = errors.New("invalid or expired token")
	ErrSessionEnded       = errors.New("session expired or revoked, please log in again")
	ErrRefreshReused      = errors.New("refresh token was already used; session revoked")
	ErrInvalidLoginType   = errors.New("login_type must be auto, username, email or code")
)

// ValidationError is a client input error (400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// ForbiddenError is an authenticated action that is not permitted (403).
type ForbiddenError struct{ Msg string }

func (e *ForbiddenError) Error() string { return e.Msg }

// LockedError means too many failed logins (429).
type LockedError struct{ Until time.Time }

func (e *LockedError) Error() string {
	return fmt.Sprintf("too many failed attempts, account locked until %s", e.Until.Format(time.RFC3339))
}

type Meta struct {
	UserAgent string
	IP        string
}

type Result struct {
	Client           Client      `json:"client"`
	TokenType        string      `json:"token_type"`
	AccessToken      string      `json:"access_token"`
	ExpiresIn        int64       `json:"expires_in"`
	ExpiresAt        time.Time   `json:"expires_at"`
	RefreshToken     string      `json:"refresh_token,omitempty"`
	RefreshExpiresAt time.Time   `json:"refresh_expires_at"`
	SessionID        string      `json:"session_id"`
	User             *store.User `json:"user"`
}

// Principal is the authenticated caller of a request.
type Principal struct {
	User    *store.User
	Session *store.Session
}

type Service struct {
	cfg       *config.Config
	users     *store.Users
	sessions  *store.Sessions
	tokens    *Tokens
	policy    *rbac.Policy
	dummyHash []byte
}

func NewService(cfg *config.Config, users *store.Users, sessions *store.Sessions, policy *rbac.Policy) (*Service, error) {
	// Compared against when the user does not exist, so a failed login takes
	// the same time whether or not the identifier is registered.
	dummy, err := bcrypt.GenerateFromPassword([]byte(uuid.NewString()), cfg.Security.BcryptCost)
	if err != nil {
		return nil, err
	}
	return &Service{cfg: cfg, users: users, sessions: sessions, tokens: NewTokens(cfg.JWT), policy: policy, dummyHash: dummy}, nil
}

func (s *Service) Login(ctx context.Context, client Client, identifier, loginType, password string, meta Meta) (*Result, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" || password == "" {
		return nil, &ValidationError{"identifier and password are required"}
	}
	u, err := s.findUser(ctx, identifier, loginType)
	if errors.Is(err, store.ErrNotFound) {
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(password))
		slog.Warn("login failed: unknown identifier", "identifier", identifier, "ip", meta.IP)
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}

	now := database.Now()
	if u.IsLocked(now) {
		return nil, &LockedError{Until: *u.LockedUntil}
	}
	if err := s.verifyPassword(ctx, u, password, now); err != nil {
		slog.Warn("login failed: wrong password", "user", u.ID, "ip", meta.IP)
		return nil, err
	}
	// Status checks come after the password so they reveal nothing to
	// someone who does not know it.
	if !u.IsActive {
		return nil, ErrUserInactive
	}
	if !s.policy.ClientAllowed(u.Role, string(client)) {
		return nil, &ForbiddenError{fmt.Sprintf("role %q may not log in from a %s client", u.Role, client)}
	}

	jti := uuid.NewString()
	sess := &store.Session{
		ID:         uuid.NewString(),
		UserID:     u.ID,
		Client:     string(client),
		RefreshJTI: &jti,
		UserAgent:  truncate(meta.UserAgent, 255),
		IP:         truncate(meta.IP, 64),
		CreatedAt:  now,
		LastUsedAt: now,
	}
	sess.ExpiresAt = s.refreshExpiry(sess, now)
	if err := s.sessions.Create(ctx, sess); err != nil {
		return nil, err
	}
	if err := s.users.RecordLogin(ctx, u.ID, now); err != nil {
		slog.Warn("record login failed", "user", u.ID, "err", err)
	}
	u.LastLoginAt, u.FailedLoginCount, u.LockedUntil = &now, 0, nil
	slog.Info("login", "user", u.ID, "client", client, "session", sess.ID, "ip", meta.IP)
	return s.issue(u, sess, now)
}

// verifyPassword checks a password and counts failures toward the lockout.
func (s *Service) verifyPassword(ctx context.Context, u *store.User, password string, now time.Time) error {
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil {
		return nil
	}
	sec := s.cfg.Security
	if err := s.users.RecordFailedLogin(ctx, u.ID, sec.MaxFailedLogins, now.Add(sec.LockoutDuration.Duration)); err != nil {
		slog.Warn("record failed login failed", "user", u.ID, "err", err)
	}
	return ErrInvalidCredentials
}

func (s *Service) findUser(ctx context.Context, id, loginType string) (*store.User, error) {
	switch strings.ToLower(strings.TrimSpace(loginType)) {
	case "username":
		return s.users.ByUsername(ctx, id)
	case "email":
		return s.users.ByEmail(ctx, id)
	case "code":
		return s.users.ByCode(ctx, id)
	case "", "auto":
		if strings.Contains(id, "@") {
			if u, err := s.users.ByEmail(ctx, id); !errors.Is(err, store.ErrNotFound) {
				return u, err
			}
		}
		if u, err := s.users.ByUsername(ctx, id); !errors.Is(err, store.ErrNotFound) {
			return u, err
		}
		return s.users.ByCode(ctx, id)
	}
	return nil, ErrInvalidLoginType
}

// refreshExpiry is now + the client's refresh TTL, capped by the session's
// absolute maximum age.
func (s *Service) refreshExpiry(sess *store.Session, now time.Time) time.Time {
	exp := now.Add(s.cfg.RefreshTTL(sess.Client))
	if max := s.cfg.JWT.Refresh.MaxSessionAge.Duration; max > 0 {
		if limit := sess.CreatedAt.Add(max); exp.After(limit) {
			exp = limit
		}
	}
	return exp
}

func (s *Service) issue(u *store.User, sess *store.Session, now time.Time) (*Result, error) {
	base := Claims{SessionID: sess.ID, Client: Client(sess.Client), Role: u.Role}
	base.Subject = u.ID

	accessExp := now.Add(s.cfg.JWT.Access.TTL.Duration)
	if accessExp.After(sess.ExpiresAt) {
		accessExp = sess.ExpiresAt
	}
	ac := base
	ac.ID = uuid.NewString()
	access, err := s.tokens.Issue(KindAccess, ac, now, accessExp)
	if err != nil {
		return nil, err
	}
	rc := base
	rc.ID = *sess.RefreshJTI
	refresh, err := s.tokens.Issue(KindRefresh, rc, now, sess.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return &Result{
		Client:           Client(sess.Client),
		TokenType:        "Bearer",
		AccessToken:      access,
		ExpiresIn:        int64(accessExp.Sub(now).Seconds()),
		ExpiresAt:        accessExp,
		RefreshToken:     refresh,
		RefreshExpiresAt: sess.ExpiresAt,
		SessionID:        sess.ID,
		User:             u,
	}, nil
}

// Refresh exchanges a refresh token for a new access + refresh pair. Each
// refresh token works once; presenting an old one revokes the session,
// because it means the token was copied.
func (s *Service) Refresh(ctx context.Context, raw string, meta Meta) (*Result, error) {
	claims, err := s.tokens.Parse(KindRefresh, strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	sess, u, err := s.sessions.GetWithUser(ctx, claims.SessionID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrSessionEnded
	}
	if err != nil {
		return nil, err
	}
	now := database.Now()
	if sess.UserID != claims.Subject || sess.RevokedAt != nil || !now.Before(sess.ExpiresAt) {
		return nil, ErrSessionEnded
	}
	if sess.RefreshJTI == nil || *sess.RefreshJTI != claims.ID {
		slog.Warn("refresh token reuse detected, revoking session", "session", sess.ID, "user", u.ID, "ip", meta.IP)
		_ = s.sessions.Revoke(ctx, sess.ID, now)
		return nil, ErrRefreshReused
	}
	if !u.IsActive {
		_ = s.sessions.Revoke(ctx, sess.ID, now)
		return nil, ErrUserInactive
	}

	newJTI := uuid.NewString()
	exp := s.refreshExpiry(sess, now)
	ok, err := s.sessions.Rotate(ctx, sess.ID, claims.ID, newJTI, exp, now)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrSessionEnded
	}
	sess.RefreshJTI, sess.ExpiresAt, sess.LastUsedAt = &newJTI, exp, now
	return s.issue(u, sess, now)
}

// Authenticate validates an access token and checks that its session is
// still live and its user still active. The role is read from the database,
// so role changes and revocations apply on the very next request.
func (s *Service) Authenticate(ctx context.Context, raw string) (*Principal, error) {
	claims, err := s.tokens.Parse(KindAccess, raw)
	if err != nil {
		return nil, err
	}
	sess, u, err := s.sessions.GetWithUser(ctx, claims.SessionID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrSessionEnded
	}
	if err != nil {
		return nil, err
	}
	if sess.UserID != claims.Subject || sess.RevokedAt != nil || !database.Now().Before(sess.ExpiresAt) {
		return nil, ErrSessionEnded
	}
	if !u.IsActive {
		return nil, ErrUserInactive
	}
	return &Principal{User: u, Session: sess}, nil
}

// Logout revokes the session behind an access or refresh token. Invalid
// tokens are ignored so logout always succeeds from the client's view.
func (s *Service) Logout(ctx context.Context, accessToken, refreshToken string) {
	var claims *Claims
	if accessToken != "" {
		claims, _ = s.tokens.Parse(KindAccess, accessToken)
	}
	if claims == nil && refreshToken != "" {
		claims, _ = s.tokens.Parse(KindRefresh, refreshToken)
	}
	if claims == nil {
		return
	}
	if _, err := s.sessions.RevokeOwned(ctx, claims.SessionID, claims.Subject, database.Now()); err != nil {
		slog.Warn("logout failed", "session", claims.SessionID, "err", err)
	}
}

func (s *Service) CleanupSessions(ctx context.Context) (int64, error) {
	return s.sessions.DeleteStale(ctx, database.Now())
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
