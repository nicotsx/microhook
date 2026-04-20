package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicotsx/microhook/internal/auth"
	"github.com/nicotsx/microhook/internal/auth/tokenformat"
	"github.com/nicotsx/microhook/internal/config"
)

func TestServerHealthz(t *testing.T) {
	server := New("127.0.0.1:0", slog.Default(), newTestAuthService(t).service)

	t.Run("get", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		response := httptest.NewRecorder()

		server.server.Handler.ServeHTTP(response, request)

		result := response.Result()
		defer result.Body.Close()

		body, err := io.ReadAll(result.Body)
		if err != nil {
			t.Fatalf("read response body: %v", err)
		}

		if result.StatusCode != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, result.StatusCode)
		}

		if contentType := result.Header.Get("Content-Type"); contentType != "text/plain; charset=utf-8" {
			t.Fatalf("expected content type text/plain; charset=utf-8, got %q", contentType)
		}

		if string(body) != "ok\n" {
			t.Fatalf("expected body ok\\n, got %q", string(body))
		}
	})

	t.Run("method not allowed", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/healthz", strings.NewReader(""))
		response := httptest.NewRecorder()

		server.server.Handler.ServeHTTP(response, request)

		result := response.Result()
		defer result.Body.Close()

		if result.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, result.StatusCode)
		}

		if allow := result.Header.Get("Allow"); allow != http.MethodGet {
			t.Fatalf("expected Allow header %q, got %q", http.MethodGet, allow)
		}
	})
}

func TestAuthenticateMiddlewareStoresIdentityInRequestContext(t *testing.T) {
	fixture := newTestAuthService(t)
	handler := authenticate(fixture.service)(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		identity, ok := auth.IdentityFromContext(request.Context())
		if !ok {
			t.Fatal("expected identity in request context")
		}

		if identity.Name() != "scoped" {
			t.Fatalf("expected identity name %q, got %q", "scoped", identity.Name())
		}

		writer.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "/v1/runs", nil)
	request.Header.Set("Authorization", "Bearer "+fixture.scopedToken)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Result().StatusCode != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, response.Result().StatusCode)
	}
}

func TestProtectedRoutesRequireAuth(t *testing.T) {
	fixture := newTestAuthService(t)
	server := New("127.0.0.1:0", slog.Default(), fixture.service)

	tests := []struct {
		name       string
		header     string
		wantStatus int
	}{
		{name: "missing auth", wantStatus: http.StatusUnauthorized},
		{name: "invalid token", header: "Bearer legacy-secret", wantStatus: http.StatusUnauthorized},
		{name: "valid token", header: "Bearer " + fixture.globalToken, wantStatus: http.StatusNotImplemented},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/runs", nil)
			if test.header != "" {
				request.Header.Set("Authorization", test.header)
			}

			response := httptest.NewRecorder()
			server.server.Handler.ServeHTTP(response, request)

			result := response.Result()
			defer result.Body.Close()

			body, err := io.ReadAll(result.Body)
			if err != nil {
				t.Fatalf("read response body: %v", err)
			}

			if result.StatusCode != test.wantStatus {
				t.Fatalf("expected status %d, got %d", test.wantStatus, result.StatusCode)
			}

			if test.wantStatus == http.StatusUnauthorized {
				if challenge := result.Header.Get("WWW-Authenticate"); challenge != "Bearer" {
					t.Fatalf("expected WWW-Authenticate header %q, got %q", "Bearer", challenge)
				}
			}

			if strings.Contains(string(body), "legacy-secret") {
				t.Fatalf("expected response body not to contain bearer token, got %q", string(body))
			}
		})
	}
}

func TestActionRoutesEnforceScopedAuthorization(t *testing.T) {
	fixture := newTestAuthService(t)
	server := New("127.0.0.1:0", slog.Default(), fixture.service)

	tests := []struct {
		name       string
		header     string
		path       string
		wantStatus int
	}{
		{
			name:       "scoped token forbidden on other action",
			header:     "Bearer " + fixture.scopedToken,
			path:       "/v1/actions/restart/runs",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "scoped token allowed on configured action",
			header:     "Bearer " + fixture.scopedToken,
			path:       "/v1/actions/deploy/runs",
			wantStatus: http.StatusNotImplemented,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, nil)
			request.Header.Set("Authorization", test.header)
			response := httptest.NewRecorder()

			server.server.Handler.ServeHTTP(response, request)

			if response.Result().StatusCode != test.wantStatus {
				t.Fatalf("expected status %d, got %d", test.wantStatus, response.Result().StatusCode)
			}
		})
	}
}

type testAuthService struct {
	service     *auth.Service
	globalToken string
	scopedToken string
}

func newTestAuthService(t *testing.T) testAuthService {
	t.Helper()

	globalToken := mustGenerateToken(t)
	scopedToken := mustGenerateToken(t)

	service, err := auth.New(config.AuthConfig{
		Tokens: []config.Token{
			{Name: "global", Value: globalToken},
			{Name: "scoped", Value: scopedToken, Actions: []string{"deploy"}},
		},
	})
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}

	return testAuthService{
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
