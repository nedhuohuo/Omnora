package share

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

const (
	randomSecretBytes     = 32
	passwordSaltBytes     = 16
	passwordKeyBytes      = 32
	passwordPBKDF2Iters   = 210000
	secretHashPrefix      = "sha256:"
	passwordHashAlgorithm = "pbkdf2-sha256"
)

func NewSecret() (string, error) {
	return randomToken(randomSecretBytes)
}

func NewSessionToken() (string, error) {
	return randomToken(randomSecretBytes)
}

func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return secretHashPrefix + base64.RawURLEncoding.EncodeToString(sum[:])
}

func VerifySecret(secret, hash string) bool {
	return constantTimeStringEqual(HashSecret(secret), hash)
}

func HashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}

	key, err := pbkdf2.Key(sha256.New, password, salt, passwordPBKDF2Iters, passwordKeyBytes)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s$%d$%s$%s",
		passwordHashAlgorithm,
		passwordPBKDF2Iters,
		base64.RawURLEncoding.EncodeToString(salt),
		base64.RawURLEncoding.EncodeToString(key),
	), nil
}

func VerifyPassword(password, hash string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != passwordHashAlgorithm {
		return false
	}

	iters, err := strconv.Atoi(parts[1])
	if err != nil || iters <= 0 {
		return false
	}
	salt, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(salt) == 0 {
		return false
	}
	want, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return false
	}

	got, err := pbkdf2.Key(sha256.New, password, salt, iters, len(want))
	if err != nil {
		return false
	}
	return constantTimeBytesEqual(got, want)
}

func constantTimeStringEqual(a, b string) bool {
	return constantTimeBytesEqual([]byte(a), []byte(b))
}

func constantTimeBytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		maxLen := len(a)
		if len(b) > maxLen {
			maxLen = len(b)
		}
		ap := make([]byte, maxLen)
		bp := make([]byte, maxLen)
		copy(ap, a)
		copy(bp, b)
		_ = subtle.ConstantTimeCompare(ap, bp)
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}

func randomToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
