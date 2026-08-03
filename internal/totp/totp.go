package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultDigits = 6
	DefaultPeriod = 30 * time.Second
	DefaultSkew   = 1

	defaultSecretBytes = 20
)

var (
	ErrInvalidSecret = errors.New("totp: invalid secret")
	ErrInvalidCode   = errors.New("totp: invalid code")
	ErrInvalidConfig = errors.New("totp: invalid config")
)

type Config struct {
	Digits int
	Period time.Duration
	Skew   int
}

type URIOptions struct {
	Issuer      string
	AccountName string
	Secret      string
	Config      Config
}

func GenerateSecret() (string, error) {
	return GenerateSecretBytes(defaultSecretBytes)
}

func GenerateSecretBytes(size int) (string, error) {
	if size <= 0 {
		return "", ErrInvalidConfig
	}
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}

func OTPAuthURI(opts URIOptions) (string, error) {
	issuer := strings.TrimSpace(opts.Issuer)
	account := strings.TrimSpace(opts.AccountName)
	secret := normalizeSecret(opts.Secret)
	if issuer == "" || account == "" || secret == "" {
		return "", ErrInvalidConfig
	}
	if _, err := decodeSecret(secret); err != nil {
		return "", err
	}

	cfg, err := normalizeConfig(opts.Config)
	if err != nil {
		return "", err
	}

	values := url.Values{}
	values.Set("secret", secret)
	values.Set("issuer", issuer)
	values.Set("algorithm", "SHA1")
	values.Set("digits", strconv.Itoa(cfg.Digits))
	values.Set("period", strconv.FormatInt(int64(cfg.Period/time.Second), 10))

	label := issuer + ":" + account
	return "otpauth://totp/" + url.PathEscape(label) + "?" + values.Encode(), nil
}

func GenerateCode(secret string, at time.Time, cfg Config) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return "", err
	}
	counter := uint64(at.Unix() / int64(normalized.Period/time.Second))
	return hotpSHA1(key, counter, normalized.Digits), nil
}

func Verify(secret, code string, at time.Time) bool {
	return VerifyConfig(secret, code, at, Config{})
}

func VerifyConfig(secret, code string, at time.Time, cfg Config) bool {
	normalized, err := normalizeConfig(cfg)
	if err != nil || !validCode(code, normalized.Digits) {
		return false
	}
	key, err := decodeSecret(secret)
	if err != nil {
		return false
	}

	counter := at.Unix() / int64(normalized.Period/time.Second)
	for offset := -normalized.Skew; offset <= normalized.Skew; offset++ {
		candidateCounter := counter + int64(offset)
		if candidateCounter < 0 {
			continue
		}
		candidate := hotpSHA1(key, uint64(candidateCounter), normalized.Digits)
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

func normalizeConfig(cfg Config) (Config, error) {
	if cfg.Digits == 0 {
		cfg.Digits = DefaultDigits
	}
	if cfg.Period == 0 {
		cfg.Period = DefaultPeriod
	}
	if cfg.Skew == 0 {
		cfg.Skew = DefaultSkew
	}
	if cfg.Digits < 6 || cfg.Digits > 8 {
		return Config{}, ErrInvalidConfig
	}
	if cfg.Period <= 0 || cfg.Period%time.Second != 0 {
		return Config{}, ErrInvalidConfig
	}
	if cfg.Skew < 0 || cfg.Skew > 10 {
		return Config{}, ErrInvalidConfig
	}
	return cfg, nil
}

func decodeSecret(secret string) ([]byte, error) {
	normalized := normalizeSecret(secret)
	if normalized == "" {
		return nil, ErrInvalidSecret
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(normalized)
	if err != nil || len(key) == 0 {
		return nil, ErrInvalidSecret
	}
	return key, nil
}

func normalizeSecret(secret string) string {
	secret = strings.ReplaceAll(secret, " ", "")
	secret = strings.TrimRight(secret, "=")
	return strings.ToUpper(strings.TrimSpace(secret))
}

func hotpSHA1(key []byte, counter uint64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)

	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	binCode := (uint32(sum[offset])&0x7f)<<24 |
		(uint32(sum[offset+1])&0xff)<<16 |
		(uint32(sum[offset+2])&0xff)<<8 |
		(uint32(sum[offset+3]) & 0xff)
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, binCode%mod)
}

func validCode(code string, digits int) bool {
	if len(code) != digits {
		return false
	}
	for _, ch := range code {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
