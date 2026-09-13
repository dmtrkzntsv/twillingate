package mcpserver

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var loginEpoch = time.Date(2026, 9, 12, 12, 0, 30, 0, time.UTC)

func fixedNow() time.Time { return loginEpoch }

func TestDeriveLoginKeys(t *testing.T) {
	a := deriveLoginKeys("ar_token", "pw")
	b := deriveLoginKeys("ar_token", "pw")
	for _, kd := range []kind{kindClient, kindForm, kindCode, kindAccess, kindRefresh} {
		if len(a[kd]) != 32 {
			t.Fatalf("%s key length = %d, want 32", kd, len(a[kd]))
		}
		if string(a[kd]) != string(b[kd]) {
			t.Errorf("%s key differs between derivations; a restart would log everyone out", kd)
		}
	}
	if string(a[kindCode]) == string(a[kindAccess]) {
		t.Error("code and access keys are equal; one kind could pass as another")
	}
	for name, other := range map[string]loginKeys{
		"token changed":    deriveLoginKeys("ar_other", "pw"),
		"password changed": deriveLoginKeys("ar_token", "pw2"),
		"bytes shifted":    deriveLoginKeys("ar_tokenp", "w"),
	} {
		if string(other[kindAccess]) == string(a[kindAccess]) {
			t.Errorf("%s: access key unchanged", name)
		}
	}
}

func TestSignedValuesRoundTrip(t *testing.T) {
	k := deriveLoginKeys("ar_token", "pw")
	raw := k.sign(kindCode, codeClaims{ClientID: "c1", RedirectURI: "https://claude.ai/cb", CodeChallenge: "ch",
		RegisteredClaims: jwt.RegisteredClaims{ID: "j1", ExpiresAt: jwt.NewNumericDate(loginEpoch.Add(time.Minute))}})
	var got codeClaims
	if err := k.parse(kindCode, raw, &got, fixedNow, jwt.WithExpirationRequired()); err != nil {
		t.Fatal(err)
	}
	if got.ClientID != "c1" || got.RedirectURI != "https://claude.ai/cb" || got.CodeChallenge != "ch" || got.ID != "j1" {
		t.Errorf("claims = %+v", got)
	}
}

func TestSignedValuesRejected(t *testing.T) {
	k := deriveLoginKeys("ar_token", "pw")
	valid := jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(loginEpoch.Add(time.Minute))}
	code := k.sign(kindCode, codeClaims{RegisteredClaims: valid})

	none, err := jwt.NewWithClaims(jwt.SigningMethodNone, valid).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	hs512, err := jwt.NewWithClaims(jwt.SigningMethodHS512, valid).SignedString(k[kindAccess])
	if err != nil {
		t.Fatal(err)
	}
	expired := k.sign(kindAccess, grantClaims{RegisteredClaims: jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(loginEpoch.Add(-time.Second))}})
	noExp := k.sign(kindAccess, grantClaims{})

	cases := map[string]string{
		"code presented as access": code,
		"alg none":                 none,
		"HS512":                    hs512,
		"expired":                  expired,
		"missing exp":              noExp,
		"garbage":                  "ar_token",
	}
	for name, raw := range cases {
		var c grantClaims
		if err := k.parse(kindAccess, raw, &c, fixedNow, jwt.WithExpirationRequired()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := deriveLoginKeys("ar_token", "new").parse(kindCode, code, &codeClaims{}, fixedNow); err == nil {
		t.Error("value signed before a password change still verifies")
	}
}
