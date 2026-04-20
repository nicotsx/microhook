package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nicotsx/microhook/internal/auth/tokenformat"
	"github.com/nicotsx/microhook/internal/config"
)

func TestServiceAuthenticatesAndAuthorizesScopedTokens(t *testing.T) {
	service := newTestService(t)

	identity, err := service.service.AuthenticateHeader("Bearer " + service.scopedToken)
	if err != nil {
		t.Fatalf("authenticate header: %v", err)
	}

	if identity.Name() != "scoped" {
		t.Fatalf("expected identity name %q, got %q", "scoped", identity.Name())
	}

	if identity.IsGlobal() {
		t.Fatal("expected scoped identity")
	}

	if !identity.AllowsAction("deploy") {
		t.Fatal("expected scoped identity to allow deploy")
	}

	if err := service.service.AuthorizeAction(identity, "restart"); !errors.Is(err, ErrForbiddenAction) {
		t.Fatalf("expected forbidden error, got %v", err)
	}

	ctx := ContextWithIdentity(context.Background(), identity)
	fromContext, ok := IdentityFromContext(ctx)
	if !ok {
		t.Fatal("expected identity in context")
	}

	if fromContext.Name() != identity.Name() {
		t.Fatalf("expected context identity name %q, got %q", identity.Name(), fromContext.Name())
	}
}

func TestServiceRejectsInvalidBearerHeadersWithoutEchoingSecrets(t *testing.T) {
	service := newTestService(t)

	tests := []struct {
		name        string
		header      string
		wantErr     error
		secretCheck string
	}{
		{name: "missing", header: "", wantErr: ErrMissingBearerToken},
		{name: "wrong scheme", header: "Token " + service.scopedToken, wantErr: ErrInvalidBearerToken, secretCheck: service.scopedToken},
		{name: "missing value", header: "Bearer", wantErr: ErrInvalidBearerToken},
		{name: "invalid token", header: "Bearer legacy-secret", wantErr: ErrInvalidBearerToken, secretCheck: "legacy-secret"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := service.service.AuthenticateHeader(test.header)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("expected error %v, got %v", test.wantErr, err)
			}

			if test.secretCheck != "" && strings.Contains(err.Error(), test.secretCheck) {
				t.Fatalf("expected error not to contain %q, got %q", test.secretCheck, err.Error())
			}
		})
	}
}

type testService struct {
	service     *Service
	globalToken string
	scopedToken string
}

func newTestService(t *testing.T) testService {
	t.Helper()

	globalToken := mustGenerateToken(t)
	scopedToken := mustGenerateToken(t)

	service, err := New(config.AuthConfig{
		Tokens: []config.Token{
			{Name: "global", Value: globalToken},
			{Name: "scoped", Value: scopedToken, Actions: []string{"deploy"}},
		},
	})
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}

	return testService{
		service:     service,
		globalToken: globalToken,
		scopedToken: scopedToken,
	}
}

func mustGenerateToken(t *testing.T) string {
	t.Helper()

	token, err := tokenformat.Generate()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	return token
}
