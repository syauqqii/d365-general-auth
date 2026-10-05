package auth

import (
	"context"
	"log/slog"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"general-auth/internal/database"
	"general-auth/internal/store"
)

// Self-service account changes. Each one needs the current password (so a
// stolen access token alone cannot take over the account) and the user's
// matching can_change_* flag.

func (s *Service) ChangePassword(ctx context.Context, p *Principal, current, next string) error {
	if !p.User.CanChangePassword {
		return &ForbiddenError{"you are not allowed to change your password; contact an administrator"}
	}
	if err := s.reauth(ctx, p.User, current); err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(p.User.PasswordHash), []byte(next)) == nil {
		return &ValidationError{"new password must differ from the current one"}
	}
	u := *p.User
	if err := s.setPassword(&u, next, ""); err != nil {
		return err
	}
	if err := s.save(ctx, &u); err != nil {
		return err
	}
	// Every other device must log in again with the new password.
	_, err := s.sessions.RevokeAllForUser(ctx, u.ID, p.Session.ID, database.Now())
	slog.Info("password changed", "user", u.ID)
	return err
}

func (s *Service) ChangeEmail(ctx context.Context, p *Principal, current, email string) (*store.User, error) {
	if !p.User.CanChangeEmail {
		return nil, &ForbiddenError{"you are not allowed to change your email; contact an administrator"}
	}
	if err := s.reauth(ctx, p.User, current); err != nil {
		return nil, err
	}
	u := *p.User
	u.Email = normEmail(&email)
	if u.Email == nil {
		return nil, &ValidationError{"email is required"}
	}
	if err := s.validateUser(&u); err != nil {
		return nil, err
	}
	if err := s.save(ctx, &u); err != nil {
		return nil, err
	}
	slog.Info("email changed", "user", u.ID)
	return &u, nil
}

func (s *Service) ChangeUsername(ctx context.Context, p *Principal, current, username string) (*store.User, error) {
	if !p.User.CanChangeUsername {
		return nil, &ForbiddenError{"you are not allowed to change your username; contact an administrator"}
	}
	if err := s.reauth(ctx, p.User, current); err != nil {
		return nil, err
	}
	u := *p.User
	u.Username = strings.ToLower(strings.TrimSpace(username))
	if err := s.validateUser(&u); err != nil {
		return nil, err
	}
	if err := s.save(ctx, &u); err != nil {
		return nil, err
	}
	slog.Info("username changed", "user", u.ID, "username", u.Username)
	return &u, nil
}

func (s *Service) MySessions(ctx context.Context, p *Principal) ([]store.Session, error) {
	return s.sessions.ListActive(ctx, p.User.ID, database.Now())
}

// RevokeMySession logs out one of the caller's own devices.
func (s *Service) RevokeMySession(ctx context.Context, p *Principal, sessionID string) error {
	ok, err := s.sessions.RevokeOwned(ctx, sessionID, p.User.ID, database.Now())
	if err != nil {
		return err
	}
	if !ok {
		return store.ErrNotFound
	}
	return nil
}

// LogoutAll ends every session of the caller, including the current one.
func (s *Service) LogoutAll(ctx context.Context, p *Principal) (int64, error) {
	return s.sessions.RevokeAllForUser(ctx, p.User.ID, "", database.Now())
}

func (s *Service) save(ctx context.Context, u *store.User) error {
	u.UpdatedAt = database.Now()
	return s.users.Update(ctx, u)
}

// reauth checks the current password for a sensitive change. Wrong guesses
// count toward the same lockout as logins.
func (s *Service) reauth(ctx context.Context, u *store.User, password string) error {
	now := database.Now()
	if u.IsLocked(now) {
		return &LockedError{Until: *u.LockedUntil}
	}
	if err := s.verifyPassword(ctx, u, password, now); err != nil {
		return &ValidationError{"current_password is incorrect"}
	}
	return nil
}
