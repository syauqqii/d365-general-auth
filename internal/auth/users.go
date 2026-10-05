package auth

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"general-auth/internal/database"
	"general-auth/internal/store"
)

// UserInput is used by the admin API and by the seeder (init/users.json).
// Give either password (plain) or password_hash (bcrypt). Unset can_change_*
// flags take the defaults from config [account].
type UserInput struct {
	Username          string  `json:"username"`
	Email             *string `json:"email"`
	Code              *string `json:"code"`
	Name              string  `json:"name"`
	Password          string  `json:"password"`
	PasswordHash      string  `json:"password_hash"`
	Role              string  `json:"role"`
	IsActive          *bool   `json:"is_active"`
	CanChangeUsername *bool   `json:"can_change_username"`
	CanChangeEmail    *bool   `json:"can_change_email"`
	CanChangePassword *bool   `json:"can_change_password"`
}

// UserPatch changes only the fields that are set. An empty email or code
// clears it.
type UserPatch struct {
	Username          *string `json:"username"`
	Email             *string `json:"email"`
	Code              *string `json:"code"`
	Name              *string `json:"name"`
	Password          *string `json:"password"`
	PasswordHash      *string `json:"password_hash"`
	Role              *string `json:"role"`
	IsActive          *bool   `json:"is_active"`
	CanChangeUsername *bool   `json:"can_change_username"`
	CanChangeEmail    *bool   `json:"can_change_email"`
	CanChangePassword *bool   `json:"can_change_password"`
}

