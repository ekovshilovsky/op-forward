//go:build !windows

package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ekovshilovsky/op-forward/internal/auth"
	"github.com/ekovshilovsky/op-forward/internal/endpoint"
	"github.com/ekovshilovsky/op-forward/internal/executor"
)

// This file characterizes the exit contract of runProxy end to end against a
// scripted daemon. The op shim execs the real op binary whenever the proxy
// exits 127 and relays every other status as-is, so each failure branch below
// asserts the exact exit status and the exact bytes written to stderr and
// stdout. A change to either is a change to the shim contract.
//
// The "Pins" comment on each test names the failure branch it covers by the
// diagnostic the branch prints, which is the stable identifier for it.

const (
	pathExecute = "/op/execute"
	pathRefresh = "/token/refresh"

	proxyTestVersion = "4.5.6-test"
)

// ---------- Scripted daemon ----------

// daemonReply is one scripted HTTP response. Truncate advertises a body longer
// than the one sent and then drops the connection, which surfaces on the
// client as a failed body read after the headers were accepted.
type daemonReply struct {
	Status   int
	Body     string
	Headers  map[string]string
	Truncate bool
}

// daemonRequest records the parts of a request that carry the proxy's
// identity and credentials.
type daemonRequest struct {
	Path          string
	Authorization string
	ClientVersion string
	UserAgent     string
	Body          string
}

// fakeDaemon is an httptest server that answers each path from a queue of
// scripted replies and records every request it receives. A request to a path
// whose queue is empty fails the test, and so does a scripted reply that was
// never requested, so the scripts double as an exact statement of the
// expected conversation.
type fakeDaemon struct {
	srv *httptest.Server

	mu     sync.Mutex
	script map[string][]daemonReply
	reqs   []daemonRequest
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()

	d := &fakeDaemon{script: map[string][]daemonReply{}}
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		d.mu.Lock()
		d.reqs = append(d.reqs, daemonRequest{
			Path:          r.URL.Path,
			Authorization: r.Header.Get("Authorization"),
			ClientVersion: r.Header.Get("X-Client-Version"),
			UserAgent:     r.Header.Get("User-Agent"),
			Body:          string(body),
		})
		var reply daemonReply
		queue := d.script[r.URL.Path]
		scripted := len(queue) > 0
		if scripted {
			reply = queue[0]
			d.script[r.URL.Path] = queue[1:]
		}
		d.mu.Unlock()

		if !scripted {
			t.Errorf("fakeDaemon: unscripted request to %s", r.URL.Path)
			http.Error(w, "unscripted request", http.StatusNotImplemented)
			return
		}

		for name, value := range reply.Headers {
			w.Header().Set(name, value)
		}
		if reply.Truncate {
			w.Header().Set("Content-Length", strconv.Itoa(len(reply.Body)+1000))
		}
		w.WriteHeader(reply.Status)
		_, _ = io.WriteString(w, reply.Body)
		if reply.Truncate {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	t.Cleanup(d.srv.Close)
	t.Cleanup(func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		for path, queue := range d.script {
			if len(queue) > 0 {
				t.Errorf("fakeDaemon: %d scripted repl(ies) for %s were never requested", len(queue), path)
			}
		}
	})
	return d
}

// Reply appends scripted responses for a path; they are served in order.
func (d *fakeDaemon) Reply(path string, replies ...daemonReply) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.script[path] = append(d.script[path], replies...)
}

// Endpoint returns the tcp:// endpoint string the proxy dials.
func (d *fakeDaemon) Endpoint() string {
	return "tcp://" + d.srv.Listener.Addr().String()
}

// Requests returns every request received so far, in arrival order.
func (d *fakeDaemon) Requests() []daemonRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]daemonRequest(nil), d.reqs...)
}

// requestPaths flattens a request log to its paths for sequence assertions.
func requestPaths(reqs []daemonRequest) string {
	paths := make([]string, len(reqs))
	for i, r := range reqs {
		paths[i] = r.Path
	}
	return strings.Join(paths, " ")
}

func resultReply(res executor.Result) daemonReply {
	body, _ := json.Marshal(res)
	return daemonReply{Status: http.StatusOK, Body: string(body)}
}

// refreshReply scripts a successful /token/refresh response carrying a fresh
// token pair whose expiries lie in the future.
func refreshReply(access, refresh string) daemonReply {
	body, _ := json.Marshal(tokenRefreshResponse{
		AccessToken:    access,
		AccessExpires:  expiryString(time.Hour),
		RefreshToken:   refresh,
		RefreshExpires: expiryString(24 * time.Hour),
	})
	return daemonReply{Status: http.StatusOK, Body: string(body)}
}

