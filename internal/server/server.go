package server

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/helmet"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/fiber/v2/middleware/requestid"

	"general-auth/internal/auth"
	"general-auth/internal/config"
	"general-auth/internal/d365"
	"general-auth/internal/rbac"
	"general-auth/internal/store"
)

type Server struct {
	cfg        *config.Config
	app        *fiber.App
	auth       *auth.Service
	policy     *rbac.Policy
	connectors map[string]*d365.Connector
}

func New(cfg *config.Config, svc *auth.Service, policy *rbac.Policy, connectors map[string]*d365.Connector) *Server {
	app := fiber.New(fiber.Config{
		AppName:                 cfg.App.Name,
		Immutable:               true,
		BodyLimit:               cfg.App.BodyLimitMB * 1024 * 1024,
		ReadTimeout:             cfg.App.ReadTimeout.Duration,
		WriteTimeout:            cfg.App.WriteTimeout.Duration,
		IdleTimeout:             cfg.App.IdleTimeout.Duration,
		ProxyHeader:             cfg.App.ProxyHeader,
		EnableTrustedProxyCheck: len(cfg.App.TrustedProxies) > 0,
		TrustedProxies:          cfg.App.TrustedProxies,
		DisableStartupMessage:   true,
		ErrorHandler:            errorHandler,
	})
	s := &Server{cfg: cfg, app: app, auth: svc, policy: policy, connectors: connectors}
	s.routes()
	return s
}

func (s *Server) Listen() error { return s.app.Listen(s.cfg.App.Addr()) }

func (s *Server) Shutdown(ctx context.Context) error { return s.app.ShutdownWithContext(ctx) }

// App exposes the fiber app, mainly for tests (app.Test).
func (s *Server) App() *fiber.App { return s.app }

func (s *Server) routes() {
	s.app.Use(
		recover.New(),
		requestid.New(),
		logger.New(logger.Config{Format: "${time} ${locals:requestid} ${status} ${method} ${path} ${latency} ${ip}\n"}),
		helmet.New(),
		noStore,
	)
	if origins := s.cfg.App.CORSOrigins; len(origins) > 0 {
		wildcard := len(origins) == 1 && origins[0] == "*"
		s.app.Use(cors.New(cors.Config{
			AllowOrigins: strings.Join(origins, ","),
			// Credentials (the refresh cookie) are only allowed for exact origins.
			AllowCredentials: !wildcard,
			AllowHeaders:     "Origin, Content-Type, Accept, Authorization, If-Match, Prefer, X-Client-Type, X-Requested-With",
			AllowMethods:     "GET,POST,PUT,PATCH,DELETE,OPTIONS",
			MaxAge:           600,
		}))
	}

	s.app.Get("/health", func(c *fiber.Ctx) error { return ok(c, fiber.Map{"status": "ok"}) })

	authLimit := limiter.New(limiter.Config{
		Max:          s.cfg.Security.LoginRateLimit,
		Expiration:   s.cfg.Security.LoginRateWindow.Duration,
		KeyGenerator: func(c *fiber.Ctx) string { return c.IP() + "|" + c.Path() },
		LimitReached: func(c *fiber.Ctx) error {
			return fail(c, fiber.StatusTooManyRequests, "rate_limited", "too many requests, try again later")
		},
	})

	g := s.app.Group(config.APIPrefix)
	guard := func(h fiber.Handler) []fiber.Handler {
		return []fiber.Handler{s.authenticate, s.authorize, h}
	}

	g.Post("/auth/login", authLimit, s.login)
	g.Post("/auth/refresh", authLimit, s.refresh)
	g.Post("/auth/logout", s.logout)
	g.Post("/auth/logout-all", guard(s.logoutAll)...)

	g.Get("/me", guard(s.me)...)
	g.Put("/me/password", authLimit, s.authenticate, s.authorize, s.changePassword)
	g.Put("/me/email", authLimit, s.authenticate, s.authorize, s.changeEmail)
	g.Put("/me/username", authLimit, s.authenticate, s.authorize, s.changeUsername)
	g.Get("/me/sessions", guard(s.mySessions)...)
	g.Delete("/me/sessions/:id", guard(s.revokeMySession)...)

	g.Get("/admin/users", guard(s.listUsers)...)
	g.Post("/admin/users", guard(s.createUser)...)
	g.Get("/admin/users/:id", guard(s.getUser)...)
	g.Patch("/admin/users/:id", guard(s.updateUser)...)
	g.Get("/admin/users/:id/sessions", guard(s.userSessions)...)
	g.Post("/admin/users/:id/activate", guard(s.setActive(true))...)
	g.Post("/admin/users/:id/deactivate", guard(s.setActive(false))...)
	g.Post("/admin/users/:id/revoke-sessions", guard(s.revokeUserSessions)...)
	g.Post("/admin/users/:id/unlock", guard(s.unlockUser)...)

	g.Get("/d365", guard(s.d365Connectors)...)
	g.Get("/d365/:connector", guard(s.d365Entities)...)
	g.All("/d365/:connector/*", guard(s.d365Proxy)...)

	s.app.Use(func(c *fiber.Ctx) error {
		return fail(c, fiber.StatusNotFound, "not_found", "route not found")
	})
}

