package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"general-auth/internal/auth"
	"general-auth/internal/config"
	"general-auth/internal/d365"
	"general-auth/internal/database"
	"general-auth/internal/rbac"
	"general-auth/internal/server"
	"general-auth/internal/store"
)

type env struct {
	t   *testing.T
	srv *server.Server
	svc *auth.Service
}

type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   struct {
		Code string `json:"code"`
	} `json:"error"`
}

type reply struct {
	status int
	env    envelope
	raw    []byte
	resp   *http.Response
}

func (e *env) req(method, path, token string, body any, headers ...string) reply {
	e.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := e.srv.App().Test(req, 10000)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	var env envelope
	_ = json.Unmarshal(raw, &env)
	return reply{resp.StatusCode, env, raw, resp}
}

func (r reply) expect(t *testing.T, name string, status int, code ...string) reply {
	t.Helper()
	if r.status != status || (len(code) > 0 && r.env.Error.Code != code[0]) {
		t.Fatalf("%s: got %d %q, want %d %v; body %s", name, r.status, r.env.Error.Code, status, code, r.raw)
	}
	return r
}

func (r reply) cookie(name string) *http.Cookie {
	for _, c := range r.resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

const api = config.APIPrefix

func setup(t *testing.T) (*env, *int32) {
	t.Helper()
	ctx := context.Background()

	// Fake Microsoft Entra ID + D365.
	var tokenCalls int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			atomic.AddInt32(&tokenCalls, 1)
			r.ParseForm()
			if r.Form.Get("client_secret") != "s3cret" || r.Form.Get("grant_type") != "client_credentials" {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"token_type":"Bearer","expires_in":3599,"access_token":"aad-token"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"method": r.Method, "path": r.URL.EscapedPath(), "query": r.URL.RawQuery,
			"auth": r.Header.Get("Authorization"), "if_match": r.Header.Get("If-Match"),
		})
	}))
	t.Cleanup(fake.Close)

	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Setenv("TEST_FO_SECRET", "s3cret")
	conn, err := d365.Load(write("connector-fo.toml", `
instance = "fo"
d365_tenant_id = "tenant-1"
d365_client_id = "client-1"
d365_client_secret = "${TEST_FO_SECRET}"
d365_grant_type = "client_credentials"
d365_scope = "https://contoso.operations.dynamics.com/.default"
d365_auth_url = "`+fake.URL+`/%s/oauth2/v2.0/token"
api_url = "`+fake.URL+`/data/%s"
[endpoints]
customer = "CustomersV3"
so_header = "SalesOrderHeadersV2"
[default_query]
"$top" = "100"
[forced_query]
cross-company = "false"
`))
	if err != nil {
		t.Fatal(err)
	}

	policy, found, err := rbac.Load(write("rbac.toml", `
roles = ["superadmin", "admin", "sales", "viewer"]
superuser_roles = ["superadmin"]
admin_roles = ["superadmin", "admin"]
protected_roles = ["superadmin", "admin"]
default = ["*"]
[clients]
mobile = ["sales"]
[d365."*"."*"]
delete = []
[d365.fo.customer]
read = ["*"]
create = ["admin"]
[d365.fo.so_header]
all = ["sales"]
`))
	if err != nil || !found {
		t.Fatalf("rbac: %v %v", found, err)
	}

	cfg := &config.Config{
		App: config.App{Name: "test", Host: "127.0.0.1", Port: 3000, BodyLimitMB: 1},
		JWT: config.JWT{
			Issuer: "test", Audience: "test-api",
			Access: config.AccessToken{Secret: strings.Repeat("a", 32), TTL: config.Duration{Duration: 15 * time.Minute}},
			Refresh: config.RefreshToken{
				Secret:        strings.Repeat("r", 32),
				WebTTL:        config.Duration{Duration: 7 * 24 * time.Hour},
				MobileTTL:     config.Duration{Duration: 90 * 24 * time.Hour},
				MaxSessionAge: config.Duration{Duration: 180 * 24 * time.Hour},
			},
		},
		Cookie:   config.Cookie{Enabled: true, Name: "ga_refresh", Path: api + "/auth", Secure: true, SameSite: "Strict"},
		Database: config.Database{Driver: "sqlite", Name: ":memory:", MaxOpenConns: 1},
		Security: config.Security{
			BcryptCost: 10, PasswordMinLength: 8,
			LoginRateLimit: 1000, LoginRateWindow: config.Duration{Duration: time.Minute},
			MaxFailedLogins: 3, LockoutDuration: config.Duration{Duration: 15 * time.Minute},
		},
		Account: config.Account{DefaultCanChangeEmail: true, DefaultCanChangePassword: true},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	db, err := database.Open(ctx, cfg.Database, func(s string) string { return s })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil { // idempotent
		t.Fatal(err)
	}
	svc, err := auth.NewService(cfg, store.NewUsers(db), store.NewSessions(db), policy)
	if err != nil {
		t.Fatal(err)
	}
	str := func(s string) *string { return &s }
	no := false
	for _, in := range []auth.UserInput{
		{Username: "root", Password: "RootPass123", Role: "superadmin"},
		{Username: "admin", Email: str("admin@example.com"), Password: "AdminPass123", Role: "admin"},
		{Username: "admin2", Password: "AdminPass123", Role: "admin"},
		{Username: "Sales01", Email: str("Sales01@Example.com"), Code: str("S001"), Password: "SalesPass123", Role: "sales"},
		{Username: "viewer01", Password: "ViewerPass123", Role: "viewer", CanChangePassword: &no},
	} {
		if _, err := svc.CreateUser(ctx, nil, in); err != nil {
			t.Fatalf("create %s: %v", in.Username, err)
		}
	}
	if _, err := svc.CreateUser(ctx, nil, auth.UserInput{Username: "dup", Code: str("S001"), Password: "whatever123", Role: "viewer"}); err == nil {
		t.Fatal("expected duplicate code to be rejected")
	}

	srv := server.New(cfg, svc, policy, map[string]*d365.Connector{"fo": conn})
	return &env{t: t, srv: srv, svc: svc}, &tokenCalls
}

type tokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	RefreshExp   time.Time `json:"refresh_expires_at"`
	User         struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
}

func (e *env) login(body map[string]string) (tokens, reply) {
	e.t.Helper()
	r := e.req("POST", api+"/auth/login", "", body).expect(e.t, "login "+body["identifier"], 200)
	var tk tokens
	json.Unmarshal(r.env.Data, &tk)
	return tk, r
}

func TestWebCookieFlow(t *testing.T) {
	e, _ := setup(t)

	e.req("POST", api+"/auth/login", "", map[string]string{"identifier": "sales01", "password": "nope-nope"}).
		expect(t, "bad password", 401, "invalid_credentials")

	// Web: refresh token only in an HttpOnly cookie, never in the body.
	tk, r := e.login(map[string]string{"identifier": "SALES01@example.com", "password": "SalesPass123"})
	ck := r.cookie("ga_refresh")
	if tk.RefreshToken != "" || ck == nil || !ck.HttpOnly || !ck.Secure || ck.Path != api+"/auth" {
		t.Fatalf("web refresh token must be an HttpOnly cookie only: body=%q cookie=%+v", tk.RefreshToken, ck)
	}
	e.req("GET", api+"/me", tk.AccessToken, nil).expect(t, "me", 200)

	cookieHdr := "ga_refresh=" + ck.Value
	e.req("POST", api+"/auth/refresh", "", nil, "Cookie", cookieHdr).
		expect(t, "cookie refresh without X-Requested-With", 400)
	r = e.req("POST", api+"/auth/refresh", "", nil, "Cookie", cookieHdr, "X-Requested-With", "fetch").expect(t, "refresh", 200)
	ck2 := r.cookie("ga_refresh")
	if ck2 == nil || ck2.Value == ck.Value {
		t.Fatal("refresh must rotate the cookie")
	}

	// Replaying the old refresh token revokes the whole session.
	e.req("POST", api+"/auth/refresh", "", nil, "Cookie", cookieHdr, "X-Requested-With", "fetch").
		expect(t, "reuse", 401, "refresh_token_reused")
	e.req("GET", api+"/me", tk.AccessToken, nil).expect(t, "access after reuse", 401, "session_ended")
}