// ---------- Fixture ----------

// proxyFixture isolates everything runProxy reads from the environment: the
// token directory, the endpoint variables, the timeouts, and the build
// version. It points the proxy at a scripted daemon by default.
type proxyFixture struct {
	daemon   *fakeDaemon
	tokenDir string
}

func newProxyFixture(t *testing.T) proxyFixture {
	t.Helper()

	withVersion(t, proxyTestVersion)
	withUmask(t, 0o022)

	daemon := newFakeDaemon(t)
	tokenDir := filepath.Join(t.TempDir(), "tokens")
	t.Setenv("OP_FORWARD_TOKEN_DIR", tokenDir)
	t.Setenv("OP_FORWARD_TOKEN_FILE", "")
	t.Setenv(endpoint.EnvAddr, daemon.Endpoint())
	t.Setenv(endpoint.EnvHost, "")
	t.Setenv(endpoint.EnvPort, "")
	t.Setenv("OP_FORWARD_PROBE_TIMEOUT_MS", "")
	t.Setenv("OP_FORWARD_FETCH_TIMEOUT_MS", "")
	return proxyFixture{daemon: daemon, tokenDir: tokenDir}
}

func (fx proxyFixture) path(name string) string { return filepath.Join(fx.tokenDir, name) }

// seed writes a token file in the format the daemon produces: the value on the
// first line and the RFC 3339 expiry on the second.
func (fx proxyFixture) seed(t *testing.T, name, value string, expiresIn time.Duration) {
	t.Helper()
	writeFile(t, fx.path(name), value+"\n"+expiryString(expiresIn)+"\n", 0o600)
}

// seedBare writes a token file with no expiry line, as the pre-two-tier
// versions wrote session.token. The proxy treats such a token as not known to
// be valid.
func (fx proxyFixture) seedBare(t *testing.T, name, value string) {
	t.Helper()
	writeFile(t, fx.path(name), value+"\n", 0o600)
}

// deadEndpoint points the proxy at a loopback port with nothing listening and
// returns the endpoint string.
func deadEndpoint(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a loopback port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}
	ep := "tcp://" + addr
	t.Setenv(endpoint.EnvAddr, ep)
	return ep
}

// proxyOutcome is everything runProxy produced: the returned error and the
// text written to each standard stream.
type proxyOutcome struct {
	Err    error
	Stdout string
	Stderr string
}

// runProxyArgs runs runProxy as the operator would have typed
// "op-forward proxy <proxyArgs...>".
func runProxyArgs(t *testing.T, proxyArgs ...string) proxyOutcome {
	t.Helper()
	setArgs(t, append([]string{"op-forward", "proxy"}, proxyArgs...)...)
	read := captureStdio(t)
	err := runProxy()
	stdout, stderr := read()
	return proxyOutcome{Err: err, Stdout: stdout, Stderr: stderr}
}

// runForwarded runs the proxy the way the shim does: the op arguments follow a
// "--" separator so that flag-looking op arguments are never parsed here.
func runForwarded(t *testing.T, opArgs ...string) proxyOutcome {
	t.Helper()
	return runProxyArgs(t, append([]string{"--"}, opArgs...)...)
}

// requireSuccess fails unless err is nil, the proxy's signal for exit 0.
func requireSuccess(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("error = %v (%T), want nil (exit 0)", err, err)
	}
}

// requireInfraFailure asserts the shim-visible failure contract: status 127,
// the exact diagnostic on stderr, and nothing on stdout.
func requireInfraFailure(t *testing.T, got proxyOutcome, wantStderr string) {
	t.Helper()
	requireExitStatus(t, got.Err, proxyExitInfraFailure)
	if got.Stdout != "" {
		t.Errorf("stdout = %q, want empty (stdout carries op output only)", got.Stdout)
	}
	if got.Stderr != wantStderr {
		t.Errorf("stderr =\n%q\nwant\n%q", got.Stderr, wantStderr)
	}
}

// assertTokenFile verifies a rotated token file: owner-only mode, the expected
// value, and an expiry that is still in the future.
func assertTokenFile(t *testing.T, path, wantValue string) {
	t.Helper()
	assertMode(t, path, 0o600)
	value, valid := readToken(path)
	if value != wantValue {
		t.Errorf("%s holds %q, want %q", filepath.Base(path), value, wantValue)
	}
	if !valid {
		t.Errorf("%s has no future expiry line", filepath.Base(path))
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("%s.tmp was left behind (stat error: %v)", filepath.Base(path), err)
	}
}

// Byte-exact diagnostics, transcribed from the proxy's output rather than
// built from the production helpers, so a wording change fails here.

