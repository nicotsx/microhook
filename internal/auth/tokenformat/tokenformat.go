package tokenformat

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
)

const (
	Prefix = "mhv1_"

	rawTokenBytes = 32
)

var (
	ErrInvalidToken = errors.New("invalid Microhook token")

	encodedTokenLength = base64.RawURLEncoding.EncodedLen(rawTokenBytes)
)

func Generate() (string, error) {
	raw := make([]byte, rawTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}

	return Prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func Validate(value string) error {
	if !strings.HasPrefix(value, Prefix) {
		return ErrInvalidToken
	}

	encoded := strings.TrimPrefix(value, Prefix)
	if len(encoded) != encodedTokenLength {
		return ErrInvalidToken
	}

	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != rawTokenBytes {
		return ErrInvalidToken
	}

	return nil
}
