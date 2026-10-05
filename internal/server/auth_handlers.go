package server

import (
	"encoding/json"
	"time"

	"github.com/gofiber/fiber/v2"

	"general-auth/internal/auth"
)

func parseJSON(c *fiber.Ctx, v any) error {
	if len(c.Body()) == 0 {
		return nil
	}
	if err := json.Unmarshal(c.Body(), v); err != nil {
		return &auth.ValidationError{Msg: "request body must be valid JSON"}
	}
	return nil
}

func meta(c *fiber.Ctx) auth.Meta {
	return auth.Meta{UserAgent: c.Get(fiber.HeaderUserAgent), IP: c.IP()}
}

// useCookie reports whether this client gets its refresh token in an
// HttpOnly cookie (web) instead of the response body (mobile).
func (s *Server) useCookie(client auth.Client) bool {
	return s.cfg.Cookie.Enabled && client == auth.Web
}

func (s *Server) setRefreshCookie(c *fiber.Ctx, token string, exp time.Time) {
	ck := s.cfg.Cookie
	c.Cookie(&fiber.Cookie{
		Name:     ck.Name,
		Value:    token,
		Path:     ck.Path,
		Domain:   ck.Domain,
		Expires:  exp,
		Secure:   ck.Secure,
		HTTPOnly: true,
		SameSite: ck.SameSite,
	})
}

func (s *Server) clearRefreshCookie(c *fiber.Ctx) {
	if !s.cfg.Cookie.Enabled {
		return
	}
	s.setRefreshCookie(c, "", time.Unix(0, 0))
}

// respondTokens sends the token pair. Web clients get the refresh token in
// the cookie only, so JavaScript (and any XSS) can never read it.
func (s *Server) respondTokens(c *fiber.Ctx, res *auth.Result) error {
	if s.useCookie(res.Client) {
		s.setRefreshCookie(c, res.RefreshToken, res.RefreshExpiresAt)
		res.RefreshToken = ""
	}
	return ok(c, res)
}

// refreshTokenFrom reads the refresh token from the body (mobile) or the
// cookie (web). A cookie is sent by the browser automatically, so a
// cookie-based call must also carry X-Requested-With: a cross-site form
// cannot set custom headers, and cross-origin scripts need CORS approval.
func (s *Server) refreshTokenFrom(c *fiber.Ctx, body string) (token string, fromCookie bool, err error) {
	if body != "" {
		return body, false, nil
	}
	if s.cfg.Cookie.Enabled {
		if v := c.Cookies(s.cfg.Cookie.Name); v != "" {
			if c.Get("X-Requested-With") == "" {
				return "", true, &auth.ValidationError{Msg: "X-Requested-With header is required when using the refresh cookie"}
			}
			return v, true, nil
		}
	}
	return "", false, nil
}

// loginRequest accepts either "identifier" (+ optional "login_type") or one
// of "username" / "email" / "code". "client" is "web" (default) or
// "mobile"; the X-Client-Type header works too.
type loginRequest struct {
	Identifier string `json:"identifier"`
	LoginType  string `json:"login_type"`
	Username   string `json:"username"`
	Email      string `json:"email"`
	Code       string `json:"code"`
	Password   string `json:"password"`
	Client     string `json:"client"`
}

func (s *Server) login(c *fiber.Ctx) error {
	var req loginRequest
	if err := parseJSON(c, &req); err != nil {
		return handleErr(c, err)
	}
	if req.Client == "" {
		req.Client = c.Get("X-Client-Type")
	}
	client, err := auth.ParseClient(req.Client)
	if err != nil {
		return handleErr(c, err)
	}
	id, kind := req.Identifier, req.LoginType
	switch {
	case id != "":
	case req.Username != "":
		id, kind = req.Username, "username"
	case req.Email != "":
		id, kind = req.Email, "email"
	case req.Code != "":
		id, kind = req.Code, "code"
	}
	res, err := s.auth.Login(c.UserContext(), client, id, kind, req.Password, meta(c))
	if err != nil {
		return handleErr(c, err)
	}
	return s.respondTokens(c, res)
}

func (s *Server) refresh(c *fiber.Ctx) error {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := parseJSON(c, &req); err != nil {
		return handleErr(c, err)
	}
	token, fromCookie, err := s.refreshTokenFrom(c, req.RefreshToken)
	if err != nil {
		return handleErr(c, err)
	}
	if token == "" {
		return fail(c, fiber.StatusBadRequest, "validation_error", "refresh_token is required")
	}
	res, err := s.auth.Refresh(c.UserContext(), token, meta(c))
	if err != nil {
		if fromCookie {
			s.clearRefreshCookie(c)
		}
		return handleErr(c, err)
	}
	return s.respondTokens(c, res)
}

// logout ends the current session. It accepts the access token, the
// refresh token (body or cookie) or both, so it still works after the
// access token has expired.
func (s *Server) logout(c *fiber.Ctx) error {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := parseJSON(c, &req); err != nil {
		return handleErr(c, err)
	}
	refresh, _, err := s.refreshTokenFrom(c, req.RefreshToken)
	if err != nil {
		return handleErr(c, err)
	}
	s.auth.Logout(c.UserContext(), bearerToken(c), refresh)
	s.clearRefreshCookie(c)
	return ok(c, fiber.Map{"logged_out": true})
}

func (s *Server) logoutAll(c *fiber.Ctx) error {
	n, err := s.auth.LogoutAll(c.UserContext(), principal(c))
	if err != nil {
		return handleErr(c, err)
	}
	s.clearRefreshCookie(c)
	return ok(c, fiber.Map{"revoked_sessions": n})
}