const wantNoTokenStderr = "op-forward: no authentication token found. The op-forward token files " +
	"are missing from this VM. Run your deployment script to push tokens, " +
	"or copy the refresh.token file from the host:\n" +
	"  scp host:~/Library/Caches/op-forward/refresh.token ~/.cache/op-forward/refresh.token\n\n" +
	"This is NOT a 1Password issue — it is the op-forward inter-VM session token.\n"

func wantRefreshFailedStderr(cause string) string {
	return "op-forward: token refresh failed — " + cause + "\n\n" +
		"The op-forward access token expired and the refresh token could not " +
		"obtain a new one. This typically means the refresh token has not been " +
		"used in over 30 days and the daemon was restarted with a new token.\n\n" +
		"To fix this, re-run your deployment script to push a fresh token, or:\n" +
		"  scp host:~/Library/Caches/op-forward/refresh.token ~/.cache/op-forward/refresh.token\n\n" +
		"This is NOT a 1Password authentication issue — it is the op-forward " +
		"inter-VM session token that needs to be redeployed.\n"
}

func wantUpdateNotice(available string) string {
	return "op-forward: update available (v" + proxyTestVersion + " → v" + available + ") — run: op-forward update\n"
}

// ---------- Relaying op results ----------

// Pins: the final relay of the forwarded command's stdout, stderr, and a zero
// exit code (nil error). Also pins the request the daemon sees: bearer token,
// version headers, and the JSON body built from the forwarded arguments.
func TestProxyRelaysOutputAndZeroExit(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.AccessTokenFile, "access-1", time.Hour)
	fx.daemon.Reply(pathExecute, resultReply(executor.Result{Stdout: "hello\n", Stderr: "note\n", ExitCode: 0}))

	got := runForwarded(t, "read", "op://vault/item/field")

	requireSuccess(t, got.Err)
	if got.Stdout != "hello\n" || got.Stderr != "note\n" {
		t.Errorf("relayed stdout/stderr = %q/%q, want %q/%q", got.Stdout, got.Stderr, "hello\n", "note\n")
	}
	want := daemonRequest{
		Path:          pathExecute,
		Authorization: "Bearer access-1",
		ClientVersion: proxyTestVersion,
		UserAgent:     "op-forward/" + proxyTestVersion,
		Body:          `{"args":["read","op://vault/item/field"],"timeout_ms":60000}`,
	}
	reqs := fx.daemon.Requests()
	if len(reqs) != 1 || reqs[0] != want {
		t.Errorf("daemon saw %+v, want exactly [%+v]", reqs, want)
	}
}

// Pins: the final relay with a non-zero op exit code. The code is returned
// unchanged, and an empty stdout is not written.
func TestProxyRelaysNonZeroOpExitCode(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.AccessTokenFile, "access-1", time.Hour)
	fx.daemon.Reply(pathExecute, resultReply(executor.Result{Stderr: "[ERROR] item not found\n", ExitCode: 3}))

	got := runForwarded(t, "item", "get", "missing")

	requireExitStatus(t, got.Err, 3)
	if got.Stdout != "" {
		t.Errorf("stdout = %q, want empty", got.Stdout)
	}
	if got.Stderr != "[ERROR] item not found\n" {
		t.Errorf("stderr = %q, want the op diagnostic only", got.Stderr)
	}
}

// Pins: the final relay when op itself exits 127. The proxy relays it as-is,
// which the shim cannot tell apart from an infrastructure failure and answers
// by falling back to the real op binary. This documents the existing
// ambiguity; it must not be changed without changing the shim.
func TestProxyRelaysOpExit127Unchanged(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.AccessTokenFile, "access-1", time.Hour)
	fx.daemon.Reply(pathExecute, resultReply(executor.Result{Stderr: "op: command not found\n", ExitCode: 127}))

	got := runForwarded(t, "whoami")

	requireExitStatus(t, got.Err, 127)
	if got.Stderr != "op: command not found\n" {
		t.Errorf("stderr = %q, want the relayed op diagnostic", got.Stderr)
	}
}

// Everything after "--" belongs to op, including arguments that look like proxy
// flags. The shim always inserts the separator, so a mistake here would make
// "op --help" print the proxy's usage instead of reaching the daemon.
func TestProxyForwardsFlagLookingArgumentsAfterSeparator(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.AccessTokenFile, "access-1", time.Hour)
	fx.daemon.Reply(pathExecute, resultReply(executor.Result{Stdout: "usage\n"}))

	got := runForwarded(t, "--help", "-h", "--timeout", "5")

	requireSuccess(t, got.Err)
	reqs := fx.daemon.Requests()
	if len(reqs) != 1 {
		t.Fatalf("daemon saw %d requests, want 1", len(reqs))
	}
	if want := `{"args":["--help","-h","--timeout","5"],"timeout_ms":60000}`; reqs[0].Body != want {
		t.Errorf("request body = %s, want %s", reqs[0].Body, want)
	}
}

