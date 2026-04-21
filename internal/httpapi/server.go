// Package httpapi implements an HTTP API server for managing and invoking actions.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nicotsx/microhook/internal/auth"
	"github.com/nicotsx/microhook/internal/execution"
	"github.com/nicotsx/microhook/internal/storage"
)

type Server struct {
	server *http.Server
	logger *slog.Logger
	auth   *auth.Service
	runs   runReader
	exec   actionInvoker
}

type contextKey string

const requestLogStateContextKey contextKey = "httpapi.request-log-state"

const (
	maxInvokeActionRequestBodyBytes = 1 << 20
	readTimeout                     = 15 * time.Second
	writeTimeout                    = 30 * time.Second
)

var errRequestBodyTooLarge = errors.New("request body too large")

type actionInvoker interface {
	HasAction(string) bool
	Invoke(context.Context, execution.InvokeParams) (storage.Run, error)
}

type runReader interface {
	GetRun(context.Context, string) (storage.Run, error)
	ListRuns(context.Context, storage.RunFilter) ([]storage.Run, error)
}

type invokeActionRequest struct {
	Mode  string          `json:"mode"`
	Input json.RawMessage `json:"input,omitempty"`
}

type runResponse struct {
	ID              string          `json:"id"`
	Action          string          `json:"action"`
	Status          string          `json:"status"`
	ExitCode        *int            `json:"exit_code,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	StartedAt       *time.Time      `json:"started_at,omitempty"`
	FinishedAt      *time.Time      `json:"finished_at,omitempty"`
	TimedOut        bool            `json:"timed_out,omitempty"`
	RequestMetadata json.RawMessage `json:"request_metadata,omitempty"`
	StdoutTail      string          `json:"stdout_tail,omitempty"`
	StderrTail      string          `json:"stderr_tail,omitempty"`
	ErrorSummary    string          `json:"error_summary,omitempty"`
}

type requestMetadata struct {
	Mode      string `json:"mode"`
	RequestID string `json:"request_id,omitempty"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type requestLogState struct {
	requestID string
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func New(listenAddress string, logger *slog.Logger, authService *auth.Service, executor actionInvoker, runs runReader) *Server {
	server := &Server{
		logger: logger,
		auth:   authService,
		runs:   runs,
		exec:   executor,
	}

	router := chi.NewRouter()
	router.Use(server.observeRequests)
	router.HandleFunc("/healthz", healthz)
	router.Route("/v1", func(r chi.Router) {
		r.Use(authenticate(authService))
		r.Get("/runs", server.listRuns)
		r.Get("/runs/{id}", server.getRun)
		r.Post("/actions/{name}/runs", server.invokeAction)
	})

	server.server = &http.Server{
		Addr:              listenAddress,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       60 * time.Second,
	}

	return server
}

func authenticate(authService *auth.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if authService == nil {
				writeUnauthorized(writer)
				return
			}

			identity, err := authService.AuthenticateRequest(request)
			if err != nil {
				writeUnauthorized(writer)
				return
			}

			next.ServeHTTP(writer, request.WithContext(auth.ContextWithIdentity(request.Context(), identity)))
		})
	}
}

func (s *Server) Serve(listener net.Listener) error {
	if s.logger != nil {
		s.logger.Info("http server started", "address", listener.Addr().String())
	}

	return s.server.Serve(listener)
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.logger != nil {
		s.logger.Info("http server shutting down")
	}

	return s.server.Shutdown(ctx)
}

func (s *Server) observeRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		startedAt := time.Now()
		state := &requestLogState{requestID: headerRequestID(request)}
		if state.requestID != "" {
			writer.Header().Set("X-Request-Id", state.requestID)
		}

		recorder := &statusRecorder{ResponseWriter: writer, status: http.StatusOK}
		request = request.WithContext(context.WithValue(request.Context(), requestLogStateContextKey, state))
		next.ServeHTTP(recorder, request)

		if s.logger == nil {
			return
		}

		route := request.URL.Path
		if routeContext := chi.RouteContext(request.Context()); routeContext != nil {
			if pattern := strings.TrimSpace(routeContext.RoutePattern()); pattern != "" {
				route = pattern
			}
		}

		attrs := []any{
			"method", request.Method,
			"route", route,
			"status", recorder.status,
			"duration_ms", time.Since(startedAt).Milliseconds(),
		}
		if state.requestID != "" {
			attrs = append(attrs, "request_id", state.requestID)
		}

		s.logger.Log(request.Context(), levelForHTTPStatus(recorder.status), "http request completed", attrs...)
	})
}

