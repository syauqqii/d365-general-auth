package server

import (
	"github.com/gofiber/fiber/v2"
)

type reauthRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
	Email           string `json:"email"`
	Username        string `json:"username"`
}

func (s *Server) me(c *fiber.Ctx) error {
	p := principal(c)
	return ok(c, fiber.Map{"user": p.User, "session": p.Session})
}

func (s *Server) changePassword(c *fiber.Ctx) error {
	var req reauthRequest
	if err := parseJSON(c, &req); err != nil {
		return handleErr(c, err)
	}
	if err := s.auth.ChangePassword(c.UserContext(), principal(c), req.CurrentPassword, req.NewPassword); err != nil {
		return handleErr(c, err)
	}
	return ok(c, fiber.Map{"password_changed": true, "other_sessions_revoked": true})
}

func (s *Server) changeEmail(c *fiber.Ctx) error {
	var req reauthRequest
	if err := parseJSON(c, &req); err != nil {
		return handleErr(c, err)
	}
	u, err := s.auth.ChangeEmail(c.UserContext(), principal(c), req.CurrentPassword, req.Email)
	if err != nil {
		return handleErr(c, err)
	}
	return ok(c, u)
}

func (s *Server) changeUsername(c *fiber.Ctx) error {
	var req reauthRequest
	if err := parseJSON(c, &req); err != nil {
		return handleErr(c, err)
	}
	u, err := s.auth.ChangeUsername(c.UserContext(), principal(c), req.CurrentPassword, req.Username)
	if err != nil {
		return handleErr(c, err)
	}
	return ok(c, u)
}

func (s *Server) mySessions(c *fiber.Ctx) error {
	p := principal(c)
	sessions, err := s.auth.MySessions(c.UserContext(), p)
	if err != nil {
		return handleErr(c, err)
	}
	return ok(c, fiber.Map{"current_session_id": p.Session.ID, "items": sessions})
}

func (s *Server) revokeMySession(c *fiber.Ctx) error {
	if err := s.auth.RevokeMySession(c.UserContext(), principal(c), c.Params("id")); err != nil {
		return handleErr(c, err)
	}
	return ok(c, fiber.Map{"revoked": true})
}