// The --timeout flag becomes the execution timeout sent to the daemon.
func TestProxyForwardsTimeoutFlagToDaemon(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.AccessTokenFile, "access-1", time.Hour)
	fx.daemon.Reply(pathExecute, resultReply(executor.Result{}))

	got := runProxyArgs(t, "--timeout", "1234", "--", "whoami")

	requireSuccess(t, got.Err)
	reqs := fx.daemon.Requests()
	if len(reqs) != 1 || reqs[0].Body != `{"args":["whoami"],"timeout_ms":1234}` {
		t.Errorf("daemon saw %+v, want one request with timeout_ms 1234", reqs)
	}
}

// Pins: the X-Update-Available notice. It goes to stderr ahead of the relayed
// op stderr and never touches stdout.
func TestProxyPrintsUpdateNoticeToStderr(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.AccessTokenFile, "access-1", time.Hour)
	reply := resultReply(executor.Result{Stdout: "out\n", Stderr: "op-err\n"})
	reply.Headers = map[string]string{"X-Update-Available": "5.0.0"}
	fx.daemon.Reply(pathExecute, reply)

	got := runForwarded(t, "whoami")

	requireSuccess(t, got.Err)
	if got.Stdout != "out\n" {
		t.Errorf("stdout = %q, want the op output only", got.Stdout)
	}
	if want := wantUpdateNotice("5.0.0") + "op-err\n"; got.Stderr != want {
		t.Errorf("stderr =\n%q\nwant\n%q", got.Stderr, want)
	}
}

// ---------- Token selection and refresh ----------

// Pins: the known-expired shortcut. An access token whose expiry has passed is
// never sent; the proxy refreshes first and sends only the new token.
func TestProxyExpiredAccessTokenRefreshesBeforeFirstRequest(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.AccessTokenFile, "stale-access", -time.Hour)
	fx.seed(t, auth.RefreshTokenFile, "refresh-1", 24*time.Hour)
	fx.daemon.Reply(pathRefresh, refreshReply("new-access", "new-refresh"))
	fx.daemon.Reply(pathExecute, resultReply(executor.Result{Stdout: "ok\n"}))

	got := runForwarded(t, "whoami")

	requireSuccess(t, got.Err)
	if got.Stdout != "ok\n" || got.Stderr != "" {
		t.Errorf("stdout/stderr = %q/%q, want %q/empty", got.Stdout, got.Stderr, "ok\n")
	}
	reqs := fx.daemon.Requests()
	if seq := requestPaths(reqs); seq != pathRefresh+" "+pathExecute {
		t.Fatalf("request sequence = %q, want refresh then execute", seq)
	}
	if reqs[0].Authorization != "Bearer refresh-1" || reqs[0].ClientVersion != proxyTestVersion {
		t.Errorf("refresh request = %+v, want the refresh token and version header", reqs[0])
	}
	if reqs[1].Authorization != "Bearer new-access" {
		t.Errorf("execute request carried %q, want the refreshed access token", reqs[1].Authorization)
	}
	assertTokenFile(t, fx.path(auth.AccessTokenFile), "new-access")
	assertTokenFile(t, fx.path(auth.RefreshTokenFile), "new-refresh")
}

