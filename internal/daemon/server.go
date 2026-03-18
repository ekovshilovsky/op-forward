package daemon

import (
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ekovshilovsky/op-forward/internal/auth"
	"github.com/ekovshilovsky/op-forward/internal/executor"
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
	port         int
	version      string // Server's own version, used for client compatibility checks
}

// New creates a new daemon server with separate access and refresh tokens.
func New(accessToken, refreshToken *auth.Token, port int, version string) *Server {
	return &Server{
		accessToken:  accessToken,
		refreshToken: refreshToken,
		port:         port,
		version:      version,
	}
}

// Start begins listening on the loopback interface.
func (s *Server) Start() error {
	addr := fmt.Sprintf("127.0.0.1:%d", s.port)

	// Verify we're binding to loopback only — refuse any other address.
	host, _, err := net.SplitHostPort(addr)
	if err != nil || (host != "127.0.0.1" && host != "::1" && host != "localhost") {
		return fmt.Errorf("refusing to bind to non-loopback address: %s", addr)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/op/execute", s.handleExecute)
	mux.HandleFunc("/token/refresh", s.handleTokenRefresh)

	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: executor.MaxTimeout + 10*time.Second,
		IdleTimeout:  120 * time.Second,
	}

	fmt.Printf("op-forward daemon listening on %s\n", addr)
	return server.ListenAndServe()
}

// handleHealth returns a simple health check (no auth required).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		log.Printf("[op-forward] warning: failed to write health response: %v", err)
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
			json.NewEncoder(w).Encode(map[string]string{"error": message})
			return
		}
	}

	// Limit request body to 1 MB to prevent resource exhaustion
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	// Parse request
	var req executor.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
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
		json.NewEncoder(w).Encode(map[string]string{
			"error": "refresh_token_expired",
			"message": "op-forward: authentication expired — the op-forward refresh " +
				"token has not been used in over 30 days and the daemon was restarted. " +
				"Re-run your deployment script to push a fresh token to this VM. " +
				"This is NOT a 1Password authentication issue — it is the op-forward " +
				"inter-VM session token that needs to be redeployed.",
		})
		return
	}

	// Generate new access token.
	newAccess, err := auth.GenerateAccess()
	if err != nil {
		log.Printf("[op-forward] error generating access token during refresh: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Rotate the refresh token — new value and extended expiry.
	newRefresh, err := auth.GenerateRefresh()
	if err != nil {
		log.Printf("[op-forward] error generating refresh token during refresh: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

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
		auth.SaveToPath(newAccess, legacyPath)
	}

	// Swap in-memory tokens.
	s.accessToken = newAccess
	s.refreshToken = newRefresh

	log.Printf("[op-forward] token refresh: new access token (expires %s), new refresh token (expires %s)",
		newAccess.Expires.Format(time.RFC3339), newRefresh.Expires.Format(time.RFC3339))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(TokenRefreshResponse{
		AccessToken:    newAccess.Value,
		AccessExpires:  newAccess.Expires.Format(time.RFC3339),
		RefreshToken:   newRefresh.Value,
		RefreshExpires: newRefresh.Expires.Format(time.RFC3339),
	})
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
