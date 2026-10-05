package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Int is an integer that may also be written as a string in TOML, so a value
// like port = "${APP_PORT:-3000}" stays valid TOML before env expansion.
type Int int

func (i *Int) UnmarshalTOML(v any) error {
	switch x := v.(type) {
	case int64:
		*i = Int(x)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return fmt.Errorf("expected an integer, got %q", x)
		}
		*i = Int(n)
	default:
		return fmt.Errorf("expected an integer, got %T", v)
	}
	return nil
}