// Pins: the 401 recovery flow. The rejected token is replaced by a refresh,
// both token files are rotated on disk with owner-only mode, and the retry
// carries the new access token rather than the rejected one.
func TestProxyUnauthorizedRefreshesRotatesTokensAndRetries(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.AccessTokenFile, "stale-access", time.Hour)
	fx.seed(t, auth.RefreshTokenFile, "refresh-1", 24*time.Hour)
	fx.daemon.Reply(pathExecute,
		daemonReply{Status: http.StatusUnauthorized, Body: `{"error":"expired"}`},
		resultReply(executor.Result{Stdout: "after-refresh\n", ExitCode: 0}),
	)
	fx.daemon.Reply(pathRefresh, refreshReply("new-access", "new-refresh"))

	got := runForwarded(t, "whoami")

	requireSuccess(t, got.Err)
	if got.Stdout != "after-refresh\n" || got.Stderr != "" {
		t.Errorf("stdout/stderr = %q/%q, want %q/empty", got.Stdout, got.Stderr, "after-refresh\n")
	}
	reqs := fx.daemon.Requests()
	if seq := requestPaths(reqs); seq != pathExecute+" "+pathRefresh+" "+pathExecute {
		t.Fatalf("request sequence = %q, want execute, refresh, execute", seq)
	}
	wantAuth := []string{"Bearer stale-access", "Bearer refresh-1", "Bearer new-access"}
	for i, want := range wantAuth {
		if reqs[i].Authorization != want {
			t.Errorf("request %d Authorization = %q, want %q", i, reqs[i].Authorization, want)
		}
		if reqs[i].ClientVersion != proxyTestVersion {
			t.Errorf("request %d X-Client-Version = %q, want %q", i, reqs[i].ClientVersion, proxyTestVersion)
		}
	}
	for _, i := range []int{0, 2} {
		if reqs[i].UserAgent != "op-forward/"+proxyTestVersion {
			t.Errorf("request %d User-Agent = %q, want op-forward/%s", i, reqs[i].UserAgent, proxyTestVersion)
		}
	}
	assertTokenFile(t, fx.path(auth.AccessTokenFile), "new-access")
	assertTokenFile(t, fx.path(auth.RefreshTokenFile), "new-refresh")
}

// Pins: tokens are persisted before the retry is attempted, so a retry that is
// rejected again still leaves the rotated tokens on disk, and the rejection is
// reported through the generic non-200 branch (no second refresh attempt).
func TestProxyRotatesTokensEvenWhenRetryIsRejected(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.AccessTokenFile, "stale-access", time.Hour)
	fx.seed(t, auth.RefreshTokenFile, "refresh-1", 24*time.Hour)
	fx.daemon.Reply(pathExecute,
		daemonReply{Status: http.StatusUnauthorized, Body: "first"},
		daemonReply{Status: http.StatusUnauthorized, Body: "still unauthorized"},
	)
	fx.daemon.Reply(pathRefresh, refreshReply("new-access", "new-refresh"))

	got := runForwarded(t, "whoami")

	requireInfraFailure(t, got, "op-forward: daemon returned HTTP 401\nstill unauthorized")
	assertTokenFile(t, fx.path(auth.AccessTokenFile), "new-access")
	assertTokenFile(t, fx.path(auth.RefreshTokenFile), "new-refresh")
}

// Pins: the legacy migration fallback for the refresh token. Without a
// refresh.token file, session.token doubles as the refresh credential, and
// because it carries no expiry it is not trusted as an access token.
func TestProxyLegacySessionTokenIsRefreshFallback(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seedBare(t, auth.LegacyTokenFile, "legacy-secret")
	fx.daemon.Reply(pathRefresh, refreshReply("new-access", "new-refresh"))
	fx.daemon.Reply(pathExecute, resultReply(executor.Result{Stdout: "migrated\n"}))

	got := runForwarded(t, "whoami")

	requireSuccess(t, got.Err)
	if got.Stdout != "migrated\n" {
		t.Errorf("stdout = %q, want %q", got.Stdout, "migrated\n")
	}
	reqs := fx.daemon.Requests()
	if seq := requestPaths(reqs); seq != pathRefresh+" "+pathExecute {
		t.Fatalf("request sequence = %q, want refresh then execute", seq)
	}
	if reqs[0].Authorization != "Bearer legacy-secret" {
		t.Errorf("refresh Authorization = %q, want the legacy token", reqs[0].Authorization)
	}
	if reqs[1].Authorization != "Bearer new-access" {
		t.Errorf("execute Authorization = %q, want the refreshed access token", reqs[1].Authorization)
	}
	assertTokenFile(t, fx.path(auth.AccessTokenFile), "new-access")
	assertTokenFile(t, fx.path(auth.RefreshTokenFile), "new-refresh")
}

// Pins: the legacy fallback for the access token. A session.token that is
// still within its expiry is used directly when access.token is absent.
func TestProxyValidLegacySessionTokenServesAsAccessToken(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.LegacyTokenFile, "legacy-secret", time.Hour)
	fx.daemon.Reply(pathExecute, resultReply(executor.Result{Stdout: "legacy-ok\n"}))

	got := runForwarded(t, "whoami")

	requireSuccess(t, got.Err)
	reqs := fx.daemon.Requests()
	if len(reqs) != 1 || reqs[0].Path != pathExecute || reqs[0].Authorization != "Bearer legacy-secret" {
		t.Errorf("daemon saw %+v, want one execute request with the legacy token", reqs)
	}
}

