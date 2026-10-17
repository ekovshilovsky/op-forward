//go:build !windows

package cmd

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ekovshilovsky/op-forward/internal/auth"
)

// serveFixture isolates the token directory and the endpoint environment, and
// holds a loopback port that is already in use. Because runServe can only
// return once Start fails, an occupied port is what lets these tests drive the
// full startup sequence (flag parsing, token provisioning, listener bind)
// without leaving a daemon running.
type serveFixture struct {
	tokenDir string
	occupied net.Listener
	listen   string // tcp:// endpoint for the occupied port
}

func newServeFixture(t *testing.T) serveFixture {
	t.Helper()
	withUmask(t, 0o022)
	t.Setenv("OP_FORWARD_TOKEN_FILE", "")
	t.Setenv(envListenName, "")
	t.Setenv(envPortName, "")
	// The directory does not exist yet: provisioning it with the right mode is
	// part of the behavior under test.
	tokenDir := filepath.Join(t.TempDir(), "tokens")
	t.Setenv("OP_FORWARD_TOKEN_DIR", tokenDir)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupying a loopback port: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return serveFixture{tokenDir: tokenDir, occupied: ln, listen: "tcp://" + ln.Addr().String()}
}

func (fx serveFixture) path(name string) string { return filepath.Join(fx.tokenDir, name) }

// run executes runServe with the given flags and returns its error and output.
func (fx serveFixture) run(t *testing.T, flags ...string) (stdout, stderr string, err error) {
	t.Helper()
	setArgs(t, append([]string{"op-forward", "serve"}, flags...)...)
	out := captureStdio(t)
	err = runServe()
	stdout, stderr = out()
	return stdout, stderr, err
}

func TestRunServeProvisionsTokensThenReturnsListenError(t *testing.T) {
	fx := newServeFixture(t)

	stdout, _, err := fx.run(t, "--listen", fx.listen)
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("error = %v, want the address-in-use listen error", err)
	}

	// Token files are provisioned before the daemon binds, so a failed bind
	// still leaves valid credentials behind.
	assertMode(t, fx.tokenDir, 0o700)
	for _, name := range []string{auth.AccessTokenFile, auth.RefreshTokenFile, auth.LegacyTokenFile} {
		assertMode(t, fx.path(name), 0o600)
		if value, valid := readToken(fx.path(name)); len(value) != auth.TokenLength*2 || !valid {
			t.Errorf("%s = (%d chars, valid=%v), want a valid %d-character token", name, len(value), valid, auth.TokenLength*2)
		}
	}
	// The legacy file mirrors the access token for older proxy clients.
	if readFileString(t, fx.path(auth.LegacyTokenFile)) != readFileString(t, fx.path(auth.AccessTokenFile)) {
		t.Error("session.token should mirror access.token")
	}

	for _, want := range []string{
		"Refresh token generated",
		"Refresh token at: " + fx.path(auth.RefreshTokenFile),
		"Access token written to: " + fx.path(auth.AccessTokenFile),
		"Starting daemon on " + fx.listen,
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout is missing %q:\n%s", want, stdout)
		}
	}
}

// The refresh token has a 30-day lifetime and must survive daemon restarts,
// while the access token is rotated on every start.
func TestRunServeReusesRefreshTokenAndRotatesAccessToken(t *testing.T) {
	fx := newServeFixture(t)

	if _, _, err := fx.run(t, "--listen", fx.listen); !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("first run error = %v, want address in use", err)
	}
	refresh1 := readTokenValue(fx.path(auth.RefreshTokenFile))
	access1 := readTokenValue(fx.path(auth.AccessTokenFile))

	stdout, _, err := fx.run(t, "--listen", fx.listen)
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("second run error = %v, want address in use", err)
	}
	refresh2 := readTokenValue(fx.path(auth.RefreshTokenFile))
	access2 := readTokenValue(fx.path(auth.AccessTokenFile))

	if refresh2 != refresh1 {
		t.Errorf("refresh token was regenerated: %q -> %q", refresh1, refresh2)
	}
	if access2 == access1 {
		t.Error("access token was not rotated on the second start")
	}
	if !strings.Contains(stdout, "Refresh token reused (expires ") {
		t.Errorf("second run should report the refresh token was reused:\n%s", stdout)
	}
	if strings.Contains(stdout, "Refresh token generated") {
		t.Errorf("second run must not report generating a refresh token:\n%s", stdout)
	}
	if readTokenValue(fx.path(auth.LegacyTokenFile)) != access2 {
		t.Error("session.token was not refreshed with the new access token")
	}
}

// An upgrade from the single-token release leaves only session.token behind.
// Starting the daemon must promote that value to the refresh token so clients
// holding it keep working.
func TestRunServePromotesLegacyTokenToRefreshToken(t *testing.T) {
	fx := newServeFixture(t)
	writeFile(t, fx.path(auth.LegacyTokenFile), "legacy-secret\n"+expiryString(48*time.Hour)+"\n", 0o600)

	stdout, _, err := fx.run(t, "--listen", fx.listen)
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("error = %v, want address in use", err)
	}
	if got := readTokenValue(fx.path(auth.RefreshTokenFile)); got != "legacy-secret" {
		t.Errorf("refresh token = %q, want the promoted legacy value", got)
	}
	if !strings.Contains(stdout, "Refresh token reused") {
		t.Errorf("a promoted token is reported as reused:\n%s", stdout)
	}
}