func healthz(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(writer, "ok\n"); err != nil {
		return
	}
}

func (s *Server) invokeAction(writer http.ResponseWriter, request *http.Request) {
	if s.exec == nil {
		writeJSONError(writer, http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError))
		return
	}

	actionName := strings.TrimSpace(chi.URLParam(request, "name"))
	if !s.exec.HasAction(actionName) {
		writeJSONError(writer, http.StatusNotFound, "action not found")
		return
	}

	identity, ok := s.requestIdentity(request.Context())
	if !ok || s.auth == nil {
		writeUnauthorized(writer)
		return
	}
	if err := s.auth.AuthorizeAction(identity, actionName); err != nil {
		writeJSONError(writer, http.StatusForbidden, http.StatusText(http.StatusForbidden))
		return
	}

	payload, err := decodeInvokeActionRequest(writer, request)
	if err != nil {
		if errors.Is(err, errRequestBodyTooLarge) {
			writeJSONError(writer, http.StatusRequestEntityTooLarge, err.Error())
			return
		}
		writeJSONError(writer, http.StatusBadRequest, err.Error())
		return
	}

	mode := strings.TrimSpace(payload.Mode)
	if mode != execution.InvokeModeSync && mode != execution.InvokeModeAsync {
		writeJSONError(writer, http.StatusBadRequest, "mode must be one of: sync, async")
		return
	}

	requestID := extractRequestID(request, payload.Input)
	setRequestID(writer, request.Context(), requestID)
	requestMetadata, err := json.Marshal(requestMetadata{Mode: mode, RequestID: requestID})
	if err != nil {
		writeJSONError(writer, http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError))
		return
	}

	run, err := s.exec.Invoke(request.Context(), execution.InvokeParams{
		ActionName:      actionName,
		Mode:            mode,
		Input:           payload.Input,
		RequestMetadata: requestMetadata,
		RequestID:       requestID,
	})
	if err != nil {
		s.writeInvocationError(writer, err)
		return
	}

	status := http.StatusOK
	if mode == execution.InvokeModeAsync {
		status = http.StatusAccepted
	}

	s.logActionInvocation(request.Context(), run, mode, requestID)
	writeJSON(writer, status, newRunResponse(run))
}

func (s *Server) getRun(writer http.ResponseWriter, request *http.Request) {
	if s.runs == nil {
		writeJSONError(writer, http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError))
		return
	}

	run, err := s.runs.GetRun(request.Context(), chi.URLParam(request, "id"))
	if err != nil {
		if errors.Is(err, storage.ErrRunNotFound) {
			writeJSONError(writer, http.StatusNotFound, "run not found")
			return
		}

		writeJSONError(writer, http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError))
		return
	}

	identity, ok := s.requestIdentity(request.Context())
	if !ok || s.auth == nil {
		writeUnauthorized(writer)
		return
	}
	if !identity.AllowsAction(run.ActionName) {
		writeJSONError(writer, http.StatusForbidden, http.StatusText(http.StatusForbidden))
		return
	}

	writeJSON(writer, http.StatusOK, newRunResponse(run))
}

func (s *Server) listRuns(writer http.ResponseWriter, request *http.Request) {
	if s.runs == nil {
		writeJSONError(writer, http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError))
		return
	}

	identity, ok := s.requestIdentity(request.Context())
	if !ok || s.auth == nil {
		writeUnauthorized(writer)
		return
	}

	actionName := strings.TrimSpace(request.URL.Query().Get("action"))
	if actionName != "" && !identity.AllowsAction(actionName) {
		writeJSONError(writer, http.StatusForbidden, http.StatusText(http.StatusForbidden))
		return
	}

	runs, err := s.runs.ListRuns(request.Context(), storage.RunFilter{
		ActionName: actionName,
		Status:     request.URL.Query().Get("status"),
	})
	if err != nil {
		if errors.Is(err, storage.ErrInvalidRunState) {
			writeJSONError(writer, http.StatusBadRequest, err.Error())
			return
		}

		writeJSONError(writer, http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError))
		return
	}

	if !identity.IsGlobal() {
		filtered := runs[:0]
		for _, run := range runs {
			if identity.AllowsAction(run.ActionName) {
				filtered = append(filtered, run)
			}
		}
		runs = filtered
	}

	response := make([]runResponse, 0, len(runs))
	for _, run := range runs {
		response = append(response, newRunResponse(run))
	}

	writeJSON(writer, http.StatusOK, response)
}

