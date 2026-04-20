package tokenformat

import (
	"errors"
	"strings"
	"testing"
)

func TestGenerateProducesValidToken(t *testing.T) {
	token, err := Generate()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	if err := Validate(token); err != nil {
		t.Fatalf("expected generated token to validate: %v", err)
	}
}

func TestValidateRejectsInvalidTokens(t *testing.T) {
	validToken, err := Generate()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	tests := []string{
		"",
		"legacy-secret",
		"mhv1_short",
		validToken + "=",
		strings.Replace(validToken, Prefix, "other_", 1),
	}

	for _, token := range tests {
		t.Run(token, func(t *testing.T) {
			if err := Validate(token); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("expected ErrInvalidToken, got %v", err)
			}
		})
	}
}
