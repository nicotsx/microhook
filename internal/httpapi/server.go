package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

type Server struct {
	server *http.Server
	logger *slog.Logger
}

func New(listenAddress string, logger *slog.Logger) *Server {
	router := chi.NewRouter()
	router.HandleFunc("/healthz", healthz)

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
