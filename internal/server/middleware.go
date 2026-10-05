package server

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"general-auth/internal/auth"
	"general-auth/internal/config"
)

const principalKey = "principal"

func principal(c *fiber.Ctx) *auth.Principal {
	p, _ := c.Locals(principalKey).(*auth.Principal)
	return p
}

func bearerToken(c *fiber.Ctx) string {
	h := c.Get(fiber.HeaderAuthorization)
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// authenticate requires a valid access token whose session is still live.
func (s *Server) authenticate(c *fiber.Ctx) error {
	raw := bearerToken(c)
	if raw == "" {
		return fail(c, fiber.StatusUnauthorized, "missing_token", "Authorization: Bearer <access_token> header is required")
	}
	p, err := s.auth.Authenticate(c.UserContext(), raw)
	if err != nil {
		return handleErr(c, err)
	}
	c.Locals(principalKey, p)
	return c.Next()
}

// authorize checks the route against rbac.toml [routes], using the route
// pattern without the /api/v1 prefix (e.g. "/admin/users/:id").
func (s *Server) authorize(c *fiber.Ctx) error {
	p := principal(c)
	path := strings.TrimPrefix(c.Route().Path, config.APIPrefix)
	if !s.policy.RouteAllowed(p.User.Role, c.Method(), path) {
		return fail(c, fiber.StatusForbidden, "forbidden", "your role is not allowed to access this route")
	}
	return c.Next()
}

func retryAfter(le *auth.LockedError) string {
	secs := int(time.Until(le.Until).Seconds()) + 1
	if secs < 1 {
		secs = 1
	}
	return strconv.Itoa(secs)
}