func TestMobileFlow(t *testing.T) {
	e, _ := setup(t)

	// Mobile: same token format, refresh token in the body, long lifetime.
	m, r := e.login(map[string]string{"code": "S001", "password": "SalesPass123", "client": "mobile"})
	if m.RefreshToken == "" || r.cookie("ga_refresh") != nil {
		t.Fatal("mobile must receive the refresh token in the body, not a cookie")
	}
	if until := time.Until(m.RefreshExp); until < 89*24*time.Hour {
		t.Fatalf("mobile refresh expires too soon: %v", until)
	}
	r = e.req("POST", api+"/auth/refresh", "", map[string]string{"refresh_token": m.RefreshToken}).expect(t, "mobile refresh", 200)
	var m2 tokens
	json.Unmarshal(r.env.Data, &m2)
	if m2.RefreshToken == "" || m2.RefreshToken == m.RefreshToken {
		t.Fatal("mobile refresh must rotate the refresh token")
	}
	// A refresh token is never accepted as an access token.
	e.req("GET", api+"/me", m2.RefreshToken, nil).expect(t, "refresh as access", 401)

	// rbac [clients] mobile = ["sales"]
	e.req("POST", api+"/auth/login", "", map[string]string{"identifier": "viewer01", "password": "ViewerPass123", "client": "mobile"}).
		expect(t, "viewer on mobile", 403, "forbidden")

	// Logout with only the refresh token (access token may have expired).
	e.req("POST", api+"/auth/logout", "", map[string]string{"refresh_token": m2.RefreshToken}).expect(t, "logout", 200)
	e.req("GET", api+"/me", m2.AccessToken, nil).expect(t, "after logout", 401, "session_ended")
}

func TestLockout(t *testing.T) {
	e, _ := setup(t)
	for i := 0; i < 3; i++ {
		e.req("POST", api+"/auth/login", "", map[string]string{"identifier": "viewer01", "password": "wrong-pass"}).expect(t, "fail", 401)
	}
	r := e.req("POST", api+"/auth/login", "", map[string]string{"identifier": "viewer01", "password": "ViewerPass123"}).
		expect(t, "locked", 429, "account_locked")
	if r.resp.Header.Get("Retry-After") == "" {
		t.Fatal("Retry-After header missing")
	}
	a, _ := e.login(map[string]string{"identifier": "admin", "password": "AdminPass123"})
	r = e.req("GET", api+"/admin/users?q=viewer", a.AccessToken, nil).expect(t, "find viewer", 200)
	var list struct {
		Items []struct{ ID string }
		Total int
	}
	json.Unmarshal(r.env.Data, &list)
	if list.Total != 1 {
		t.Fatalf("search total %d", list.Total)
	}
	e.req("POST", api+"/admin/users/"+list.Items[0].ID+"/unlock", a.AccessToken, nil).expect(t, "unlock", 200)
	e.login(map[string]string{"identifier": "viewer01", "password": "ViewerPass123"})
}

