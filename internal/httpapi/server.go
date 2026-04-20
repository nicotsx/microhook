package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nicotsx/microhook/internal/auth"
)

type Server struct {
	server *http.Server
	logger *slog.Logger
}

func New(listenAddress string, logger *slog.Logger, authService *auth.Service) *Server {
	router := chi.NewRouter()
	router.HandleFunc("/healthz", healthz)
	router.Route("/v1", func(r chi.Router) {
		r.Use(authenticate(authService))
		r.Get("/runs", notImplemented)
		r.Get("/runs/{id}", notImplemented)
		r.With(authorizeAction(authService)).Post("/actions/{name}/runs", notImplemented)
	})

	return &Server{
		server: &http.Server{
			Addr:              listenAddress,
			Handler:           router,
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		logger: logger,
	}
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

func authorizeAction(authService *auth.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			identity, ok := auth.IdentityFromContext(request.Context())
			if !ok || authService == nil {
				writeUnauthorized(writer)
				return
			}

			if err := authService.AuthorizeAction(identity, chi.URLParam(request, "name")); err != nil {
				http.Error(writer, http.StatusText(http.StatusForbidden), http.StatusForbidden)
				return
			}

			next.ServeHTTP(writer, request)
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

func notImplemented(writer http.ResponseWriter, _ *http.Request) {
	http.Error(writer, http.StatusText(http.StatusNotImplemented), http.StatusNotImplemented)
}

func writeUnauthorized(writer http.ResponseWriter) {
	writer.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(writer, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
}
