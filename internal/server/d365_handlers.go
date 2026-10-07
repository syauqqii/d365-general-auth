package server

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v2"

	"general-auth/internal/rbac"
)

// entityPath matches "customer", "customer('10000')" or
// "customers(dataAreaId='usmf',CustomerAccount='US-001')". Slashes are not
// allowed, so navigation properties cannot reach entities outside the map.
var entityPath = regexp.MustCompile(`^([A-Za-z0-9_\-]+)(\([^/]*\))?$`)

var (
	forwardRequestHeaders  = []string{"Accept", "Content-Type", "If-Match", "If-None-Match", "Prefer", "Accept-Language"}
	forwardResponseHeaders = []string{"Content-Type", "ETag", "OData-Version", "Preference-Applied"}
)

// d365Connectors lists connectors and the entities the caller may read.
func (s *Server) d365Connectors(c *fiber.Ctx) error {
	role := principal(c).User.Role
	names := make([]string, 0, len(s.connectors))
	for n := range s.connectors {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]fiber.Map, 0, len(names))
	for _, n := range names {
		conn := s.connectors[n]
		entities := []string{}
		for _, e := range conn.Entities() {
			if s.policy.EntityAllowed(role, n, e, rbac.Read) {
				entities = append(entities, e)
			}
		}
		out = append(out, fiber.Map{"name": n, "instance": conn.Instance, "entities": entities})
	}
	return ok(c, out)
}

// d365Entities lists a connector's entities with the actions the caller may
// perform on each.
func (s *Server) d365Entities(c *fiber.Ctx) error {
	name := c.Params("connector")
	conn, found := s.connectors[name]
	if !found {
		return fail(c, fiber.StatusNotFound, "connector_not_found", fmt.Sprintf("connector %q is not configured", name))
	}
	role := principal(c).User.Role
	entities := fiber.Map{}
	for _, e := range conn.Entities() {
		if actions := s.policy.EntityActions(role, name, e); len(actions) > 0 {
			entities[e] = actions
		}
	}
	return ok(c, fiber.Map{"name": name, "instance": conn.Instance, "entities": entities})
}

// d365Proxy forwards /d365/<connector>/<entity>[(key)]?<odata query> to the
// D365 endpoint mapped in the connector file, using the backend's own
// Azure AD token. The response body is returned as D365 sent it.
func (s *Server) d365Proxy(c *fiber.Ctx) error {
	name := c.Params("connector")
	conn, found := s.connectors[name]
	if !found {
		return fail(c, fiber.StatusNotFound, "connector_not_found", fmt.Sprintf("connector %q is not configured", name))
	}
	rest, err := url.PathUnescape(c.Params("*"))
	if err != nil {
		return fail(c, fiber.StatusBadRequest, "invalid_entity_path", "entity path is not valid")
	}
	m := entityPath.FindStringSubmatch(rest)
	if m == nil {
		return fail(c, fiber.StatusBadRequest, "invalid_entity_path", "expected /d365/<connector>/<entity> or /d365/<connector>/<entity>(<key>)")
	}
	entity, key := m[1], m[2]
	endpoint, found := conn.Endpoints[entity]
	if !found {
		return fail(c, fiber.StatusNotFound, "entity_not_found", fmt.Sprintf("entity %q is not mapped in connector %q", entity, name))
	}
	action, found := rbac.ActionForMethod(c.Method())
	if !found {
		return fail(c, fiber.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
	if !s.policy.EntityAllowed(principal(c).User.Role, name, entity, action) {
		return fail(c, fiber.StatusForbidden, "forbidden", fmt.Sprintf("your role may not %s %s", action, entity))
	}

	header := http.Header{}
	for _, h := range forwardRequestHeaders {
		if v := c.Get(h); v != "" {
			header.Set(h, v)
		}
	}
	if (action == rbac.Update || action == rbac.Delete) && header.Get("If-Match") == "" {
		header.Set("If-Match", "*")
	}

	resp, err := conn.Do(c.UserContext(), c.Method(), endpoint, escapeODataKey(key),
		string(c.Request().URI().QueryString()), c.Body(), header)
	if err != nil {
		slog.Error("d365 request failed", "connector", name, "entity", entity, "err", err)
		return fail(c, fiber.StatusBadGateway, "d365_unavailable", "could not reach D365")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fail(c, fiber.StatusBadGateway, "d365_unavailable", "could not read D365 response")
	}
	for _, h := range forwardResponseHeaders {
		if v := resp.Header.Get(h); v != "" {
			c.Set(h, v)
		}
	}
	return c.Status(resp.StatusCode).Send(body)
}

// escapeODataKey percent-encodes everything except the characters OData keys
// use literally, e.g. ('10000') or (dataAreaId='usmf',CustomerAccount='X').
func escapeODataKey(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' ||
			strings.IndexByte("-_.~'()=,:@$&+!*;", ch) >= 0 {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}
