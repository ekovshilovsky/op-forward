package daemon

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ekovshilovsky/op-forward/internal/auth"
	"github.com/ekovshilovsky/op-forward/internal/endpoint"
	"github.com/ekovshilovsky/op-forward/internal/executor"
	"github.com/ekovshilovsky/op-forward/internal/opcheck"
	"github.com/ekovshilovsky/op-forward/internal/version"
)

// MinClientVersion is the oldest client version the daemon will accept.
// Bump this when a release contains a security fix or breaking protocol
// change that older clients must not bypass.
var MinClientVersion = "0.1.0"

// Server is the host-side HTTP daemon that executes op commands.
type Server struct {
	accessToken  *auth.Token
	refreshToken *auth.Token
	mu           sync.Mutex // Protects token state during concurrent renewal
	endpoint     endpoint.Endpoint
	version      string // Server's own version, used for client compatibility checks

	// probeMu admits one authenticated /health/op probe at a time. Each probe
	// can raise a biometric prompt on the host, so concurrent callers must not
	// be able to stack prompts.
	probeMu sync.Mutex

	// probeTimeout bounds the authenticated probe; zero selects
	// opcheck.DefaultProbeTimeout. Tests shorten it.
	probeTimeout time.Duration
}

// New creates a new daemon server with separate access and refresh tokens,
// bound to the given endpoint (tcp://127.0.0.1:port or unix:///path.sock).
func New(accessToken, refreshToken *auth.Token, ep endpoint.Endpoint, version string) *Server {
	return &Server{
		accessToken:  accessToken,
		refreshToken: refreshToken,
		endpoint:     ep,
		version:      version,
	}
}

// Start opens the configured endpoint and serves until the listener fails.
// The endpoint package enforces the transport rules: TCP must be loopback,
// Unix sockets are created 0600 inside a 0700 directory.
func (s *Server) Start() error {
	ln, err := s.endpoint.Listen()
	if err != nil {
		return err
	}
	fmt.Printf("op-forward daemon listening on %s\n", s.endpoint)
	return s.Serve(ln)
}

// Serve runs the HTTP daemon on an already-open listener. Peer credentials
// are captured once per connection and carried in the request context so
// the peer check in handler() can refuse callers running as a different user.
func (s *Server) Serve(ln net.Listener) error {
	server := &http.Server{
		Handler:      s.handler(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: executor.MaxTimeout + 10*time.Second,
		IdleTimeout:  120 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if uid, ok := endpoint.PeerUID(c); ok {
				return withPeerUID(ctx, uid)
			}
			return ctx
		},
	}
	return server.Serve(ln)
}

// peerUIDKey is the context key under which Serve stores the connecting
// process's uid for Unix socket connections.
type peerUIDKey struct{}

func withPeerUID(ctx context.Context, uid int) context.Context {
	return context.WithValue(ctx, peerUIDKey{}, uid)
}

// handler builds the route table and wraps it in the peer check, so every
// route, present and future, is covered without each handler having to
// remember to call it.
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/health/op", s.handleHealthOp)
	mux.HandleFunc("/op/execute", s.handleExecute)
	mux.HandleFunc("/token/refresh", s.handleTokenRefresh)
	return rejectForeignPeer(mux)
}

// rejectForeignPeer refuses requests whose connection carries peer
// credentials for a user other than the one running the daemon. When no
// credentials are available (TCP, or an unsupported platform) the request
// proceeds to the bearer token check, which remains the primary gate.
func rejectForeignPeer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if uid, ok := r.Context().Value(peerUIDKey{}).(int); ok && uid != os.Getuid() {
			log.Printf("[op-forward] refused connection from uid %d (daemon runs as uid %d)", uid, os.Getuid())
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleHealth returns a simple health check (no auth required).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		log.Printf("[op-forward] warning: failed to write health response: %v", err)
	}
}