// --port is a shorthand for a loopback listen endpoint. Typing it must beat an
// OP_FORWARD_LISTEN inherited from the environment, even when that value is
// unusable, so a stale profile export cannot override the command line.
func TestRunServeTypedPortOverridesEnvironmentListen(t *testing.T) {
	fx := newServeFixture(t)
	t.Setenv(envListenName, "bogus-endpoint")
	_, port, err := net.SplitHostPort(fx.occupied.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	stdout, _, runErr := fx.run(t, "--port", port)
	if !errors.Is(runErr, syscall.EADDRINUSE) {
		t.Fatalf("error = %v, want the address-in-use error for 127.0.0.1:%s", runErr, port)
	}
	if want := "Starting daemon on tcp://127.0.0.1:" + port; !strings.Contains(stdout, want) {
		t.Errorf("stdout is missing %q:\n%s", want, stdout)
	}
}

func TestRunServeRejectsInvalidEndpointBeforeCreatingTokens(t *testing.T) {
	fx := newServeFixture(t)

	_, _, err := fx.run(t, "--listen", "bogus-endpoint")
	if err == nil || !strings.Contains(err.Error(), "bogus-endpoint") {
		t.Fatalf("error = %v, want the endpoint parse error", err)
	}
	if _, statErr := os.Stat(fx.tokenDir); !os.IsNotExist(statErr) {
		t.Errorf("no token directory may be created for an invalid endpoint (stat err: %v)", statErr)
	}
}

func TestRunServeRefusesNonLoopbackBindAfterProvisioningTokens(t *testing.T) {
	fx := newServeFixture(t)

	_, _, err := fx.run(t, "--listen", "tcp://0.0.0.0:0")
	if err == nil || !strings.Contains(err.Error(), "refusing to bind to non-loopback") {
		t.Fatalf("error = %v, want the non-loopback refusal", err)
	}
	if _, statErr := os.Stat(fx.path(auth.RefreshTokenFile)); statErr != nil {
		t.Errorf("tokens are provisioned before the bind is attempted: %v", statErr)
	}
}

func TestExecuteServeDispatchesToDaemonStartup(t *testing.T) {
	fx := newServeFixture(t)
	setArgs(t, "op-forward", "serve", "--listen", fx.listen)
	out := captureStdio(t)
	err := Execute()
	stdout, _ := out()

	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("Execute(serve) error = %v, want address in use", err)
	}
	if !strings.Contains(stdout, "Starting daemon on "+fx.listen) {
		t.Errorf("Execute did not reach runServe; stdout = %q", stdout)
	}
}

// ---------- startup failures ----------

func TestRunServeFailsWhenRefreshTokenCannotBeProvisioned(t *testing.T) {
	fx := newServeFixture(t)
	// A regular file where the token directory should be blocks MkdirAll.
	writeFile(t, fx.tokenDir, "not a directory", 0o600)

	_, _, err := fx.run(t, "--listen", fx.listen)
	if err == nil || !strings.Contains(err.Error(), "refresh token setup") {
		t.Fatalf("error = %v, want a 'refresh token setup' error", err)
	}
}

func TestRunServeFailsWhenAccessTokenCannotBeSaved(t *testing.T) {
	fx := newServeFixture(t)
	blocker := filepath.Join(t.TempDir(), "blocker")
	writeFile(t, blocker, "a regular file", 0o600)
	t.Setenv("OP_FORWARD_TOKEN_FILE", filepath.Join(blocker, "sub", "access.token"))

	stdout, _, err := fx.run(t, "--listen", fx.listen)
	if err == nil || !strings.Contains(err.Error(), "saving access token") {
		t.Fatalf("error = %v, want a 'saving access token' error", err)
	}
	if !strings.Contains(stdout, "Refresh token generated") {
		t.Errorf("the refresh token should have been provisioned first:\n%s", stdout)
	}
}

// The legacy session.token file only serves older clients, so failing to write
// it is a warning and the daemon still proceeds to start.
func TestRunServeWarnsButContinuesWhenLegacyTokenCannotBeWritten(t *testing.T) {
	fx := newServeFixture(t)
	// A non-empty directory at the legacy path makes the final rename fail.
	writeFile(t, filepath.Join(fx.path(auth.LegacyTokenFile), "child"), "x", 0o600)

	_, stderr, err := fx.run(t, "--listen", fx.listen)
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("error = %v, want startup to continue to the listen error", err)
	}
	if !strings.Contains(stderr, "warning: could not write legacy session token") {
		t.Errorf("stderr = %q, want the legacy token warning", stderr)
	}
	if _, statErr := os.Stat(fx.path(auth.AccessTokenFile)); statErr != nil {
		t.Errorf("access token should still have been written: %v", statErr)
	}
}

// A failed legacy migration is reported as a warning. When the same condition
// also prevents generating a refresh token, startup fails with that error.
func TestRunServeReportsLegacyMigrationFailure(t *testing.T) {
	fx := newServeFixture(t)
	writeFile(t, fx.path(auth.LegacyTokenFile), "legacy-secret\n"+expiryString(48*time.Hour)+"\n", 0o600)
	// A directory squatting on the staging path makes every refresh write fail.
	if err := os.Mkdir(fx.path(auth.RefreshTokenFile)+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}

	_, stderr, err := fx.run(t, "--listen", fx.listen)
	if !strings.Contains(stderr, "warning: legacy token migration") {
		t.Errorf("stderr = %q, want the migration warning", stderr)
	}
	if err == nil || !strings.Contains(err.Error(), "refresh token setup") {
		t.Fatalf("error = %v, want a 'refresh token setup' error", err)
	}
}
