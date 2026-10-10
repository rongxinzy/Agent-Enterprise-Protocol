package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemory           = 64 * 1024
	argonIterations       = 3
	argonParallelism      = 2
	argonSaltLength       = 16
	argonKeyLength        = 32
	minimumPasswordLength = 6
	maximumPasswordLength = 1024
)

// DefaultTemporaryPassword is assigned when user create or import omits the
// temporary password.
const DefaultTemporaryPassword = "123456"

var ErrPasswordPolicy = errors.New("password must contain 6 to 1024 characters")
var ErrPasswordEdgeWhitespace = errors.New("password must not start or end with whitespace")

var dummyPasswordHash = encodePassword("aep-dummy-password", make([]byte, argonSaltLength))

func ValidatePassword(password string) error {
	length := utf8.RuneCountInString(password)
	if length < minimumPasswordLength || length > maximumPasswordLength {
		return ErrPasswordPolicy
	}
	return nil
}

// EdgeWhitespace reports whether the password carries leading or trailing
// whitespace. New passwords reject it so the stored secret matches what the
// operator typed; verification paths trim instead (see TrimPassword).
func EdgeWhitespace(password string) bool {
	return strings.TrimSpace(password) != password
}

// TrimPassword strips leading and trailing whitespace before verification.
func TrimPassword(password string) string {
	return strings.TrimSpace(password)
}

func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, argonSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return encodePassword(password, salt), nil
}

func encodePassword(password string, salt []byte) string {
	hash := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, argonKeyLength)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonIterations, argonParallelism, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash))
}

func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var memory uint32
	var iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func VerifyPasswordOrDummy(encoded, password string) bool {
	if encoded == "" {
		encoded = dummyPasswordHash
	}
	return VerifyPassword(encoded, password)
}
