package server

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/knadh/koanf/v2"
	"go.uber.org/zap"
)

type Server struct {
	logger *zap.SugaredLogger
	router chi.Router
	server *http.Server
}

// New will setup the API listener
func New(config *koanf.Koanf) (*Server, error) {

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.GetHead)
	// CORS Config
	r.Use(cors.New(cors.Options{
		AllowedOrigins:   config.Strings("server.cors.allowed_origins"),
		AllowedMethods:   config.Strings("server.cors.allowed_methods"),
		AllowedHeaders:   config.Strings("server.cors.allowed_headers"),
		AllowCredentials: config.Bool("server.cors.allowed_credentials"),
		MaxAge:           config.Int("server.cors.max_age"),
	}).Handler)
	r.Use(middleware.Heartbeat("/health"))
	r.Use(middleware.Timeout(90 * time.Second))
	r.Use(middleware.Compress(3, "gzip"))
	r.Use(middleware.Recoverer)

	// Log Requests - Use appropriate format depending on the encoding
	if config.Bool("server.log_requests") {
		switch config.String("logger.encoding") {
		case "stackdriver":
			r.Use(loggerHTTPMiddlewareStackdriver(config.Bool("server.log_requests_body"), config.Strings("server.log_disabled_http")))
		default:
			r.Use(loggerHTTPMiddlewareDefault(config.Bool("server.log_requests_body"), config.Strings("server.log_disabled_http")))
		}
	}

	s := &Server{
		logger: zap.S().With("package", "server"),
		router: r,
	}

	return s, nil

}

// ListenAndServe will listen for requests
func (s *Server) ListenAndServe(config *koanf.Koanf) error {

	s.server = &http.Server{
		Addr:    net.JoinHostPort(config.String("server.host"), config.String("server.port")),
		Handler: s.router,
	}

	// Listen
	listener, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		return fmt.Errorf("Could not listen on %s: %v", s.server.Addr, err)
	}

	go func() {
		if err = s.server.Serve(listener); err != nil {
			s.logger.Fatalw("API Listen error", "error", err, "address", s.server.Addr)
		}
	}()
	s.logger.Infow("API Listening", "address", s.server.Addr, "tls", config.Bool("server.tls"))

	// Enable profiler
	if config.Bool("server.profiler_enabled") && config.String("server.profiler_path") != "" {
		zap.S().Debugw("Profiler enabled on API", "path", config.String("server.profiler_path"))
		s.router.Mount(config.String("server.profiler_path"), middleware.Profiler())
	}

	return nil

}

// Router returns the router
func (s *Server) Router() chi.Router {
	return s.router
}
