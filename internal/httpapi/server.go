package httpapi

import (
	"context"
	"encoding/json"
	"errors"
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

func New(listenAddress string, logger *slog.Logger, authService *auth.Service, executor actionInvoker, runs runReader) *Server {
	server := &Server{
		logger: logger,
		auth:   authService,
		runs:   runs,
		exec:   executor,
	}

	router := chi.NewRouter()
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

	identity, ok := auth.IdentityFromContext(request.Context())
	if !ok || s.auth == nil {
		writeUnauthorized(writer)
		return
	}
	if err := s.auth.AuthorizeAction(identity, actionName); err != nil {
		writeJSONError(writer, http.StatusForbidden, http.StatusText(http.StatusForbidden))
		return
	}

	payload, err := decodeInvokeActionRequest(request)
	if err != nil {
		writeJSONError(writer, http.StatusBadRequest, err.Error())
		return
	}

	mode := strings.TrimSpace(payload.Mode)
	if mode != execution.InvokeModeSync && mode != execution.InvokeModeAsync {
		writeJSONError(writer, http.StatusBadRequest, "mode must be one of: sync, async")
		return
	}

	requestID := extractRequestID(request, payload.Input)
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

	writeJSON(writer, http.StatusOK, newRunResponse(run))
}

func (s *Server) listRuns(writer http.ResponseWriter, request *http.Request) {
	if s.runs == nil {
		writeJSONError(writer, http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError))
		return
	}

	runs, err := s.runs.ListRuns(request.Context(), storage.RunFilter{
		ActionName: request.URL.Query().Get("action"),
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

func decodeInvokeActionRequest(request *http.Request) (invokeActionRequest, error) {
	var payload invokeActionRequest
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return invokeActionRequest{}, errors.New("request body must be valid JSON")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return invokeActionRequest{}, errors.New("request body must contain a single JSON object")
	}

	return payload, nil
}

func extractRequestID(request *http.Request, input json.RawMessage) string {
	if request != nil {
		if requestID := strings.TrimSpace(request.Header.Get("X-Request-Id")); requestID != "" {
			return requestID
		}
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
