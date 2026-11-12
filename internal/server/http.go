// Package server assembles the single HTTP listener: health endpoints, the
// REST API and the MCP streamable-HTTP endpoint, both behind the same OAuth
// guard when configured. Without OAuth there is no authentication and no
// caller: only a server nothing but a trusted proxy can reach runs that way,
// and private repositories are then refused to everyone.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/giantswarm/skill-manager/internal/api"
)

// Config configures the listener.
type Config struct {
	Addr    string
	MCPPath string
	// OAuth, when set, makes the server an OAuth 2.1 resource server: the MCP
	// endpoint and the REST API require a bearer token the platform IdP
	// issued (forwarded by muster, agent-manager or the portal) or this
	// server's own, and every call carries the caller's identity. Off:
	// anonymous.
	OAuth *OAuthConfig
}

// Server is the assembled HTTP server.
type Server struct {
	http  *http.Server
	mcp   *mcpserver.StreamableHTTPServer
	oauth *oauthRuntime
	log   *slog.Logger
}

// New builds the server.
func New(cfg Config, rest *api.REST, mcpSrv *mcpserver.MCPServer, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg.MCPPath == "" {
		cfg.MCPPath = "/mcp"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// Readiness does not track GitHub: the API must stay reachable so
	// clients read a failure from the response (a 502, a source's error)
	// instead of an unready Service.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	s := &Server{log: log}
	if cfg.OAuth != nil {
		o, err := newOAuth(*cfg.OAuth, cfg.MCPPath, log)
		if err != nil {
			return nil, err
		}
		o.register(mux)
		s.oauth = o
	}

	// The REST API on its own mux so one guard covers every route; the
	// health endpoints above and the OAuth metadata stay open. Each route is
	// also a pattern of the outer mux, which names the request's span; the
	// prefix takes the rest (404, 405).
	restMux := http.NewServeMux()
	rest.Register(restMux)
	guarded := s.guard(restMux)
	for _, p := range api.Patterns {
		mux.Handle(p, guarded)
	}
	mux.Handle(api.Prefix+"/", guarded)

	if mcpSrv != nil {
		// mcp-go reads a POST body whole before validating it
		// (CVE-2026-108859); the bound keeps that read to what the REST API
		// accepts.
		s.mcp = mcpserver.NewStreamableHTTPServer(mcpSrv, mcpserver.WithEndpointPath(cfg.MCPPath))
		allowed := ""
		if cfg.OAuth != nil {
			allowed = origin(cfg.OAuth.BaseURL)
		}
		mux.Handle(cfg.MCPPath, http.MaxBytesHandler(checkOrigin(allowed, s.guardMCP(s.mcp)), maxMCPBodyBytes))
	}
	s.http = &http.Server{
		Addr:              cfg.Addr,
		Handler:           traced(mux),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: MCP streams outlive any fixed value.
		IdleTimeout: 120 * time.Second,
	}
	return s, nil
}

// traced opens a server span per request, joined to an inbound traceparent,
// named after the matched route so the name stays low-cardinality. Probes
// are not traced.
func traced(mux *http.ServeMux) http.Handler {
	return otelhttp.NewHandler(mux, "skill-manager",
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/healthz" && r.URL.Path != "/readyz"
		}),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			switch {
			case r.Pattern == "":
				return r.Method
			case strings.Contains(r.Pattern, " "):
				return r.Pattern
			default:
				return r.Method + " " + r.Pattern
			}
		}),
	)
}

// guard requires an authenticated caller on the REST API when OAuth is on.
func (s *Server) guard(next http.Handler) http.Handler {
	if s.oauth == nil {
		return next
	}
	return s.oauth.protectREST(next)
}

// checkOrigin refuses an MCP request whose Origin header is present and not
// allowed with 403, as the Streamable HTTP transport requires against DNS
// rebinding. The MCP clients are servers (muster), which send no Origin; a
// browser is only admitted from allowed, this server's own public origin
// ("" admits none).
func checkOrigin(allowed string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" && (allowed == "" || !strings.EqualFold(o, allowed)) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32600,"message":"origin not allowed"}}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// origin is the scheme://host[:port] of rawURL; "" when it has none.
func origin(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// maxMCPBodyBytes bounds an MCP request body, as the REST API bounds its own.
const maxMCPBodyBytes = 1 << 20

// guardMCP is guard for the MCP endpoint: the IdP ID token as the bearer.
func (s *Server) guardMCP(next http.Handler) http.Handler {
	if s.oauth == nil {
		return next
	}
	return s.oauth.protect(next)
}

// Handler exposes the mux (tests).
func (s *Server) Handler() http.Handler { return s.http.Handler }

// Run serves until ctx is done, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("listening", "addr", s.http.Addr)
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// The MCP sessions first: an open listening stream never goes idle, and
	// Shutdown would wait for it to the deadline.
	if s.mcp != nil {
		if err := s.mcp.Shutdown(shutdownCtx); err != nil {
			s.log.Warn("closing MCP sessions", "error", err)
		}
	}
	// Then the listener: requests in flight finish with the OAuth store
	// they validate against still open.
	err := s.http.Shutdown(shutdownCtx)
	if s.oauth != nil {
		oauthCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.oauth.shutdown(oauthCtx)
	}
	if err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	s.log.Info("server stopped")
	return nil
}
