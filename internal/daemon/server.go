package daemon

import (
	"crypto/subtle"
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
)

// Server is the host-side HTTP daemon that executes op commands.
type Server struct {
	token *auth.Token
	mu    sync.Mutex // Protects token state during concurrent renewal
	port  int
}

// New creates a new daemon server.
func New(token *auth.Token, port int) *Server {
	return &Server{token: token, port: port}
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

	// Verify authentication
	if !s.authenticate(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
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

	// Renew token on successful auth if needed (sliding expiration).
	// Mutex protects against concurrent renewal races.
	s.mu.Lock()
	if s.token.ShouldRenew() {
		s.token.Renew()
		if err := s.token.Save(); err != nil {
			log.Printf("[op-forward] warning: failed to renew token: %v", err)
		}
	}
	s.mu.Unlock()

	// Return result
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		log.Printf("[op-forward] warning: failed to write response: %v", err)
	}
}

// authenticate verifies the Bearer token from the request.
// Uses constant-time comparison to prevent timing side-channel attacks.
func (s *Server) authenticate(r *http.Request) bool {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return false
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		return false
	}
	tokenMatch := subtle.ConstantTimeCompare([]byte(parts[1]), []byte(s.token.Value)) == 1
	return tokenMatch && s.token.IsValid()
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
