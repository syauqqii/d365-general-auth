package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"general-auth/internal/config"
)

// Kind separates access and refresh tokens. Each is signed with its own
// secret, so one can never be accepted in place of the other.
type Kind string

const (
	KindAccess  Kind = "access"
	KindRefresh Kind = "refresh"
)

type Claims struct {
	jwt.RegisteredClaims
	Kind      Kind   `json:"typ"`
	SessionID string `json:"sid"`
	Client    Client `json:"cli"`
	Role      string `json:"role,omitempty"`
}

type Tokens struct {
	issuer   string
	audience string
	secrets  map[Kind][]byte
}

func NewTokens(cfg config.JWT) *Tokens {
	return &Tokens{
		issuer:   cfg.Issuer,
		audience: cfg.Audience,
		secrets: map[Kind][]byte{
			KindAccess:  []byte(cfg.Access.Secret),
			KindRefresh: []byte(cfg.Refresh.Secret),
		},
	}
}

func (t *Tokens) Issue(kind Kind, c Claims, now, exp time.Time) (string, error) {
	c.Kind = kind
	c.Issuer = t.issuer
	c.Audience = jwt.ClaimStrings{t.audience}
	c.IssuedAt = jwt.NewNumericDate(now)
	c.NotBefore = jwt.NewNumericDate(now)
	c.ExpiresAt = jwt.NewNumericDate(exp)
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(t.secrets[kind])
	if err != nil {
		return "", fmt.Errorf("sign %s token: %w", kind, err)
	}
	return s, nil
}

func (t *Tokens) Parse(kind Kind, raw string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(raw, claims,
		func(*jwt.Token) (any, error) { return t.secrets[kind], nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(t.issuer),
		jwt.WithAudience(t.audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(30*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if claims.Kind != kind || claims.SessionID == "" || claims.Subject == "" || claims.ID == "" {
		return nil, ErrInvalidToken
	}
	return claims, nil
}
