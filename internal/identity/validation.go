package identity

import (
	"fmt"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"

	"omnora/internal/domain"
)

const (
	minPasswordBytes = 12
	maxPasswordBytes = 1024
	maxDisplayRunes  = 80
)

func normalizeEmail(value string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(value))
	if email == "" {
		return "", fieldError("email", "is required")
	}
	if len(email) > 254 {
		return "", fieldError("email", "is too long")
	}
	if strings.Count(email, "@") != 1 {
		return "", fieldError("email", "must contain one @")
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || parsed.Name != "" {
		return "", fieldError("email", "must be a valid address")
	}
	local, domainPart, ok := strings.Cut(email, "@")
	if !ok || local == "" || domainPart == "" || strings.Contains(domainPart, "..") {
		return "", fieldError("email", "must be a valid address")
	}
	return email, nil
}

func normalizeDisplayName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" {
		return "", fieldError("display_name", "is required")
	}
	if utf8.RuneCountInString(name) > maxDisplayRunes {
		return "", fieldError("display_name", "is too long")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", fieldError("display_name", "cannot contain control characters")
		}
	}
	return name, nil
}

func validatePassword(value string) error {
	if len(value) < minPasswordBytes {
		return fieldError("password", "must be at least 12 bytes")
	}
	if len(value) > maxPasswordBytes {
		return fieldError("password", "is too long")
	}

	var lower, upper, digit, other bool
	for _, r := range value {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			other = true
		}
	}
	classes := 0
	for _, ok := range []bool{lower, upper, digit, other} {
		if ok {
			classes++
		}
	}
	if classes < 3 {
		return fieldError("password", "must contain at least three character classes")
	}
	return nil
}

func normalizeRole(role domain.AccountRole) (domain.AccountRole, error) {
	if role == "" {
		return domain.AccountRoleMember, nil
	}
	switch role {
	case domain.AccountRoleAdmin, domain.AccountRoleMember:
		return role, nil
	default:
		return "", fieldError("role", "is invalid")
	}
}

func fieldError(field, message string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalidInput, field, message)
}
