package main

// HTTP REST API for LLM agents (nanobot / Claude / OpenAI tool-calling).
//
// Boot lifecycle:
//
//   * runHTTPAPI is started in a goroutine from daemon.go, after the
//     Telegram bot and proactive scheduler.
//   * If neither ARBOX_API_READ_TOKEN nor ARBOX_API_ADMIN_TOKEN is set, the
//     server is NOT started — we never want to expose an unauthenticated
//     surface that lets an agent book/cancel classes. The daemon keeps
//     doing everything else.
//   * The server binds to ARBOX_HTTP_ADDR (default "127.0.0.1:8080" — the
//     loopback-only default is deliberate: the production target is the
//     Oracle Free Tier VM where nanobot runs as the same uid on the same
//     host, so loopback is the least-exposed path. Set ARBOX_HTTP_ADDR to
//     ":8080" explicitly if you want to listen on all interfaces, e.g.
//     for running nanobot in a separate container on the same network).
//   * On ctx cancellation we run a 5-second graceful Shutdown so in-flight
//     mutations finish (especially the burst-protected booker calls).
//
// All routes live under /api/v1/. Routing is plain net/http with a small
// helper for "method+path" matching — pulling in chi/gorilla just to have
// path params would be overkill given the tiny endpoint surface.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lafofo-nivo/arbox-scheduler/internal/arboxapi"
	"github.com/lafofo-nivo/arbox-scheduler/internal/config"
)

const (
	// defaultHTTPAddr is loopback-only on purpose. nanobot runs as the same
	// uid on the same Oracle VM, so loopback is the least-exposed path that
	// still works. Override with ARBOX_HTTP_ADDR=:8080 for separate-host or
	// separate-container consumers.
	defaultHTTPAddr  = "127.0.0.1:8080"
	httpShutdownWait = 5 * time.Second
)

// apiServer bundles the long-lived dependencies that handlers need. We pass
// it (not raw closures) so test code can build one with its own fake
// arboxapi client + temp-dir paths.
type apiServer struct {
	cfgReload     func() (*config.Config, error)
	client        *arboxapi.Client
	locID         int
	lookaheadDays int

	auth    *authMiddleware
	limiter *apiRateLimiter
}

// runHTTPAPI is the entry point invoked from daemon.go. It blocks until the
// context is cancelled (or, if disabled, returns immediately).
func runHTTPAPI(
	ctx context.Context,
	cfgReload func() (*config.Config, error),
	client *arboxapi.Client,
	locID, lookaheadDays int,
) {
	read := strings.TrimSpace(os.Getenv("ARBOX_API_READ_TOKEN"))
	admin := strings.TrimSpace(os.Getenv("ARBOX_API_ADMIN_TOKEN"))
	if read == "" && admin == "" {
		fmt.Println("[http] disabled (no API tokens set; set ARBOX_API_READ_TOKEN and/or ARBOX_API_ADMIN_TOKEN to enable)")
		return
	}

	addr := strings.TrimSpace(os.Getenv("ARBOX_HTTP_ADDR"))
	if addr == "" {
		addr = defaultHTTPAddr
	}

	srv := &apiServer{
		cfgReload:     cfgReload,
		client:        client,
		locID:         locID,
		lookaheadDays: lookaheadDays,
		auth:          newAuthMiddleware(read, admin),
		limiter:       newAPIRateLimiter(),
	}

	mux := srv.routes()
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	fmt.Printf("[http] listening on %s (read=%s admin=%s)\n",
		addr, maskAPIToken(read), maskAPIToken(admin))

	errCh := make(chan error, 1)
	go func() {
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), httpShutdownWait)
		defer cancel()
		if err := httpSrv.Shutdown(shutCtx); err != nil {
			fmt.Printf("[http] shutdown: %v\n", err)
		}
		fmt.Println("[http] stopped")
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			fmt.Printf("[http] listen error: %v\n", err)
		}
	}
}

// routes builds the mux. Every handler is wrapped with recover + auth +
// rate limit. /healthz and /openapi.json are unauthenticated (health checks +
// nanobot tool discovery).
func (s *apiServer) routes() http.Handler {
	mux := http.NewServeMux()

	// Unauthenticated.
	mux.HandleFunc("GET /api/v1/healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/v1/openapi.json", handleOpenAPI)

	// Read-only (either token).
	read := func(h http.HandlerFunc) http.Handler {
		return s.auth.requireRead(s.limiter.middleware(http.HandlerFunc(h)))
	}
	mux.Handle("GET /api/v1/version", read(s.handleVersion))
	mux.Handle("GET /api/v1/selftest", read(s.handleSelfTest))
	mux.Handle("GET /api/v1/status", read(s.handleStatus))
	mux.Handle("GET /api/v1/classes", read(s.handleClasses))
	mux.Handle("GET /api/v1/morning", read(s.handleMorning))
	mux.Handle("GET /api/v1/evening", read(s.handleEvening))
	mux.Handle("GET /api/v1/bookings", read(s.handleBookings))
	mux.Handle("GET /api/v1/plan", read(s.handlePlan))
	mux.Handle("GET /api/v1/attempts", read(s.handleAttempts))
	mux.Handle("GET /api/v1/audit", read(s.handleAudit))

	// Admin (mutations).
	admin := func(h http.HandlerFunc) http.Handler {
		return s.auth.requireAdmin(s.limiter.middleware(http.HandlerFunc(h)))
	}
	mux.Handle("POST /api/v1/book", admin(s.handleBook))
	mux.Handle("POST /api/v1/cancel", admin(s.handleCancel))
	mux.Handle("POST /api/v1/waitlist", admin(s.handleWaitlist))
	mux.Handle("POST /api/v1/waitlist/leave", admin(s.handleWaitlistLeave))
	mux.Handle("POST /api/v1/pause", admin(s.handlePause))
	mux.Handle("POST /api/v1/resume", admin(s.handleResume))
	mux.Handle("POST /api/v1/plan/day", admin(s.handlePlanDay))
	mux.Handle("POST /api/v1/plan/clear", admin(s.handlePlanClear))

	// Generic passthrough to the upstream Arbox member API for anything
	// we don't yet wrap in a typed endpoint. Admin-token-only because the
	// body is opaque JSON — we can't enforce the ?confirm=1 dry-run gate
	// on arbitrary routes, so gate it at the bearer layer instead.
	// Read-like methods (GET/HEAD) still allowed; see handler for the
	// method + path safelist.
	mux.Handle("POST /api/v1/arbox/query", admin(s.handleArboxQuery))

	return recoverMiddleware(mux)
}

// recoverMiddleware turns a panic in a handler into a 500 + log line so a
// bad path can't take down the daemon (which is the whole reason we put the
// API in the same process).
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				fmt.Printf("[http] panic in %s %s: %v\n", r.Method, r.URL.Path, rec)
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"error": "internal server error",
					"code":  "panic",
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg, code string) {
	writeJSON(w, status, map[string]any{
		"error": msg,
		"code":  code,
	})
}
