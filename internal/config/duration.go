package config

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Duration is a time.Duration that can be decoded from TOML strings such as
// "15m", "24h", "7d", "2w" or "1d12h".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

var dayWeek = regexp.MustCompile(`(\d+(?:\.\d+)?)([dw])`)

// ParseDuration extends time.ParseDuration with "d" (24h) and "w" (7d) units.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty duration")
	}
	var extra time.Duration
	var convErr error
	rest := dayWeek.ReplaceAllStringFunc(s, func(m string) string {
		parts := dayWeek.FindStringSubmatch(m)
		n, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			convErr = err
			return ""
		}
		unit := 24 * time.Hour
		if parts[2] == "w" {
			unit = 7 * 24 * time.Hour
		}
		extra += time.Duration(n * float64(unit))
		return ""
	})
	if convErr != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, convErr)
	}
	if rest == "" {
		return extra, nil
	}
	v, err := time.ParseDuration(rest)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (use e.g. 15m, 24h, 7d, 2w)", s)
	}
	return v + extra, nil
}
