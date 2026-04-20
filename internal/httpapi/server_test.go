package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServerHealthz(t *testing.T) {
	server := New("127.0.0.1:0", slog.Default())

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
