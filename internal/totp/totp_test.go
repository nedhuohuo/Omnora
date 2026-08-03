package totp

import (
	"encoding/base32"
	"net/url"
	"strings"
	"testing"
	"time"
)

const rfc6238SHA1Secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func TestGenerateCodeRFC6238SHA1Vectors(t *testing.T) {
	tests := []struct {
		unix int64
		want string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}

	for _, tt := range tests {
		got, err := GenerateCode(rfc6238SHA1Secret, time.Unix(tt.unix, 0), Config{Digits: 8})
		if err != nil {
			t.Fatalf("GenerateCode(%d) error = %v", tt.unix, err)
		}
		if got != tt.want {
			t.Fatalf("GenerateCode(%d) = %q, want %q", tt.unix, got, tt.want)
		}
	}
}

func TestVerifyDefaultSixDigitsWithOneStepWindow(t *testing.T) {
	now := time.Unix(59, 0)
	code, err := GenerateCode(rfc6238SHA1Secret, now, Config{})
	if err != nil {
		t.Fatalf("GenerateCode() error = %v", err)
	}
	if code != "287082" {
		t.Fatalf("default six-digit code = %q, want 287082", code)
	}
	if !Verify(rfc6238SHA1Secret, code, now.Add(30*time.Second)) {
		t.Fatal("Verify() rejected adjacent time step")
	}
	if Verify(rfc6238SHA1Secret, code, now.Add(90*time.Second)) {
		t.Fatal("Verify() accepted code outside default skew")
	}
	if Verify(rfc6238SHA1Secret, "28708x", now) {
		t.Fatal("Verify() accepted non-numeric code")
	}
}

func TestGenerateSecretIsBase32NoPadding(t *testing.T) {
	secret, err := GenerateSecretBytes(32)
	if err != nil {
		t.Fatalf("GenerateSecretBytes() error = %v", err)
	}
	if strings.Contains(secret, "=") {
		t.Fatalf("secret = %q, want no padding", secret)
	}
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatalf("DecodeString(secret) error = %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("decoded secret length = %d, want 32", len(decoded))
	}
}

func TestOTPAuthURI(t *testing.T) {
	uri, err := OTPAuthURI(URIOptions{
		Issuer:      "Omnora",
		AccountName: "ada@example.test",
		Secret:      rfc6238SHA1Secret,
	})
	if err != nil {
		t.Fatalf("OTPAuthURI() error = %v", err)
	}

	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", uri, err)
	}
	if parsed.Scheme != "otpauth" || parsed.Host != "totp" {
		t.Fatalf("URI authority = %s://%s, want otpauth://totp", parsed.Scheme, parsed.Host)
	}
	if parsed.Path != "/Omnora:ada@example.test" {
		t.Fatalf("URI path = %q, want label", parsed.Path)
	}

	values := parsed.Query()
	for key, want := range map[string]string{
		"secret":    rfc6238SHA1Secret,
		"issuer":    "Omnora",
		"algorithm": "SHA1",
		"digits":    "6",
		"period":    "30",
	} {
		if got := values.Get(key); got != want {
			t.Fatalf("query[%s] = %q, want %q", key, got, want)
		}
	}
}