func TestSelfService(t *testing.T) {
	e, _ := setup(t)
	s, _ := e.login(map[string]string{"identifier": "sales01", "password": "SalesPass123"})
	other, _ := e.login(map[string]string{"identifier": "sales01", "password": "SalesPass123", "client": "mobile"})

	// can_change_username defaults to false (config [account]).
	e.req("PUT", api+"/me/username", s.AccessToken, map[string]string{"current_password": "SalesPass123", "username": "sales-x"}).
		expect(t, "username not allowed", 403, "forbidden")
	e.req("PUT", api+"/me/email", s.AccessToken, map[string]string{"current_password": "wrong-pass", "email": "x@example.com"}).
		expect(t, "email wrong password", 400, "validation_error")
	e.req("PUT", api+"/me/email", s.AccessToken, map[string]string{"current_password": "SalesPass123", "email": "admin@example.com"}).
		expect(t, "email taken", 409, "conflict")
	e.req("PUT", api+"/me/email", s.AccessToken, map[string]string{"current_password": "SalesPass123", "email": "New@Example.com"}).
		expect(t, "email changed", 200)

	r := e.req("GET", api+"/me/sessions", s.AccessToken, nil).expect(t, "sessions", 200)
	var sess struct{ Items []struct{ ID string } }
	json.Unmarshal(r.env.Data, &sess)
	if len(sess.Items) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(sess.Items))
	}

	// Password change logs out every other device.
	e.req("PUT", api+"/me/password", s.AccessToken, map[string]string{"current_password": "SalesPass123", "new_password": "NewSales456"}).
		expect(t, "password changed", 200)
	e.req("GET", api+"/me", other.AccessToken, nil).expect(t, "other device", 401, "session_ended")
	e.req("GET", api+"/me", s.AccessToken, nil).expect(t, "current device", 200)
	e.login(map[string]string{"identifier": "new@example.com", "password": "NewSales456"})

	v, _ := e.login(map[string]string{"identifier": "viewer01", "password": "ViewerPass123"})
	e.req("PUT", api+"/me/password", v.AccessToken, map[string]string{"current_password": "ViewerPass123", "new_password": "Another123"}).
		expect(t, "viewer may not change password", 403, "forbidden")
}

func TestAdminProtection(t *testing.T) {
	e, _ := setup(t)
	ids := map[string]string{}
	login := func(user, pass, client string) tokens {
		tk, _ := e.login(map[string]string{"identifier": user, "password": pass, "client": client})
		ids[user] = tk.User.ID
		return tk
	}
	root := login("root", "RootPass123", "web")
	adm := login("admin", "AdminPass123", "web")
	adm2 := login("admin2", "AdminPass123", "web")
	sw := login("sales01", "SalesPass123", "web")
	sm := login("sales01", "SalesPass123", "mobile")
	login("viewer01", "ViewerPass123", "web")

	e.req("GET", api+"/admin/users", sm.AccessToken, nil).expect(t, "sales on admin", 403)

	// Deactivating a user logs them out on web and mobile immediately.
	e.req("POST", api+"/admin/users/"+ids["sales01"]+"/deactivate", adm.AccessToken, nil).expect(t, "deactivate", 200)
	e.req("GET", api+"/me", sw.AccessToken, nil).expect(t, "web after deactivate", 401)
	e.req("GET", api+"/me", sm.AccessToken, nil).expect(t, "mobile after deactivate", 401)
	e.req("POST", api+"/auth/login", "", map[string]string{"identifier": "sales01", "password": "SalesPass123"}).
		expect(t, "login while inactive", 403, "user_inactive")
	e.req("POST", api+"/admin/users/"+ids["sales01"]+"/activate", adm.AccessToken, nil).expect(t, "activate", 200)
	sm = login("sales01", "SalesPass123", "mobile")

	// Force logout everywhere.
	r := e.req("POST", api+"/admin/users/"+ids["sales01"]+"/revoke-sessions", adm.AccessToken, nil).expect(t, "revoke", 200)
	if !strings.Contains(string(r.raw), `"revoked_sessions":1`) {
		t.Fatalf("unexpected revoke result %s", r.raw)
	}
	e.req("GET", api+"/me", sm.AccessToken, nil).expect(t, "after revoke", 401)

	// Protected accounts.
	e.req("POST", api+"/admin/users/"+ids["admin2"]+"/revoke-sessions", adm.AccessToken, nil).expect(t, "admin revokes admin", 403)
	e.req("POST", api+"/admin/users/"+ids["root"]+"/deactivate", adm.AccessToken, nil).expect(t, "admin deactivates superadmin", 403)
	e.req("POST", api+"/admin/users/"+ids["admin"]+"/deactivate", adm.AccessToken, nil).expect(t, "self via admin api", 403)
	e.req("PATCH", api+"/admin/users/"+ids["viewer01"], adm.AccessToken, map[string]string{"role": "admin"}).expect(t, "admin grants admin", 403)
	e.req("POST", api+"/admin/users", adm.AccessToken, map[string]string{"username": "evil", "password": "EvilPass123", "role": "superadmin"}).
		expect(t, "admin creates superadmin", 403)
	e.req("POST", api+"/admin/users/"+ids["root"]+"/revoke-sessions", root.AccessToken, nil).expect(t, "superadmin on self", 403)
	e.req("POST", api+"/admin/users/"+ids["admin2"]+"/revoke-sessions", root.AccessToken, nil).expect(t, "superadmin revokes admin", 200)
	e.req("GET", api+"/me", adm2.AccessToken, nil).expect(t, "admin2 after revoke", 401)

	// Admin edits a normal user, including the self-service flags.
	e.req("PATCH", api+"/admin/users/"+ids["sales01"], adm.AccessToken, map[string]any{"can_change_username": true, "name": "Sales One"}).
		expect(t, "patch flags", 200)
	s, _ := e.login(map[string]string{"identifier": "sales01", "password": "SalesPass123"})
	e.req("PUT", api+"/me/username", s.AccessToken, map[string]string{"current_password": "SalesPass123", "username": "sales-one"}).
		expect(t, "username now allowed", 200)
}