// handleHealthOp reports the host's op integration: whether the op CLI is
// present, its version, how many accounts it lists, and (only on request)
// whether an authenticated read succeeds. The classification logic lives in
// the opcheck package so "op-forward doctor" and this endpoint cannot
// disagree.
//
// Authentication: the bearer access token is required, exactly as for
// /op/execute. The body is limited to booleans, counts, versions, and
// classification strings, but it still describes the state of the host's
// 1Password integration, and the endpoint can start a process on the host, so
// it is not offered to unauthenticated callers. The plain /health route stays
// open and unchanged for liveness checks.
//
// Prompts: by default the endpoint runs metadata commands only, which never
// raise a biometric prompt, and reports the session as "not_probed". A caller
// that explicitly passes probe=1 additionally runs an authenticated read,
// which may raise a prompt on the host; at most one such probe runs at a
// time and a second concurrent request is refused with 409.
//
// A probe against a locked 1Password app gets no answer and runs to its
// timeout (opcheck.DefaultProbeTimeout), so the request can legitimately take
// that long. The HTTP response cannot signal progress, so the wait is
// announced in this daemon's log, which is where the person who can unlock the
// app on the host is looking; the caller receives app_locked in the report
// when the wait ends without an answer.
func (s *Server) handleHealthOp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authenticateAccess(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	opts := opcheck.Options{}
	if r.URL.Query().Get("probe") == "1" {
		if !s.probeMu.TryLock() {
			http.Error(w, "an authenticated probe is already in progress", http.StatusConflict)
			return
		}
		defer s.probeMu.Unlock()
		opts.Authenticated = true
		opts.ProbeTimeout = s.probeTimeout
		wait := s.probeTimeout
		if wait <= 0 {
			wait = opcheck.DefaultProbeTimeout
		}
		log.Printf("[op-forward] health probe: waiting up to %s for 1Password on this host; if the app is locked or shows an approval prompt, unlock or approve it now", wait)
	}

	report := opcheck.Check(r.Context(), opts)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(report); err != nil {
		log.Printf("[op-forward] warning: failed to write op health response: %v", err)
	}
}

// handleExecute runs an op command and returns the result.
func (s *Server) handleExecute(w http.ResponseWriter, r *http.Request) {
	// Verify HTTP method
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Verify authentication against the access token.
	if !s.authenticateAccess(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Check client version compatibility
	if clientVersion := r.Header.Get("X-Client-Version"); clientVersion != "" {
		upgradeRequired, updateAvailable, message := version.CheckCompatibility(
			clientVersion, s.version, MinClientVersion,
		)
		if updateAvailable != "" {
			w.Header().Set("X-Update-Available", updateAvailable)
		}
		if upgradeRequired {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUpgradeRequired)
			if err := json.NewEncoder(w).Encode(map[string]string{"error": message}); err != nil {
				log.Printf("[op-forward] warning: failed to write upgrade-required response: %v", err)
			}
			return
		}
	}

	// Limit request body to 1 MB to prevent resource exhaustion
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	// Parse request. A single JSON value must account for the entire body:
	// trailing data would mean part of what the client sent was silently
	// ignored, and the protocol promises 400 for malformed bodies.
	var req executor.Request
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}
	if _, err := dec.Token(); err != io.EOF {
		http.Error(w, "invalid request: trailing data after JSON body", http.StatusBadRequest)
		return
	}

	// Log the command (without sensitive values)
	log.Printf("[op-forward] executing: op %s", sanitizeArgsForLog(req.Args))

	// Execute
	result, err := executor.Execute(&req)
	if err != nil {
		log.Printf("[op-forward] error: %v", err)
		http.Error(w, fmt.Sprintf("execution error: %v", err), http.StatusInternalServerError)
		return
	}

	// Return result
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		log.Printf("[op-forward] warning: failed to write response: %v", err)
	}
}

// TokenRefreshResponse is returned by the /token/refresh endpoint.
type TokenRefreshResponse struct {
	AccessToken    string `json:"access_token"`
	AccessExpires  string `json:"access_expires"`
	RefreshToken   string `json:"refresh_token"`
	RefreshExpires string `json:"refresh_expires"`
}