// PatchFrom turns a full input into a patch, used by `seed -update`.
func PatchFrom(in UserInput) UserPatch {
	p := UserPatch{
		Email: in.Email, Code: in.Code, Name: &in.Name, Role: &in.Role, IsActive: in.IsActive,
		CanChangeUsername: in.CanChangeUsername, CanChangeEmail: in.CanChangeEmail, CanChangePassword: in.CanChangePassword,
	}
	if in.Password != "" {
		p.Password = &in.Password
	}
	if in.PasswordHash != "" {
		p.PasswordHash = &in.PasswordHash
	}
	return p
}

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,99}$`)

// canManage enforces the protection rules for admin actions. actor is nil
// for the CLI/seeder, which may do anything.
//
//   - nobody manages their own account through the admin API (use /me)
//   - users with a protected role can only be managed by a superuser
//   - superusers cannot be managed through the API at all
//   - only a superuser may grant a protected role; nobody may grant a
//     superuser role through the API
func (s *Service) canManage(actor, target *store.User, newRole string) error {
	if actor == nil {
		return nil
	}
	if target != nil && actor.ID == target.ID {
		return &ForbiddenError{"use the /me endpoints to change your own account"}
	}
	actorSuper := s.policy.IsSuperuser(actor.Role)
	if target != nil {
		if s.policy.IsSuperuser(target.Role) {
			return &ForbiddenError{"superuser accounts can only be managed from the server CLI"}
		}
		if s.policy.IsProtected(target.Role) && !actorSuper {
			return &ForbiddenError{fmt.Sprintf("users with role %q can only be managed by a superuser", target.Role)}
		}
	}
	if newRole != "" {
		if s.policy.IsSuperuser(newRole) {
			return &ForbiddenError{fmt.Sprintf("role %q can only be assigned from the server CLI", newRole)}
		}
		if s.policy.IsProtected(newRole) && !actorSuper {
			return &ForbiddenError{fmt.Sprintf("only a superuser may assign role %q", newRole)}
		}
	}
	return nil
}

func (s *Service) UserByID(ctx context.Context, id string) (*store.User, error) {
	return s.users.ByID(ctx, id)
}

func (s *Service) UserByUsername(ctx context.Context, username string) (*store.User, error) {
	return s.users.ByUsername(ctx, username)
}

func (s *Service) ListUsers(ctx context.Context, f store.UserFilter) ([]store.User, int, error) {
	return s.users.List(ctx, f)
}

func (s *Service) UserSessions(ctx context.Context, id string) ([]store.Session, error) {
	if _, err := s.users.ByID(ctx, id); err != nil {
		return nil, err
	}
	return s.sessions.ListActive(ctx, id, database.Now())
}

func (s *Service) CreateUser(ctx context.Context, actor *store.User, in UserInput) (*store.User, error) {
	role := strings.TrimSpace(in.Role)
	if err := s.canManage(actor, nil, role); err != nil {
		return nil, err
	}
	acc := s.cfg.Account
	now := database.Now()
	u := &store.User{
		ID:                uuid.NewString(),
		Username:          strings.ToLower(strings.TrimSpace(in.Username)),
		Email:             normEmail(in.Email),
		Code:              normCode(in.Code),
		Name:              strings.TrimSpace(in.Name),
		Role:              role,
		IsActive:          boolOr(in.IsActive, true),
		CanChangeUsername: boolOr(in.CanChangeUsername, acc.DefaultCanChangeUsername),
		CanChangeEmail:    boolOr(in.CanChangeEmail, acc.DefaultCanChangeEmail),
		CanChangePassword: boolOr(in.CanChangePassword, acc.DefaultCanChangePassword),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := s.setPassword(u, in.Password, in.PasswordHash); err != nil {
		return nil, err
	}
	if err := s.validateUser(u); err != nil {
		return nil, err
	}
	if err := s.users.Create(ctx, u); err != nil {
		return nil, err
	}
	slog.Info("user created", "user", u.ID, "username", u.Username, "role", u.Role, "by", actorID(actor))
	return u, nil
}

// UpdateUser applies a patch. Changing the password or deactivating the
// user signs them out everywhere.
func (s *Service) UpdateUser(ctx context.Context, actor *store.User, id string, p UserPatch) (*store.User, error) {
	u, err := s.users.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	newRole := ""
	if p.Role != nil && strings.TrimSpace(*p.Role) != u.Role {
		newRole = strings.TrimSpace(*p.Role)
	}
	if err := s.canManage(actor, u, newRole); err != nil {
		return nil, err
	}

	if p.Username != nil {
		u.Username = strings.ToLower(strings.TrimSpace(*p.Username))
	}
	if p.Email != nil {
		u.Email = normEmail(p.Email)
	}
	if p.Code != nil {
		u.Code = normCode(p.Code)
	}
	if p.Name != nil {
		u.Name = strings.TrimSpace(*p.Name)
	}
	if p.Role != nil {
		u.Role = strings.TrimSpace(*p.Role)
	}
	u.CanChangeUsername = boolOr(p.CanChangeUsername, u.CanChangeUsername)
	u.CanChangeEmail = boolOr(p.CanChangeEmail, u.CanChangeEmail)
	u.CanChangePassword = boolOr(p.CanChangePassword, u.CanChangePassword)

	revoke := false
	if p.IsActive != nil {
		revoke = u.IsActive && !*p.IsActive
		u.IsActive = *p.IsActive
	}
	if p.Password != nil || p.PasswordHash != nil {
		if err := s.setPassword(u, deref(p.Password), deref(p.PasswordHash)); err != nil {
			return nil, err
		}
		revoke = true
	}
	if err := s.validateUser(u); err != nil {
		return nil, err
	}
	if err := s.save(ctx, u); err != nil {
		return nil, err
	}
	if revoke {
		if _, err := s.sessions.RevokeAllForUser(ctx, u.ID, "", database.Now()); err != nil {
			return nil, err
		}
	}
	slog.Info("user updated", "user", u.ID, "by", actorID(actor), "sessions_revoked", revoke)
	return u, nil
}

// SetActive activates or deactivates a user. Deactivating also ends every
// session, so the user is logged out on web and mobile immediately.
func (s *Service) SetActive(ctx context.Context, actor *store.User, id string, active bool) (*store.User, error) {
	return s.UpdateUser(ctx, actor, id, UserPatch{IsActive: &active})
}

// RevokeUserSessions force-logs-out a user on every device.
func (s *Service) RevokeUserSessions(ctx context.Context, actor *store.User, id string) (int64, error) {
	u, err := s.users.ByID(ctx, id)
	if err != nil {
		return 0, err
	}
	if err := s.canManage(actor, u, ""); err != nil {
		return 0, err
	}
	n, err := s.sessions.RevokeAllForUser(ctx, u.ID, "", database.Now())
	slog.Info("user sessions revoked", "user", u.ID, "by", actorID(actor), "count", n)
	return n, err
}

// Unlock clears a lockout caused by failed logins.
func (s *Service) Unlock(ctx context.Context, actor *store.User, id string) error {
	u, err := s.users.ByID(ctx, id)
	if err != nil {
		return err
	}
	if err := s.canManage(actor, u, ""); err != nil {
		return err
	}
	return s.users.Unlock(ctx, u.ID)
}

func (s *Service) setPassword(u *store.User, plain, hash string) error {
	if hash != "" {
		cost, err := bcrypt.Cost([]byte(hash))
		if err != nil {
			return &ValidationError{"password_hash is not a valid bcrypt hash"}
		}
		if cost < 10 {
			return &ValidationError{"password_hash must use a bcrypt cost of at least 10"}
		}
		u.PasswordHash = hash
	} else {
		min := s.cfg.Security.PasswordMinLength
		switch {
		case len(plain) < min:
			return &ValidationError{fmt.Sprintf("password must be at least %d characters", min)}
		case len(plain) > 72:
			return &ValidationError{"password must be at most 72 bytes"}
		case strings.EqualFold(plain, u.Username):
			return &ValidationError{"password must not equal the username"}
		}
		h, err := bcrypt.GenerateFromPassword([]byte(plain), s.cfg.Security.BcryptCost)
		if err != nil {
			return err
		}
		u.PasswordHash = string(h)
	}
	now := database.Now()
	u.PasswordChangedAt = &now
	return nil
}

func (s *Service) validateUser(u *store.User) error {
	switch {
	case !usernamePattern.MatchString(u.Username):
		return &ValidationError{"username must be 3-100 characters of a-z, 0-9, '.', '_' or '-', starting with a letter or digit"}
	case u.Email != nil && (len(*u.Email) > 255 || !strings.Contains(*u.Email, "@") || strings.ContainsAny(*u.Email, " \t\r\n")):
		return &ValidationError{"email is not valid"}
	case u.Code != nil && len(*u.Code) > 100:
		return &ValidationError{"code must be at most 100 characters"}
	case len(u.Name) > 255:
		return &ValidationError{"name must be at most 255 characters"}
	case u.Role == "":
		return &ValidationError{"role is required"}
	case !s.policy.KnownRole(u.Role):
		return &ValidationError{fmt.Sprintf("role %q is not listed in rbac roles %v", u.Role, s.policy.Roles)}
	}
	return nil
}

func normEmail(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.ToLower(strings.TrimSpace(*p))
	if v == "" {
		return nil
	}
	return &v
}

func normCode(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return nil
	}
	return &v
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func actorID(u *store.User) string {
	if u == nil {
		return "cli"
	}
	return u.ID
}
