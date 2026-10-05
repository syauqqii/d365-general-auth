package config

import (
	"os"
	"regexp"
	"strings"
)

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// ExpandEnv replaces ${VAR} and ${VAR:-default} references in a TOML
// document before it is parsed. The default is used when VAR is unset or
// empty. Values are escaped so they stay valid inside double-quoted strings.
func ExpandEnv(src []byte) []byte {
	return envRef.ReplaceAllFunc(src, func(m []byte) []byte {
		parts := envRef.FindSubmatch(m)
		val, ok := os.LookupEnv(string(parts[1]))
		if !ok || val == "" {
			val = string(parts[2])
		}
		return []byte(tomlEscape(val))
	})
}

var tomlEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)

func tomlEscape(s string) string { return tomlEscaper.Replace(s) }