// Pins: a refresh token alone is enough. With no access token and no legacy
// token, the proxy goes straight to a refresh instead of reporting missing
// credentials.
func TestProxyRefreshTokenAloneIsEnough(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.RefreshTokenFile, "refresh-1", 24*time.Hour)
	fx.daemon.Reply(pathRefresh, refreshReply("new-access", "new-refresh"))
	fx.daemon.Reply(pathExecute, resultReply(executor.Result{Stdout: "ok\n"}))

	got := runForwarded(t, "whoami")

	requireSuccess(t, got.Err)
	reqs := fx.daemon.Requests()
	if seq := requestPaths(reqs); seq != pathRefresh+" "+pathExecute {
		t.Fatalf("request sequence = %q, want refresh then execute", seq)
	}
	if reqs[0].Authorization != "Bearer refresh-1" || reqs[1].Authorization != "Bearer new-access" {
		t.Errorf("authorization sequence = %q, %q", reqs[0].Authorization, reqs[1].Authorization)
	}
}

// ---------- Infrastructure failures: exit 127 ----------

// Pins "op-forward: endpoint ... must be of the form" (endpoint resolution
// failure). The endpoint is validated before any token or network access, so
// this fails identically with no tokens present and nothing reaches the
// daemon. Covers both ways to supply the bad value.
func TestProxyRejectsInvalidEndpointBeforeAnythingElse(t *testing.T) {
	const wantStderr = "op-forward: endpoint \"bogus\" must be of the form tcp://host:port or unix:///path\n"

	t.Run("flag", func(t *testing.T) {
		fx := newProxyFixture(t)

		got := runProxyArgs(t, "--addr", "bogus", "--", "whoami")

		requireInfraFailure(t, got, wantStderr)
		if n := len(fx.daemon.Requests()); n != 0 {
			t.Errorf("daemon saw %d requests, want none", n)
		}
	})

	t.Run("environment", func(t *testing.T) {
		fx := newProxyFixture(t)
		t.Setenv(endpoint.EnvAddr, "bogus")

		got := runForwarded(t, "whoami")

		requireInfraFailure(t, got, wantStderr)
		if n := len(fx.daemon.Requests()); n != 0 {
			t.Errorf("daemon saw %d requests, want none", n)
		}
	})
}

// Pins the no-token guidance when no credentials exist at all. The token check
// runs before the daemon probe, so the guidance wins even when the daemon is
// also unreachable.
func TestProxyWithoutAnyTokenPrintsGuidance(t *testing.T) {
	fx := newProxyFixture(t)
	deadEndpoint(t)

	got := runForwarded(t, "whoami")

	requireInfraFailure(t, got, wantNoTokenStderr)
	if n := len(fx.daemon.Requests()); n != 0 {
		t.Errorf("daemon saw %d requests, want none", n)
	}
}

// Pins the no-token guidance reached after a rejection or an expiry, when
// there is nothing to refresh with.
func TestProxyWithoutRefreshCredentialsPrintsGuidance(t *testing.T) {
	t.Run("access token rejected", func(t *testing.T) {
		fx := newProxyFixture(t)
		fx.seed(t, auth.AccessTokenFile, "access-1", time.Hour)
		fx.daemon.Reply(pathExecute, daemonReply{Status: http.StatusUnauthorized, Body: `{"error":"expired"}`})

		got := runForwarded(t, "whoami")

		requireInfraFailure(t, got, wantNoTokenStderr)
		if seq := requestPaths(fx.daemon.Requests()); seq != pathExecute {
			t.Errorf("request sequence = %q, want a single execute", seq)
		}
	})

	t.Run("access token expired", func(t *testing.T) {
		fx := newProxyFixture(t)
		fx.seed(t, auth.AccessTokenFile, "stale-access", -time.Hour)

		got := runForwarded(t, "whoami")

		requireInfraFailure(t, got, wantNoTokenStderr)
		if n := len(fx.daemon.Requests()); n != 0 {
			t.Errorf("daemon saw %d requests, want none", n)
		}
	})
}

// Pins "op-forward: daemon not reachable at ...". Credentials are valid but
// nothing accepts connections, which is the signal for the shim to fall back
// to the local op binary.
func TestProxyReportsUnreachableDaemon(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.AccessTokenFile, "access-1", time.Hour)
	ep := deadEndpoint(t)

	got := runForwarded(t, "whoami")

	requireInfraFailure(t, got, "op-forward: daemon not reachable at "+ep+"\n")
	if n := len(fx.daemon.Requests()); n != 0 {
		t.Errorf("daemon saw %d requests, want none", n)
	}
}

