package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicotsx/microhook/internal/auth"
	"github.com/nicotsx/microhook/internal/auth/tokenformat"
	"github.com/nicotsx/microhook/internal/config"
	"github.com/nicotsx/microhook/internal/execution"
	"github.com/nicotsx/microhook/internal/storage"
)

func TestServerHealthz(t *testing.T) {
	server := New("127.0.0.1:0", slog.Default(), newTestAuthService(t).service, nil, nil)

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
	fixture := newServerFixture(t, []config.Action{testAction("deploy", "printf deployed", "allow")})

	tests := []struct {
		name       string
		header     string
		wantStatus int
	}{
		{name: "missing auth", wantStatus: http.StatusUnauthorized},
		{name: "invalid token", header: "Bearer legacy-secret", wantStatus: http.StatusUnauthorized},
		{name: "valid token", header: "Bearer " + fixture.globalToken, wantStatus: http.StatusOK},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/runs", nil)
			if test.header != "" {
				request.Header.Set("Authorization", test.header)
			}

			response := httptest.NewRecorder()
			fixture.server.server.Handler.ServeHTTP(response, request)

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
	fixture := newServerFixture(t, []config.Action{
		testAction("deploy", "printf deployed", "allow"),
		testAction("restart", "printf restarted", "allow"),
	})

	tests := []struct {
		name       string
		header     string
		path       string
		body       string
		wantStatus int
	}{
		{
			name:       "scoped token forbidden on other action",
			header:     "Bearer " + fixture.scopedToken,
			path:       "/v1/actions/restart/runs",
			body:       `{"mode":"sync"}`,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "scoped token allowed on configured action",
			header:     "Bearer " + fixture.scopedToken,
			path:       "/v1/actions/deploy/runs",
			body:       `{"mode":"sync"}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "unknown action returns not found before authz",
			header:     "Bearer " + fixture.scopedToken,
			path:       "/v1/actions/missing/runs",
			body:       `{"mode":"sync"}`,
			wantStatus: http.StatusNotFound,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("Authorization", test.header)
			response := httptest.NewRecorder()

			fixture.server.server.Handler.ServeHTTP(response, request)

			if response.Result().StatusCode != test.wantStatus {
				t.Fatalf("expected status %d, got %d", test.wantStatus, response.Result().StatusCode)
			}
		})
	}
}

func TestRunReadRoutesEnforceScopedAuthorization(t *testing.T) {
	fixture := newServerFixture(t, []config.Action{
		testAction("deploy", "printf deployed", "allow"),
		testAction("restart", "printf restarted", "allow"),
	})
	ctx := context.Background()

	for _, run := range []storage.CreateRunParams{
		{
			ID:         "run-deploy",
			ActionName: "deploy",
			Status:     storage.RunStatusSucceeded,
			CreatedAt:  time.Date(2026, time.April, 21, 10, 15, 0, 0, time.UTC),
			StdoutTail: "deploy-output",
			ActionSnapshot: storage.ActionSnapshot{
				Description:       "Deploy",
				Mode:              config.ActionModeCommand,
				Command:           []string{"/bin/sh", "-c", "printf deployed"},
				Timeout:           time.Second,
				ConcurrencyPolicy: "allow",
				MaxOutputBytes:    1024,
				Enabled:           true,
			},
		},
		{
			ID:         "run-restart",
			ActionName: "restart",
			Status:     storage.RunStatusSucceeded,
			CreatedAt:  time.Date(2026, time.April, 21, 10, 16, 0, 0, time.UTC),
			StdoutTail: "restart-output",
			ActionSnapshot: storage.ActionSnapshot{
				Description:       "Restart",
				Mode:              config.ActionModeCommand,
				Command:           []string{"/bin/sh", "-c", "printf restarted"},
				Timeout:           time.Second,
				ConcurrencyPolicy: "allow",
				MaxOutputBytes:    1024,
				Enabled:           true,
			},
		},
	} {
		if _, err := fixture.store.CreateRun(ctx, run); err != nil {
			t.Fatalf("create run %q: %v", run.ID, err)
		}
	}

	t.Run("list returns only allowed actions", func(t *testing.T) {
		response := fixture.doRequest(http.MethodGet, "/v1/runs", "", fixture.scopedToken, "")
		if response.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
		}

		var runs []runResponse
		decodeResponseJSON(t, response, &runs)
		if len(runs) != 1 {
			t.Fatalf("expected 1 run, got %d", len(runs))
		}
		if runs[0].Action != "deploy" {
			t.Fatalf("expected action %q, got %q", "deploy", runs[0].Action)
		}
	})

	t.Run("list rejects unauthorized action filter", func(t *testing.T) {
		response := fixture.doRequest(http.MethodGet, "/v1/runs?action=restart", "", fixture.scopedToken, "")
		if response.Code != http.StatusForbidden {
			t.Fatalf("expected status %d, got %d", http.StatusForbidden, response.Code)
		}
	})

	t.Run("get rejects unauthorized run", func(t *testing.T) {
		response := fixture.doRequest(http.MethodGet, "/v1/runs/run-restart", "", fixture.scopedToken, "")
		if response.Code != http.StatusForbidden {
			t.Fatalf("expected status %d, got %d", http.StatusForbidden, response.Code)
		}
	})

	t.Run("get allows authorized run", func(t *testing.T) {
		response := fixture.doRequest(http.MethodGet, "/v1/runs/run-deploy", "", fixture.scopedToken, "")
		if response.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
		}

		var run runResponse
		decodeResponseJSON(t, response, &run)
		if run.Action != "deploy" {
			t.Fatalf("expected action %q, got %q", "deploy", run.Action)
		}
		if run.StdoutTail != "deploy-output" {
			t.Fatalf("expected stdout tail %q, got %q", "deploy-output", run.StdoutTail)
		}
	})
}

func TestInvokeActionSyncReturnsCompletedRun(t *testing.T) {
	fixture := newServerFixture(t, []config.Action{
		testAction("deploy", `cat >/dev/null; printf deploy-ok; printf deploy-warn >&2`, "allow"),
	})

	response := fixture.doRequest(http.MethodPost, "/v1/actions/deploy/runs", `{"mode":"sync","input":{"request_id":"req-123","reason":"backup-start"}}`, fixture.globalToken, "")
	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}

	var body runResponse
	decodeResponseJSON(t, response, &body)

	if body.Action != "deploy" {
		t.Fatalf("expected action %q, got %q", "deploy", body.Action)
	}
	if body.Status != storage.RunStatusSucceeded {
		t.Fatalf("expected status %q, got %q", storage.RunStatusSucceeded, body.Status)
	}
	if body.ExitCode == nil || *body.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %v", body.ExitCode)
	}
	if body.StartedAt == nil || body.FinishedAt == nil {
		t.Fatal("expected started_at and finished_at in sync response")
	}
	if body.StdoutTail != "deploy-ok" {
		t.Fatalf("expected stdout tail %q, got %q", "deploy-ok", body.StdoutTail)
	}
	if body.StderrTail != "deploy-warn" {
		t.Fatalf("expected stderr tail %q, got %q", "deploy-warn", body.StderrTail)
	}
	if requestID := response.Result().Header.Get("X-Request-Id"); requestID != "req-123" {
		t.Fatalf("expected response request id %q, got %q", "req-123", requestID)
	}

	var metadata requestMetadata
	if err := json.Unmarshal(body.RequestMetadata, &metadata); err != nil {
		t.Fatalf("decode request metadata: %v", err)
	}
	if metadata.Mode != execution.InvokeModeSync {
		t.Fatalf("expected request metadata mode %q, got %q", execution.InvokeModeSync, metadata.Mode)
	}
	if metadata.RequestID != "req-123" {
		t.Fatalf("expected request metadata request id %q, got %q", "req-123", metadata.RequestID)
	}
}

func TestInvokeActionAsyncReturnsAcceptedRunAndSupportsLookup(t *testing.T) {
	fixture := newServerFixture(t, []config.Action{
		testAction("deploy", `sleep 0.1; printf done`, "allow"),
	})

	response := fixture.doRequest(http.MethodPost, "/v1/actions/deploy/runs", `{"mode":"async","input":{"request_id":"body-id"}}`, fixture.globalToken, "header-id")
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, response.Code)
	}

	var accepted runResponse
	decodeResponseJSON(t, response, &accepted)
	if accepted.ID == "" {
		t.Fatal("expected async response to include a run id")
	}
	if accepted.Status != storage.RunStatusRunning {
		t.Fatalf("expected async accepted status %q, got %q", storage.RunStatusRunning, accepted.Status)
	}
	if requestID := response.Result().Header.Get("X-Request-Id"); requestID != "header-id" {
		t.Fatalf("expected response request id %q, got %q", "header-id", requestID)
	}

	completed := fixture.waitForRun(t, accepted.ID)
	if completed.Status != storage.RunStatusSucceeded {
		t.Fatalf("expected async run status %q, got %q", storage.RunStatusSucceeded, completed.Status)
	}
	if completed.StdoutTail != "done" {
		t.Fatalf("expected stdout tail %q, got %q", "done", completed.StdoutTail)
	}

	var metadata requestMetadata
	if err := json.Unmarshal(completed.RequestMetadata, &metadata); err != nil {
		t.Fatalf("decode request metadata: %v", err)
	}
	if metadata.Mode != execution.InvokeModeAsync {
		t.Fatalf("expected request metadata mode %q, got %q", execution.InvokeModeAsync, metadata.Mode)
	}
	if metadata.RequestID != "header-id" {
		t.Fatalf("expected header request id to win, got %q", metadata.RequestID)
	}
}

func TestInvokeActionBadRequestConflictAndNotFound(t *testing.T) {
	fixture := newServerFixture(t, []config.Action{
		testAction("reject-run", `sleep 0.15; printf done`, "reject"),
	})

	t.Run("bad request payload returns 400", func(t *testing.T) {
		response := fixture.doRequest(http.MethodPost, "/v1/actions/reject-run/runs", `{"mode":"later"}`, fixture.globalToken, "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
		}
	})

	t.Run("unknown action returns 404", func(t *testing.T) {
		response := fixture.doRequest(http.MethodPost, "/v1/actions/missing/runs", `{"mode":"sync"}`, fixture.globalToken, "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("expected status %d, got %d", http.StatusNotFound, response.Code)
		}
	})

	t.Run("unknown run returns 404", func(t *testing.T) {
		response := fixture.doRequest(http.MethodGet, "/v1/runs/run_missing", "", fixture.globalToken, "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("expected status %d, got %d", http.StatusNotFound, response.Code)
		}
	})

	t.Run("reject policy returns 409 while run in flight", func(t *testing.T) {
		first := fixture.doRequest(http.MethodPost, "/v1/actions/reject-run/runs", `{"mode":"async"}`, fixture.globalToken, "")
		if first.Code != http.StatusAccepted {
			t.Fatalf("expected status %d, got %d", http.StatusAccepted, first.Code)
		}

		var accepted runResponse
		decodeResponseJSON(t, first, &accepted)

		second := fixture.doRequest(http.MethodPost, "/v1/actions/reject-run/runs", `{"mode":"async"}`, fixture.globalToken, "")
		if second.Code != http.StatusConflict {
			t.Fatalf("expected status %d, got %d", http.StatusConflict, second.Code)
		}

		fixture.waitForRun(t, accepted.ID)
	})
}

func TestInvokeActionRejectsOversizedRequestBody(t *testing.T) {
	fixture := newServerFixture(t, []config.Action{
		testAction("deploy", `printf done`, "allow"),
	})

	body := `{"mode":"sync","input":"` + strings.Repeat("a", maxInvokeActionRequestBodyBytes) + `"}`
	response := fixture.doRequest(http.MethodPost, "/v1/actions/deploy/runs", body, fixture.globalToken, "")
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status %d, got %d", http.StatusRequestEntityTooLarge, response.Code)
	}

	var errorBody errorResponse
	decodeResponseJSON(t, response, &errorBody)
	if !strings.Contains(errorBody.Error, "at most") {
		t.Fatalf("expected oversized body error, got %q", errorBody.Error)
	}
}

func TestListRunsSupportsActionAndStatusFilters(t *testing.T) {
	fixture := newServerFixture(t, []config.Action{testAction("deploy", `printf done`, "allow")})
	ctx := context.Background()

	for _, run := range []storage.CreateRunParams{
		{
			ID:         "run-1",
			ActionName: "deploy",
			Status:     storage.RunStatusSucceeded,
			CreatedAt:  time.Date(2026, time.April, 21, 10, 15, 0, 0, time.UTC),
			ActionSnapshot: storage.ActionSnapshot{
				Description:       "Deploy",
				Mode:              config.ActionModeCommand,
				Command:           []string{"/bin/sh", "-c", "printf done"},
				Timeout:           time.Second,
				ConcurrencyPolicy: "allow",
				MaxOutputBytes:    1024,
				Enabled:           true,
			},
		},
		{
			ID:         "run-2",
			ActionName: "deploy",
			Status:     storage.RunStatusFailed,
			CreatedAt:  time.Date(2026, time.April, 21, 10, 16, 0, 0, time.UTC),
			ActionSnapshot: storage.ActionSnapshot{
				Description:       "Deploy",
				Mode:              config.ActionModeCommand,
				Command:           []string{"/bin/sh", "-c", "printf done"},
				Timeout:           time.Second,
				ConcurrencyPolicy: "allow",
				MaxOutputBytes:    1024,
				Enabled:           true,
			},
		},
		{
			ID:         "run-3",
			ActionName: "backup",
			Status:     storage.RunStatusSucceeded,
			CreatedAt:  time.Date(2026, time.April, 21, 10, 17, 0, 0, time.UTC),
			ActionSnapshot: storage.ActionSnapshot{
				Description:       "Backup",
				Mode:              config.ActionModeCommand,
				Command:           []string{"/bin/sh", "-c", "printf done"},
				Timeout:           time.Second,
				ConcurrencyPolicy: "allow",
				MaxOutputBytes:    1024,
				Enabled:           true,
			},
		},
	} {
		if _, err := fixture.store.CreateRun(ctx, run); err != nil {
			t.Fatalf("create run %q: %v", run.ID, err)
		}
	}

	response := fixture.doRequest(http.MethodGet, "/v1/runs?action=deploy&status=succeeded", "", fixture.globalToken, "")
	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}

	var runs []runResponse
	decodeResponseJSON(t, response, &runs)
	if len(runs) != 1 {
		t.Fatalf("expected 1 filtered run, got %d", len(runs))
	}
	if runs[0].ID != "run-1" {
		t.Fatalf("expected run id %q, got %q", "run-1", runs[0].ID)
	}
}

func TestServerLogsStructuredRequestEventsWithoutLeakingTokens(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		var buffer bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buffer, nil))
		fixture := newServerFixtureWithLogger(t, logger, []config.Action{
			testAction("deploy", `printf deploy-ok`, "allow"),
		})

		response := fixture.doRequest(http.MethodPost, "/v1/actions/deploy/runs", `{"mode":"sync","input":{"request_id":"req-123"}}`, fixture.globalToken, "")
		if response.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
		}

		lines := strings.Split(strings.TrimSpace(buffer.String()), "\n")
		if len(lines) < 2 {
			t.Fatalf("expected at least 2 log lines, got %d: %q", len(lines), buffer.String())
		}

		var messages []string
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}

			var entry map[string]any
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				t.Fatalf("expected JSON log line, got error %v for %q", err, line)
			}

			msg, _ := entry["msg"].(string)
			messages = append(messages, msg)
		}

		logs := buffer.String()
		for _, expected := range []string{"http request completed", "action invocation handled", "request_id", "run_id", "route", "status"} {
			if !strings.Contains(logs, expected) {
				t.Fatalf("expected logs to contain %q, got %q", expected, logs)
			}
		}
		if !containsString(messages, "http request completed") {
			t.Fatalf("expected request completion log message, got %v", messages)
		}
		if !containsString(messages, "action invocation handled") {
			t.Fatalf("expected action invocation log message, got %v", messages)
		}
		if strings.Contains(logs, fixture.globalToken) {
			t.Fatalf("expected logs not to contain bearer token, got %q", logs)
		}
	})

	t.Run("text", func(t *testing.T) {
		var buffer bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buffer, nil))
		fixture := newServerFixtureWithLogger(t, logger, []config.Action{
			testAction("deploy", `printf deploy-ok`, "allow"),
		})

		response := fixture.doRequest(http.MethodPost, "/v1/actions/deploy/runs", `{"mode":"sync","input":{"request_id":"req-123"}}`, fixture.globalToken, "")
		if response.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
		}

		logs := buffer.String()
		for _, expected := range []string{"msg=\"http request completed\"", "msg=\"action invocation handled\"", "request_id=req-123", "run_id=", "route=/v1/actions/{name}/runs", "status=200"} {
			if !strings.Contains(logs, expected) {
				t.Fatalf("expected logs to contain %q, got %q", expected, logs)
			}
		}
		if strings.Contains(logs, fixture.globalToken) {
			t.Fatalf("expected logs not to contain bearer token, got %q", logs)
		}
	})
}

func TestServerConfiguresRequestTimeouts(t *testing.T) {
	server := New("127.0.0.1:0", slog.Default(), newTestAuthService(t).service, nil, nil)

	if server.server.ReadTimeout != readTimeout {
		t.Fatalf("expected read timeout %s, got %s", readTimeout, server.server.ReadTimeout)
	}
	if server.server.WriteTimeout != writeTimeout {
		t.Fatalf("expected write timeout %s, got %s", writeTimeout, server.server.WriteTimeout)
	}
}

type serverFixture struct {
	server      *Server
	store       *storage.Store
	globalToken string
	scopedToken string
}

func newServerFixture(t *testing.T, actions []config.Action) serverFixture {
	return newServerFixtureWithLogger(t, slog.Default(), actions)
}

func newServerFixtureWithLogger(t *testing.T, logger *slog.Logger, actions []config.Action) serverFixture {
	t.Helper()

	authFixture := newTestAuthService(t)
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "microhook.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})

	executor := execution.New(store, config.Config{Actions: actions}.ActionRegistry())
	server := New("127.0.0.1:0", logger, authFixture.service, executor, store)

	return serverFixture{
		server:      server,
		store:       store,
		globalToken: authFixture.globalToken,
		scopedToken: authFixture.scopedToken,
	}
}

func (f serverFixture) doRequest(method, path, body, token, requestID string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	request := httptest.NewRequest(method, path, reader)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if requestID != "" {
		request.Header.Set("X-Request-Id", requestID)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}

	response := httptest.NewRecorder()
	f.server.server.Handler.ServeHTTP(response, request)
	return response
}

func (f serverFixture) waitForRun(t *testing.T, runID string) runResponse {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for {
		response := f.doRequest(http.MethodGet, "/v1/runs/"+runID, "", f.globalToken, "")
		if response.Code != http.StatusOK {
			t.Fatalf("expected status %d while polling run %q, got %d", http.StatusOK, runID, response.Code)
		}

		var run runResponse
		decodeResponseJSON(t, response, &run)
		if run.Status != storage.RunStatusRunning {
			return run
		}

		if time.Now().After(deadline) {
			t.Fatalf("run %q did not finish before timeout", runID)
		}

		time.Sleep(25 * time.Millisecond)
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

func testAction(name, shellCommand, concurrencyPolicy string) config.Action {
	return config.Action{
		Name:              name,
		Mode:              config.ActionModeCommand,
		Command:           []string{"/bin/sh", "-c", shellCommand},
		Timeout:           time.Second,
		ConcurrencyPolicy: concurrencyPolicy,
		MaxOutputBytes:    1024,
		Enabled:           true,
	}
}

func decodeResponseJSON(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()

	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response json: %v\nbody=%s", err, response.Body.String())
	}
}

func containsString(values []string, target string) bool {
	return slices.Contains(values, target)
}
