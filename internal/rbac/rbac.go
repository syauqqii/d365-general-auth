// Package rbac decides which roles may call which API route and which D365
// entity, based on rbac.toml. Nothing about roles is hardcoded.
package rbac

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// Wildcard in a role list means "any authenticated user".
const Wildcard = "*"

type Action string

const (
	Read   Action = "read"
	Create Action = "create"
	Update Action = "update"
	Delete Action = "delete"
)

// ActionForMethod maps an HTTP method to a D365 entity action.
func ActionForMethod(method string) (Action, bool) {
	switch strings.ToUpper(method) {
	case "GET", "HEAD":
		return Read, true
	case "POST":
		return Create, true
	case "PATCH", "PUT", "MERGE":
		return Update, true
	case "DELETE":
		return Delete, true
	}
	return "", false
}

// Rule lists the roles per action. A nil list falls through to the next,
// less specific rule; an empty list ([]) denies everyone but superusers.
type Rule struct {
	All    []string `toml:"all"`
	Read   []string `toml:"read"`
	Create []string `toml:"create"`
	Update []string `toml:"update"`
	Delete []string `toml:"delete"`
}

func (r Rule) roles(a Action) []string {
	var specific []string
	switch a {
	case Read:
		specific = r.Read
	case Create:
		specific = r.Create
	case Update:
		specific = r.Update
	case Delete:
		specific = r.Delete
	}
	if specific != nil {
		return specific
	}
	return r.All
}

type Policy struct {
	// Roles, when non-empty, is the whitelist of roles a user may have.
	Roles []string `toml:"roles"`
	// SuperuserRoles may call every route and entity.
	SuperuserRoles []string `toml:"superuser_roles"`
	// AdminRoles is the default for /admin/* routes.
	AdminRoles []string `toml:"admin_roles"`
	// ProtectedRoles: users with these roles cannot be edited, deactivated
	// or force-logged-out through the admin API, except by a superuser (and
	// superusers only through the CLI/seeder).
	ProtectedRoles []string `toml:"protected_roles"`
	// Default applies to any route or entity without its own rule.
	Default []string `toml:"default"`
	// Clients maps a client type (web, mobile) to the roles allowed to log
	// in with it. A missing client type allows every role.
	Clients map[string][]string `toml:"clients"`
	// Routes maps "METHOD /path" (path as registered, without the /api/v1
	// prefix) to roles. Use "*" as METHOD for any.
	Routes map[string][]string `toml:"routes"`
	// D365 maps connector -> entity -> rule. Either may be "*".
	D365 map[string]map[string]Rule `toml:"d365"`
}

// Default is used when the rbac file is missing: every logged-in user may
// use every non-admin route.
func Default() *Policy {
	return &Policy{
		Default:        []string{Wildcard},
		AdminRoles:     []string{"admin"},
		SuperuserRoles: []string{"admin"},
		ProtectedRoles: []string{"admin"},
	}
}

// Load reads the policy file. found is false when the file does not exist,
// in which case Default is returned.
func Load(path string) (p *Policy, found bool, err error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), false, nil
	}
	if err != nil {
		return nil, false, err
	}
	p = &Policy{}
	md, err := toml.Decode(string(raw), p)
	if err != nil {
		return nil, true, fmt.Errorf("parse %s: %w", path, err)
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		return nil, true, fmt.Errorf("parse %s: unknown keys %v", path, keys)
	}
	if p.Default == nil {
		p.Default = []string{Wildcard}
	}
	if p.AdminRoles == nil {
		p.AdminRoles = []string{"admin"}
	}
	for name := range p.Clients {
		if name != "web" && name != "mobile" {
			return nil, true, fmt.Errorf("%s: [clients] only supports web and mobile, got %q", path, name)
		}
	}
	routes := make(map[string][]string, len(p.Routes))
	for k, v := range p.Routes {
		key, err := routeKey(k)
		if err != nil {
			return nil, true, fmt.Errorf("%s: %w", path, err)
		}
		routes[key] = v
	}
	p.Routes = routes
	return p, true, nil
}

func routeKey(s string) (string, error) {
	f := strings.Fields(s)
	if len(f) != 2 || !strings.HasPrefix(f[1], "/") {
		return "", fmt.Errorf("invalid route key %q, expected \"METHOD /path\"", s)
	}
	return strings.ToUpper(f[0]) + " " + f[1], nil
}

func allowed(role string, roles []string) bool {
	return slices.Contains(roles, Wildcard) || slices.Contains(roles, role)
}

func (p *Policy) IsSuperuser(role string) bool { return slices.Contains(p.SuperuserRoles, role) }

func (p *Policy) IsProtected(role string) bool {
	return p.IsSuperuser(role) || slices.Contains(p.ProtectedRoles, role)
}

// KnownRole reports whether a role may be assigned to a user.
func (p *Policy) KnownRole(role string) bool {
	return len(p.Roles) == 0 || slices.Contains(p.Roles, role)
}

// ClientAllowed reports whether a role may log in from a client type.
func (p *Policy) ClientAllowed(role, client string) bool {
	roles, ok := p.Clients[client]
	return !ok || p.IsSuperuser(role) || allowed(role, roles)
}

func (p *Policy) RouteAllowed(role, method, path string) bool {
	if p.IsSuperuser(role) {
		return true
	}
	method = strings.ToUpper(method)
	if roles, ok := p.Routes[method+" "+path]; ok {
		return allowed(role, roles)
	}
	if roles, ok := p.Routes["* "+path]; ok {
		return allowed(role, roles)
	}
	if path == "/admin" || strings.HasPrefix(path, "/admin/") {
		return allowed(role, p.AdminRoles)
	}
	return allowed(role, p.Default)
}

func (p *Policy) EntityAllowed(role, connector, entity string, a Action) bool {
	if p.IsSuperuser(role) {
		return true
	}
	lookups := [][2]string{{connector, entity}, {Wildcard, entity}, {connector, Wildcard}, {Wildcard, Wildcard}}
	for _, l := range lookups {
		rule, ok := p.D365[l[0]][l[1]]
		if !ok {
			continue
		}
		if roles := rule.roles(a); roles != nil {
			return allowed(role, roles)
		}
	}
	return allowed(role, p.Default)
}

// EntityActions lists the actions a role may perform on an entity.
func (p *Policy) EntityActions(role, connector, entity string) []Action {
	out := []Action{}
	for _, a := range []Action{Read, Create, Update, Delete} {
		if p.EntityAllowed(role, connector, entity, a) {
			out = append(out, a)
		}
	}
	return out
}
