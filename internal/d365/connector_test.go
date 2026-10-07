package d365

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const creds = `
d365_tenant_id = "tenant-1"
d365_client_id = "client-1"
d365_client_secret = "secret"
`

func load(t *testing.T, file, body string) (*Connector, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), file)
	if err := os.WriteFile(path, []byte(body+creds), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestConnectorBuildsURLAndScope(t *testing.T) {
	cases := []struct {
		file, body, name, apiURL, scope string
	}{
		{
			"connector-bc.toml", `instance = "bc"
company = "CRONUS International Ltd."`,
			"bc",
			"https://api.businesscentral.dynamics.com/v2.0/tenant-1/Production/ODataV4/Company('CRONUS%20International%20Ltd.')/%s",
			"https://api.businesscentral.dynamics.com/.default",
		},
		{
			// Already-encoded names and a quote in the name.
			"connector-bc-sandbox.toml", `instance = "bc"
environment = "Sandbox"
company = "Bob%20's Shop"`,
			"bc-sandbox",
			"https://api.businesscentral.dynamics.com/v2.0/tenant-1/Sandbox/ODataV4/Company('Bob%20%27%27s%20Shop')/%s",
			"https://api.businesscentral.dynamics.com/.default",
		},
		{
			"connector-pusat.toml", `instance = "FO"
host = "https://contoso.operations.dynamics.com/"`,
			"pusat",
			"https://contoso.operations.dynamics.com/data/%s",
			"https://contoso.operations.dynamics.com/.default",
		},
		{
			"connector-x.toml", `instance = "fo"
name = "custom"
api_url = "https://contoso.operations.dynamics.com/data/%s"`,
			"custom",
			"https://contoso.operations.dynamics.com/data/%s",
			"https://contoso.operations.dynamics.com/.default",
		},
	}
	for _, tc := range cases {
		c, err := load(t, tc.file, tc.body)
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		if c.Name != tc.name || c.APIURL != tc.apiURL || c.Scope != tc.scope {
			t.Errorf("%s:\n name   %q want %q\n url    %q want %q\n scope  %q want %q",
				tc.file, c.Name, tc.name, c.APIURL, tc.apiURL, c.Scope, tc.scope)
		}
	}
}

func TestConnectorConfigErrors(t *testing.T) {
	cases := map[string]string{
		`instance = "bc"`: "company is required",
		`instance = "fo"`: "host is required",
		"instance = \"fo\"\ncompany = \"X\"\nhost = \"h\"":                 "environment and company are for",
		"instance = \"bc\"\nhost = \"h\"\ncompany = \"X\"":                 "host is for",
		"instance = \"fo\"\nhost = \"h\"\napi_url = \"https://h/data/%s\"": "not both",
		`instance = "nav"`: `instance must be "bc" or "fo"`,
	}
	for body, want := range cases {
		_, err := load(t, "connector-t.toml", body)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want error containing %q", body, err, want)
		}
	}
}
