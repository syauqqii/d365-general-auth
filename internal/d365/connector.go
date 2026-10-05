// Package d365 talks to Dynamics 365 Business Central / Finance & Operations
// on behalf of authenticated users. The Azure AD client secret and the D365
// access token never leave this backend.
package d365

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"general-auth/internal/config"
)

// Connector is one D365 environment, loaded from a connector-*.toml file.
type Connector struct {
	Name         string            `toml:"-"`
	Product      string            `toml:"d365_product"`
	TenantID     string            `toml:"d365_tenant_id"`
	ClientID     string            `toml:"d365_client_id"`
	ClientSecret string            `toml:"d365_client_secret"`
	GrantType    string            `toml:"d365_grant_type"`
	Scope        string            `toml:"d365_scope"`
	AuthURL      string            `toml:"d365_auth_url"`
	APIURL       string            `toml:"api_url"`
	Timeout      config.Duration   `toml:"timeout"`
	Endpoints    map[string]string `toml:"endpoints"`
	// DefaultQuery is added to collection reads (GET without a key) when
	// the client did not send that parameter;
	// ForcedQuery always replaces whatever the client sent.
	DefaultQuery map[string]string `toml:"default_query"`
	ForcedQuery  map[string]string `toml:"forced_query"`

	client    *http.Client
	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// Load reads a connector file. ${VAR} references are expanded, so the
// client secret can come from the environment.
func Load(name, path string) (*Connector, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("connector %s: %w", name, err)
	}
	c := &Connector{Name: name}
	md, err := toml.Decode(string(config.ExpandEnv(raw)), c)
	if err != nil {
		return nil, fmt.Errorf("connector %s: parse %s: %w", name, path, err)
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		return nil, fmt.Errorf("connector %s: unknown keys %v in %s", name, keys, path)
	}

	c.Product = strings.ToLower(strings.TrimSpace(c.Product))
	if c.Product == "" {
		c.Product = "bc"
	}
	if c.GrantType == "" {
		c.GrantType = "client_credentials"
	}
	if c.Timeout.Duration <= 0 {
		c.Timeout.Duration = 30 * time.Second
	}

	var errs []error
	if c.Product != "bc" && c.Product != "fo" {
		errs = append(errs, fmt.Errorf("d365_product must be \"bc\" or \"fo\", got %q", c.Product))
	}
	for k, v := range map[string]string{
		"d365_tenant_id": c.TenantID, "d365_client_id": c.ClientID, "d365_client_secret": c.ClientSecret,
		"d365_scope": c.Scope, "d365_auth_url": c.AuthURL, "api_url": c.APIURL,
	} {
		if strings.TrimSpace(v) == "" {
			errs = append(errs, fmt.Errorf("%s is required", k))
		}
	}
	if c.APIURL != "" && !strings.Contains(c.APIURL, "%s") {
		errs = append(errs, errors.New("api_url must contain %s where the endpoint name goes"))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("connector %s (%s): %w", name, path, err)
	}

	c.client = &http.Client{Timeout: c.Timeout.Duration}
	return c, nil
}

// Entities returns the configured entity names, sorted.
func (c *Connector) Entities() []string {
	out := make([]string, 0, len(c.Endpoints))
	for k := range c.Endpoints {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// accessToken returns a cached Azure AD token, fetching a new one when it is
// missing, about to expire, or force is set. The mutex makes concurrent
// callers wait for a single fetch.
func (c *Connector) accessToken(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.token != "" && time.Until(c.expiresAt) > time.Minute {
		return c.token, nil
	}

	form := url.Values{
		"grant_type":    {c.GrantType},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
		"scope":         {c.Scope},
	}
	authURL := strings.Replace(c.AuthURL, "%s", url.PathEscape(c.TenantID), 1)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("d365 token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("d365 token request: %s: %s", resp.Status, body)
	}

	var tr struct {
		AccessToken string      `json:"access_token"`
		ExpiresIn   json.Number `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil || tr.AccessToken == "" {
		return "", fmt.Errorf("d365 token response not understood: %s", body)
	}
	secs, _ := tr.ExpiresIn.Int64()
	if secs <= 0 {
		secs = 3600
	}
	c.token = tr.AccessToken
	c.expiresAt = time.Now().Add(time.Duration(secs) * time.Second)
	return c.token, nil
}

// Do sends a request to <api_url with %s = endpoint+keySuffix>?rawQuery. If
// D365 answers 401 the token is refreshed and the request retried once.
func (c *Connector) Do(ctx context.Context, method, endpoint, keySuffix, rawQuery string, body []byte, header http.Header) (*http.Response, error) {
	target := strings.Replace(c.APIURL, "%s", endpoint+keySuffix, 1)
	// OData rejects collection options such as $top on a single record or a
	// write, so defaults only apply to collection reads.
	defaults := c.DefaultQuery
	if method != http.MethodGet || keySuffix != "" {
		defaults = nil
	}
	rawQuery = mergeQuery(rawQuery, defaults, c.ForcedQuery)
	if rawQuery != "" {
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		target += sep + rawQuery
	}

	for attempt := 0; ; attempt++ {
		tok, err := c.accessToken(ctx, attempt > 0)
		if err != nil {
			return nil, err
		}
		var rdr io.Reader
		if len(body) > 0 {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, target, rdr)
		if err != nil {
			return nil, err
		}
		for k, v := range header {
			req.Header[k] = v
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if req.Header.Get("Accept") == "" {
			req.Header.Set("Accept", "application/json")
		}
		resp, err := c.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("d365 %s request: %w", c.Name, err)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			continue
		}
		return resp, nil
	}
}

// LoadAll loads every connector listed in config [connectors].
func LoadAll(cfg *config.Config) (map[string]*Connector, error) {
	out := make(map[string]*Connector, len(cfg.Connectors))
	for name, path := range cfg.Connectors {
		c, err := Load(name, cfg.Resolve(path))
		if err != nil {
			return nil, err
		}
		out[name] = c
	}
	return out, nil
}

// mergeQuery applies default and forced parameters to a raw query string
// without re-encoding the client's own parameters (OData filters are
// sensitive to how spaces and quotes are encoded).
func mergeQuery(raw string, defaults, forced map[string]string) string {
	var parts []string
	present := map[string]bool{}
	if raw != "" {
		for _, p := range strings.Split(raw, "&") {
			if p == "" {
				continue
			}
			k, _, _ := strings.Cut(p, "=")
			if key, err := url.QueryUnescape(k); err == nil {
				k = key
			}
			k = strings.ToLower(k)
			if hasKeyFold(forced, k) {
				continue
			}
			present[k] = true
			parts = append(parts, p)
		}
	}
	add := func(m map[string]string, skipPresent bool) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if skipPresent && present[strings.ToLower(k)] {
				continue
			}
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(m[k]))
		}
	}
	add(defaults, true)
	add(forced, false)
	return strings.Join(parts, "&")
}

func hasKeyFold(m map[string]string, k string) bool {
	for key := range m {
		if strings.EqualFold(key, k) {
			return true
		}
	}
	return false
}