// Pins the token refresh failure guidance when the daemon explains the
// rejection in a JSON message. Reached from a 401 on the first request. The
// cached tokens stay untouched.
func TestProxyRefreshFailureWithJSONMessage(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.AccessTokenFile, "stale-access", time.Hour)
	fx.seed(t, auth.RefreshTokenFile, "refresh-1", 24*time.Hour)
	fx.daemon.Reply(pathExecute, daemonReply{Status: http.StatusUnauthorized, Body: `{"error":"expired"}`})
	fx.daemon.Reply(pathRefresh, daemonReply{
		Status: http.StatusUnauthorized,
		Body:   `{"error":"invalid_token","message":"refresh token rejected"}`,
	})

	got := runForwarded(t, "whoami")

	requireInfraFailure(t, got, wantRefreshFailedStderr("refresh token rejected"))
	if v := readTokenValue(fx.path(auth.AccessTokenFile)); v != "stale-access" {
		t.Errorf("access token was overwritten with %q after a failed refresh", v)
	}
	if v := readTokenValue(fx.path(auth.RefreshTokenFile)); v != "refresh-1" {
		t.Errorf("refresh token was overwritten with %q after a failed refresh", v)
	}
}

// Pins the token refresh failure guidance when the refresh endpoint answers
// with a non-JSON body, as an intermediary would. Reached through the
// known-expired shortcut, so no execute request is ever sent.
func TestProxyRefreshFailureWithPlainTextBody(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.AccessTokenFile, "stale-access", -time.Hour)
	fx.seed(t, auth.RefreshTokenFile, "refresh-1", 24*time.Hour)
	fx.daemon.Reply(pathRefresh, daemonReply{Status: http.StatusBadGateway, Body: "upstream unavailable"})

	got := runForwarded(t, "whoami")

	requireInfraFailure(t, got, wantRefreshFailedStderr("refresh returned HTTP 502: upstream unavailable"))
	if seq := requestPaths(fx.daemon.Requests()); seq != pathRefresh {
		t.Errorf("request sequence = %q, want a single refresh", seq)
	}
}

// Pins the HTTP 426 (Upgrade Required) branch: the daemon's body is printed
// after the "op-forward: " prefix and the exit status is 127, so the shim
// falls back to the real op binary. This is the documented current behavior
// (issue #21) and is pinned deliberately; changing it is a separate, visible
// decision.
func TestProxyUpgradeRequiredPrintsBodyAndExits127(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.AccessTokenFile, "access-1", time.Hour)
	fx.daemon.Reply(pathExecute, daemonReply{
		Status: http.StatusUpgradeRequired,
		Body:   "client 4.5.6-test is below the minimum 5.0.0; run: op-forward update",
	})

	got := runForwarded(t, "whoami")

	requireInfraFailure(t, got, "op-forward: client 4.5.6-test is below the minimum 5.0.0; run: op-forward update\n")
}

// The update notice is printed before the status is examined, so it precedes
// the 426 text.
func TestProxyUpgradeRequiredAlsoPrintsUpdateNotice(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.AccessTokenFile, "access-1", time.Hour)
	fx.daemon.Reply(pathExecute, daemonReply{
		Status:  http.StatusUpgradeRequired,
		Body:    "too old",
		Headers: map[string]string{"X-Update-Available": "5.0.0"},
	})

	got := runForwarded(t, "whoami")

	requireInfraFailure(t, got, wantUpdateNotice("5.0.0")+"op-forward: too old\n")
}

// Pins "op-forward: daemon returned HTTP N" for any status other than 200, 401
// (which triggers a refresh), and 426. The raw body follows the status line
// with no added newline.
func TestProxyReportsUnexpectedHTTPStatus(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"internal error", http.StatusInternalServerError, "kaboom", "op-forward: daemon returned HTTP 500\nkaboom"},
		{"forbidden", http.StatusForbidden, "forbidden\n", "op-forward: daemon returned HTTP 403\nforbidden\n"},
		{"empty body", http.StatusNotFound, "", "op-forward: daemon returned HTTP 404\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newProxyFixture(t)
			fx.seed(t, auth.AccessTokenFile, "access-1", time.Hour)
			fx.daemon.Reply(pathExecute, daemonReply{Status: tc.status, Body: tc.body})

			got := runForwarded(t, "whoami")

			requireInfraFailure(t, got, tc.want)
		})
	}
}

// Pins "op-forward: parsing response: ...". A 200 whose body is not the
// daemon's JSON result (a captive portal or proxy error page, for example) is
// an infrastructure failure, not an op result.
func TestProxyRejectsMalformedResult(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.AccessTokenFile, "access-1", time.Hour)
	fx.daemon.Reply(pathExecute, daemonReply{Status: http.StatusOK, Body: "<html>gateway</html>"})

	got := runForwarded(t, "whoami")

	requireInfraFailure(t, got, "op-forward: parsing response: invalid character '<' looking for beginning of value\n")
}