// handleTokenRefresh validates the caller's refresh token, generates a new
// access token, rotates the refresh token, and returns both. This enables
// the proxy to self-heal after a daemon restart without manual redeployment.
func (s *Server) handleTokenRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bearerVal := extractBearer(r)
	if bearerVal == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate refresh token (constant-time comparison + expiry check).
	tokenMatch := hmac.Equal([]byte(bearerVal), []byte(s.refreshToken.Value))
	if !tokenMatch || !s.refreshToken.IsValid() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		if err := json.NewEncoder(w).Encode(map[string]string{
			"error": "refresh_token_expired",
			"message": "op-forward: authentication expired — the op-forward refresh " +
				"token has not been used in over 30 days and the daemon was restarted. " +
				"Re-run your deployment script to push a fresh token to this VM. " +
				"This is NOT a 1Password authentication issue — it is the op-forward " +
				"inter-VM session token that needs to be redeployed.",
		}); err != nil {
			log.Printf("[op-forward] warning: failed to write token-expired response: %v", err)
		}
		return
	}

	// Generate a new access token and rotate the refresh token — new value
	// and extended expiry.
	newAccess := auth.GenerateAccess()
	newRefresh := auth.GenerateRefresh()

	// Persist both tokens to disk before updating in-memory state.
	if path, err := auth.AccessTokenPath(); err == nil {
		if err := auth.SaveToPath(newAccess, path); err != nil {
			log.Printf("[op-forward] warning: failed to save access token: %v", err)
		}
	}
	if path, err := auth.RefreshTokenPath(); err == nil {
		if err := auth.SaveToPath(newRefresh, path); err != nil {
			log.Printf("[op-forward] warning: failed to save refresh token: %v", err)
		}
	}
	// Keep legacy session.token in sync for backward compatibility.
	if legacyPath, err := auth.LegacyTokenPath(); err == nil {
		if err := auth.SaveToPath(newAccess, legacyPath); err != nil {
			log.Printf("[op-forward] warning: failed to save legacy session token: %v", err)
		}
	}

	// Swap in-memory tokens.
	s.accessToken = newAccess
	s.refreshToken = newRefresh

	log.Printf("[op-forward] token refresh: new access token (expires %s), new refresh token (expires %s)",
		newAccess.Expires.Format(time.RFC3339), newRefresh.Expires.Format(time.RFC3339))

	w.Header().Set("Content-Type", "application/json")
	// #nosec G117 -- returning the rotated token pair is the purpose of this endpoint; the caller has just proven possession of the current refresh token, and the daemon listens only on loopback TCP or a user-private Unix socket (where the peer-credential check also applies)
	if err := json.NewEncoder(w).Encode(TokenRefreshResponse{
		AccessToken:    newAccess.Value,
		AccessExpires:  newAccess.Expires.Format(time.RFC3339),
		RefreshToken:   newRefresh.Value,
		RefreshExpires: newRefresh.Expires.Format(time.RFC3339),
	}); err != nil {
		log.Printf("[op-forward] warning: failed to write token refresh response: %v", err)
	}
}

// authenticateAccess verifies the Bearer token against the access token.
// Uses constant-time comparison to prevent timing side-channel attacks.
func (s *Server) authenticateAccess(r *http.Request) bool {
	bearerVal := extractBearer(r)
	if bearerVal == "" {
		return false
	}
	s.mu.Lock()
	tokenVal := s.accessToken.Value
	valid := s.accessToken.IsValid()
	s.mu.Unlock()
	tokenMatch := hmac.Equal([]byte(bearerVal), []byte(tokenVal))
	return tokenMatch && valid
}

// authenticate is kept as an alias for authenticateAccess for backward
// compatibility with test code that references the old method name.
func (s *Server) authenticate(r *http.Request) bool {
	return s.authenticateAccess(r)
}

// extractBearer parses a "Bearer <token>" Authorization header.
func extractBearer(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return ""
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		return ""
	}
	return parts[1]
}

// sanitizeArgsForLog redacts potentially sensitive arguments for logging.
// Redacts the value following --password or -p flags.
func sanitizeArgsForLog(args []string) string {
	sanitized := make([]string, len(args))
	redactNext := false
	for i, arg := range args {
		if redactNext {
			sanitized[i] = "[REDACTED]"
			redactNext = false
			continue
		}
		if arg == "--reveal" {
			sanitized[i] = arg
			continue
		}
		if arg == "--password" || arg == "-p" {
			sanitized[i] = arg
			redactNext = true
			continue
		}
		sanitized[i] = arg
	}
	return strings.Join(sanitized, " ")
}