// noStore keeps tokens and personal data out of browser and proxy caches.
func noStore(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.Next()
}

func ok(c *fiber.Ctx, data any) error {
	return c.JSON(fiber.Map{"success": true, "data": data})
}

func fail(c *fiber.Ctx, status int, code, msg string) error {
	return c.Status(status).JSON(fiber.Map{"success": false, "error": fiber.Map{"code": code, "message": msg}})
}

func errorHandler(c *fiber.Ctx, err error) error {
	var fe *fiber.Error
	if errors.As(err, &fe) {
		return fail(c, fe.Code, "http_error", fe.Message)
	}
	slog.Error("unhandled error", "path", c.Path(), "request_id", c.Locals("requestid"), "err", err)
	return fail(c, fiber.StatusInternalServerError, "internal_error", "internal server error")
}

// handleErr maps service errors to HTTP responses.
func handleErr(c *fiber.Ctx, err error) error {
	var (
		ve *auth.ValidationError
		fe *auth.ForbiddenError
		le *auth.LockedError
		ce *store.ConflictError
	)
	switch {
	case errors.As(err, &ve):
		return fail(c, fiber.StatusBadRequest, "validation_error", ve.Msg)
	case errors.As(err, &fe):
		return fail(c, fiber.StatusForbidden, "forbidden", fe.Msg)
	case errors.As(err, &le):
		c.Set(fiber.HeaderRetryAfter, retryAfter(le))
		return fail(c, fiber.StatusTooManyRequests, "account_locked", le.Error())
	case errors.As(err, &ce):
		return fail(c, fiber.StatusConflict, "conflict", ce.Error())
	case errors.Is(err, store.ErrNotFound):
		return fail(c, fiber.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, auth.ErrInvalidLoginType):
		return fail(c, fiber.StatusBadRequest, "invalid_login_type", err.Error())
	case errors.Is(err, auth.ErrInvalidCredentials):
		return fail(c, fiber.StatusUnauthorized, "invalid_credentials", "invalid credentials")
	case errors.Is(err, auth.ErrUserInactive):
		return fail(c, fiber.StatusForbidden, "user_inactive", "user is inactive")
	case errors.Is(err, auth.ErrRefreshReused):
		return fail(c, fiber.StatusUnauthorized, "refresh_token_reused", err.Error())
	case errors.Is(err, auth.ErrSessionEnded):
		return fail(c, fiber.StatusUnauthorized, "session_ended", err.Error())
	case errors.Is(err, auth.ErrInvalidToken):
		return fail(c, fiber.StatusUnauthorized, "invalid_token", "invalid or expired token")
	}
	return err // logged by errorHandler as 500
}