// Pins "op-forward: reading response: ..." on the first request: the
// connection drops after the headers, so the body read fails.
func TestProxyReportsTruncatedResponse(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.AccessTokenFile, "access-1", time.Hour)
	fx.daemon.Reply(pathExecute, daemonReply{Status: http.StatusOK, Body: "partial", Truncate: true})

	got := runForwarded(t, "whoami")

	requireInfraFailure(t, got, "op-forward: reading response: unexpected EOF\n")
}

// Pins the same transport failure on the retry that follows a successful
// refresh, which is a separate branch from the first request's.
func TestProxyReportsTruncatedResponseOnRetry(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.AccessTokenFile, "stale-access", time.Hour)
	fx.seed(t, auth.RefreshTokenFile, "refresh-1", 24*time.Hour)
	fx.daemon.Reply(pathExecute,
		daemonReply{Status: http.StatusUnauthorized, Body: "expired"},
		daemonReply{Status: http.StatusOK, Body: "partial", Truncate: true},
	)
	fx.daemon.Reply(pathRefresh, refreshReply("new-access", "new-refresh"))

	got := runForwarded(t, "whoami")

	requireInfraFailure(t, got, "op-forward: reading response: unexpected EOF\n")
	assertTokenFile(t, fx.path(auth.AccessTokenFile), "new-access")
}

// ---------- Flags and dispatch ----------

// An unknown proxy flag is a usage error: status 2, the flag package's
// diagnostic and usage on stderr, and no network or token access.
func TestProxyUnknownFlagReturnsUsageStatus(t *testing.T) {
	fx := newProxyFixture(t)

	got := runProxyArgs(t, "--bogus", "--", "whoami")

	requireExitStatus(t, got.Err, 2)
	if got.Stdout != "" {
		t.Errorf("stdout = %q, want empty", got.Stdout)
	}
	for _, want := range []string{"flag provided but not defined: -bogus", "Usage of proxy:", "-timeout int"} {
		if !strings.Contains(got.Stderr, want) {
			t.Errorf("stderr is missing %q:\n%s", want, got.Stderr)
		}
	}
	if n := len(fx.daemon.Requests()); n != 0 {
		t.Errorf("daemon saw %d requests, want none", n)
	}
}

// A help request prints usage to stderr and ends successfully before any
// token or network access.
func TestProxyHelpFlagReturnsSuccessStatus(t *testing.T) {
	for _, flagName := range []string{"-h", "-help", "--help"} {
		t.Run(flagName, func(t *testing.T) {
			fx := newProxyFixture(t)

			got := runProxyArgs(t, flagName)

			requireExitStatus(t, got.Err, 0)
			if got.Stdout != "" {
				t.Errorf("stdout = %q, want empty", got.Stdout)
			}
			if !strings.Contains(got.Stderr, "Usage of proxy:") {
				t.Errorf("stderr should contain the usage text:\n%s", got.Stderr)
			}
			if n := len(fx.daemon.Requests()); n != 0 {
				t.Errorf("daemon saw %d requests, want none", n)
			}
		})
	}
}

// Execute returns the proxy's ExitStatus untouched, so the process entry point
// can turn it into the exit code. The relayed op code and the 127 failure code
// both travel this way.
func TestExecuteProxyReturnsExitStatus(t *testing.T) {
	fx := newProxyFixture(t)
	fx.seed(t, auth.AccessTokenFile, "access-1", time.Hour)
	fx.daemon.Reply(pathExecute, resultReply(executor.Result{ExitCode: 9}))

	setArgs(t, "op-forward", "proxy", "--", "whoami")
	read := captureStdio(t)
	err := Execute()
	_, _ = read()
	requireExitStatus(t, err, 9)

	deadEndpoint(t)
	setArgs(t, "op-forward", "proxy", "--", "whoami")
	read = captureStdio(t)
	err = Execute()
	_, stderr := read()
	requireExitStatus(t, err, proxyExitInfraFailure)
	if !strings.HasPrefix(stderr, "op-forward: daemon not reachable at ") {
		t.Errorf("stderr = %q, want the unreachable-daemon diagnostic", stderr)
	}
}

func TestExitStatusErrorText(t *testing.T) {
	if got := ExitStatus(127).Error(); got != "exit status 127" {
		t.Errorf("ExitStatus(127).Error() = %q, want %q", got, "exit status 127")
	}
	if got := fmt.Sprint(error(ExitStatus(2))); got != "exit status 2" {
		t.Errorf("formatted status = %q, want %q", got, "exit status 2")
	}
}
