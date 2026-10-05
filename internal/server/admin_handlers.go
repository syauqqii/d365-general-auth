package server

import (
	"github.com/gofiber/fiber/v2"

	"general-auth/internal/auth"
	"general-auth/internal/store"
)

func (s *Server) listUsers(c *fiber.Ctx) error {
	f := store.UserFilter{
		Query:  c.Query("q"),
		Role:   c.Query("role"),
		Limit:  c.QueryInt("limit", 50),
		Offset: c.QueryInt("offset", 0),
	}
	if f.Limit < 1 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	if v := c.Query("active"); v != "" {
		active := v == "true" || v == "1"
		f.Active = &active
	}
	users, total, err := s.auth.ListUsers(c.UserContext(), f)
	if err != nil {
		return handleErr(c, err)
	}
	return ok(c, fiber.Map{"items": users, "total": total, "limit": f.Limit, "offset": f.Offset})
}

func (s *Server) getUser(c *fiber.Ctx) error {
	u, err := s.auth.UserByID(c.UserContext(), c.Params("id"))
	if err != nil {
		return handleErr(c, err)
	}
	return ok(c, u)
}

func (s *Server) createUser(c *fiber.Ctx) error {
	var in auth.UserInput
	if err := parseJSON(c, &in); err != nil {
		return handleErr(c, err)
	}
	u, err := s.auth.CreateUser(c.UserContext(), principal(c).User, in)
	if err != nil {
		return handleErr(c, err)
	}
	c.Status(fiber.StatusCreated)
	return ok(c, u)
}

func (s *Server) updateUser(c *fiber.Ctx) error {
	var p auth.UserPatch
	if err := parseJSON(c, &p); err != nil {
		return handleErr(c, err)
	}
	u, err := s.auth.UpdateUser(c.UserContext(), principal(c).User, c.Params("id"), p)
	if err != nil {
		return handleErr(c, err)
	}
	return ok(c, u)
}

func (s *Server) setActive(active bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		u, err := s.auth.SetActive(c.UserContext(), principal(c).User, c.Params("id"), active)
		if err != nil {
			return handleErr(c, err)
		}
		return ok(c, u)
	}
}

func (s *Server) userSessions(c *fiber.Ctx) error {
	sessions, err := s.auth.UserSessions(c.UserContext(), c.Params("id"))
	if err != nil {
		return handleErr(c, err)
	}
	return ok(c, fiber.Map{"items": sessions})
}

func (s *Server) revokeUserSessions(c *fiber.Ctx) error {
	n, err := s.auth.RevokeUserSessions(c.UserContext(), principal(c).User, c.Params("id"))
	if err != nil {
		return handleErr(c, err)
	}
	return ok(c, fiber.Map{"revoked_sessions": n})
}

func (s *Server) unlockUser(c *fiber.Ctx) error {
	if err := s.auth.Unlock(c.UserContext(), principal(c).User, c.Params("id")); err != nil {
		return handleErr(c, err)
	}
	return ok(c, fiber.Map{"unlocked": true})
}
