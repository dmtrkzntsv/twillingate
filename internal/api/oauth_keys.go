package api

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Every value the token:// login server hands out is an HS256 JWT under a
// key derived from the token and the password (login spec §8). Nothing is
// stored: a restart derives the same keys, and changing either secret
// invalidates everything at once.
//
// HMAC is right here because the server only verifies what it signed
// itself; verifyJWT's asymmetric-only allowlist guards a different path.

type kind string

const (
	kindClient  kind = "client"
	kindForm    kind = "form"
	kindCode    kind = "code"
	kindAccess  kind = "access"
	kindRefresh kind = "refresh"
)

// loginKeys holds one key per kind, so a value of one kind never verifies
// as another even when its claims would fit.
type loginKeys map[kind][]byte

func deriveLoginKeys(token, password string) loginKeys {
	// Length prefixes keep ("ab", "c") and ("a", "bc") apart.
	secret := binary.BigEndian.AppendUint32(nil, uint32(len(token)))
	secret = append(secret, token...)
	secret = binary.BigEndian.AppendUint32(secret, uint32(len(password)))
	secret = append(secret, password...)
	keys := loginKeys{}
	for _, kd := range []kind{kindClient, kindForm, kindCode, kindAccess, kindRefresh} {
		key, err := hkdf.Key(sha256.New, secret, nil, "twillingate mcp "+string(kd), 32)
		if err != nil {
			panic(err) // only for key lengths beyond 255 hash blocks
		}
		keys[kd] = key
	}
	return keys
}

func (k loginKeys) sign(kd kind, claims jwt.Claims) string {
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(k[kd])
	if err != nil {
		panic(err) // HS256 over marshalable claims with a []byte key cannot fail
	}
	return s
}

// parse verifies raw as a kd value into claims. HS256 is the only accepted
// algorithm; now is the clock for exp.
func (k loginKeys) parse(kd kind, raw string, claims jwt.Claims, now func() time.Time, opts ...jwt.ParserOption) error {
	opts = append(opts, jwt.WithValidMethods([]string{"HS256"}), jwt.WithTimeFunc(now))
	_, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return k[kd], nil }, opts...)
	return err
}

// clientClaims is a client_id: the registration, signed.
type clientClaims struct {
	RedirectURIs []string `json:"redirect_uris"`
	ClientName   string   `json:"client_name,omitempty"`
	jwt.RegisteredClaims
}

// formClaims is the login page's hidden request field.
type formClaims struct {
	ClientID      string `json:"client_id"`
	ClientName    string `json:"client_name,omitempty"`
	RedirectURI   string `json:"redirect_uri"`
	CodeChallenge string `json:"code_challenge"`
	State         string `json:"state,omitempty"`
	jwt.RegisteredClaims
}

// codeClaims is an authorization code.
type codeClaims struct {
	ClientID      string `json:"client_id"`
	RedirectURI   string `json:"redirect_uri"`
	CodeChallenge string `json:"code_challenge"`
	jwt.RegisteredClaims
}

// grantClaims is an access or a refresh token.
type grantClaims struct {
	ClientID string `json:"client_id"`
	jwt.RegisteredClaims
}