func writeUnauthorized(writer http.ResponseWriter) {
	writer.Header().Set("WWW-Authenticate", "Bearer")
	writeJSONError(writer, http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized))
}

func (s *Server) writeInvocationError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, execution.ErrActionNotFound), errors.Is(err, execution.ErrActionDisabled):
		writeJSONError(writer, http.StatusNotFound, "action not found")
	case errors.Is(err, execution.ErrActionConflict):
		writeJSONError(writer, http.StatusConflict, http.StatusText(http.StatusConflict))
	case errors.Is(err, execution.ErrInvalidMode), errors.Is(err, storage.ErrInvalidRunState):
		writeJSONError(writer, http.StatusBadRequest, err.Error())
	default:
		writeJSONError(writer, http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError))
	}
}

func decodeInvokeActionRequest(writer http.ResponseWriter, request *http.Request) (invokeActionRequest, error) {
	var payload invokeActionRequest
	request.Body = http.MaxBytesReader(writer, request.Body, maxInvokeActionRequestBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		if isRequestBodyTooLarge(err) {
			return invokeActionRequest{}, fmt.Errorf("request body must be at most %d bytes: %w", maxInvokeActionRequestBodyBytes, errRequestBodyTooLarge)
		}
		return invokeActionRequest{}, errors.New("request body must be valid JSON")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if isRequestBodyTooLarge(err) {
			return invokeActionRequest{}, fmt.Errorf("request body must be at most %d bytes: %w", maxInvokeActionRequestBodyBytes, errRequestBodyTooLarge)
		}
		return invokeActionRequest{}, errors.New("request body must contain a single JSON object")
	}

	return payload, nil
}

func (s *Server) requestIdentity(ctx context.Context) (auth.Identity, bool) {
	if s.auth == nil {
		return auth.Identity{}, false
	}

	return auth.IdentityFromContext(ctx)
}

func isRequestBodyTooLarge(err error) bool {
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}

func extractRequestID(request *http.Request, input json.RawMessage) string {
	if requestID := headerRequestID(request); requestID != "" {
		return requestID
	}

	if len(input) == 0 {
		return ""
	}

	var values map[string]json.RawMessage
	if err := json.Unmarshal(input, &values); err != nil {
		return ""
	}

	rawRequestID, ok := values["request_id"]
	if !ok {
		return ""
	}

	var requestID string
	if err := json.Unmarshal(rawRequestID, &requestID); err != nil {
		return ""
	}

	return strings.TrimSpace(requestID)
}

func headerRequestID(request *http.Request) string {
	return strings.TrimSpace(request.Header.Get("X-Request-Id"))
}

func setRequestID(writer http.ResponseWriter, ctx context.Context, requestID string) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}

	writer.Header().Set("X-Request-Id", requestID)
	if state, ok := requestLogStateFromContext(ctx); ok {
		state.requestID = requestID
	}
}

func requestLogStateFromContext(ctx context.Context) (*requestLogState, bool) {
	state, ok := ctx.Value(requestLogStateContextKey).(*requestLogState)
	return state, ok
}

func levelForHTTPStatus(status int) slog.Level {
	switch {
	case status >= http.StatusInternalServerError:
		return slog.LevelError
	case status >= http.StatusBadRequest:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

func (s *Server) logActionInvocation(ctx context.Context, run storage.Run, mode, requestID string) {
	if s.logger == nil {
		return
	}

	attrs := []any{
		"action", run.ActionName,
		"mode", mode,
		"run_id", run.ID,
		"run_status", run.Status,
	}
	if requestID != "" {
		attrs = append(attrs, "request_id", requestID)
	}
	if run.ExitCode != nil {
		attrs = append(attrs, "exit_code", *run.ExitCode)
	}
	if run.TimedOut {
		attrs = append(attrs, "timed_out", true)
	}

	s.logger.InfoContext(ctx, "action invocation handled", attrs...)
}

func newRunResponse(run storage.Run) runResponse {
	return runResponse{
		ID:              run.ID,
		Action:          run.ActionName,
		Status:          run.Status,
		ExitCode:        run.ExitCode,
		CreatedAt:       run.CreatedAt,
		StartedAt:       run.StartedAt,
		FinishedAt:      run.FinishedAt,
		TimedOut:        run.TimedOut,
		RequestMetadata: run.RequestMetadata,
		StdoutTail:      run.StdoutTail,
		StderrTail:      run.StderrTail,
		ErrorSummary:    run.ErrorSummary,
	}
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		return
	}
}

func writeJSONError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, errorResponse{Error: message})
}