func TestD365Proxy(t *testing.T) {
	e, tokenCalls := setup(t)
	m, _ := e.login(map[string]string{"identifier": "sales01", "password": "SalesPass123", "client": "mobile"})

	r := e.req("GET", api+"/d365/fo/customer(dataAreaId='usmf',CustomerAccount='US-001')?$select=Name&cross-company=true", m.AccessToken, nil).
		expect(t, "read", 200)
	var echo map[string]string
	json.Unmarshal(r.raw, &echo)
	if echo["path"] != "/data/CustomersV3(dataAreaId='usmf',CustomerAccount='US-001')" || echo["auth"] != "Bearer aad-token" {
		t.Fatalf("unexpected forwarded request %v", echo)
	}
	// Client tried cross-company=true; forced_query wins. No $top on a keyed read.
	if echo["query"] != "$select=Name&cross-company=false" {
		t.Fatalf("unexpected forwarded query %q", echo["query"])
	}
	r = e.req("GET", api+"/d365/fo/customer?$filter=Name%20eq%20'A'", m.AccessToken, nil).expect(t, "read collection", 200)
	json.Unmarshal(r.raw, &echo)
	if echo["query"] != "$filter=Name%20eq%20'A'&%24top=100&cross-company=false" {
		t.Fatalf("collection read should get default $top: %q", echo["query"])
	}
	if n := atomic.LoadInt32(tokenCalls); n != 1 {
		t.Fatalf("Entra token should be cached, fetched %d times", n)
	}

	e.req("POST", api+"/d365/fo/customer", m.AccessToken, map[string]string{"x": "1"}).expect(t, "sales create customer", 403)
	e.req("DELETE", api+"/d365/fo/customer('1')", m.AccessToken, nil).expect(t, "delete falls to *.*", 403)
	r = e.req("PATCH", api+"/d365/fo/so_header('SO1')", m.AccessToken, map[string]string{"x": "y"}).expect(t, "update so", 200)
	json.Unmarshal(r.raw, &echo)
	if echo["if_match"] != "*" || echo["query"] != "cross-company=false" {
		t.Fatalf("If-Match default missing: %v", echo)
	}
	e.req("GET", api+"/d365/fo/customer('1')/../SalesOrderHeadersV2", m.AccessToken, nil).expect(t, "path escape", 400)
	e.req("GET", api+"/d365/fo/vendor", m.AccessToken, nil).expect(t, "unmapped", 404)
}
