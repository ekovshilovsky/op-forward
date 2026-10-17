package cmd

import (
	"encoding/json"
	"errors"
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
	"github.com/ekovshilovsky/op-forward/internal/daemon"
	"github.com/ekovshilovsky/op-forward/internal/endpoint"
	"github.com/ekovshilovsky/op-forward/internal/opcheck"
)

// This file characterizes "op-forward doctor". The tests are organized around
// the classification the command exists to get right: a failure must be
// reported against the layer that is actually broken (shim, endpoint, daemon,
// tokens, host-side 1Password), and a healthy host must never be reported as
// broken because of a misleading probe.

const (
	doctorTestVersion = "9.8.7-test"
	// The token values are distinctive so a leak into any report is caught by
	// a plain substring search.
	doctorAccessValue  = "ACCESS-TOKEN-VALUE-0123456789abcdef"
	doctorRefreshValue = "REFRESH-TOKEN-VALUE-fedcba9876543210"
	doctorLegacyValue  = "LEGACY-TOKEN-VALUE-00112233445566"
)

// ---------- Fixtures ----------

// doctorFixture isolates everything doctor reads from the environment: HOME
// (the shim location), the token directory, the endpoint variables, and PATH.
// PATH starts empty so the developer's real op can never be found, and a
// daemon that may be running on the default port is never contacted because
// every test names its endpoint explicitly.
type doctorFixture struct {
	home     string
	tokenDir string
	shim     string
}

func newDoctorFixture(t *testing.T) doctorFixture {
	t.Helper()
	withVersion(t, doctorTestVersion)
	home := tempHome(t)
	tokenDir := t.TempDir()
	t.Setenv("OP_FORWARD_TOKEN_DIR", tokenDir)
	t.Setenv("OP_FORWARD_TOKEN_FILE", "")
	for _, name := range []string{endpoint.EnvAddr, endpoint.EnvListen, endpoint.EnvHost, endpoint.EnvPort} {
		t.Setenv(name, "")
	}
	t.Setenv("PATH", t.TempDir())

	fx := doctorFixture{home: home, tokenDir: tokenDir, shim: filepath.Join(home, ".local", "bin", "op")}
	fx.writeToken(t, auth.AccessTokenFile, doctorAccessValue, time.Now().Add(time.Hour))
	fx.writeToken(t, auth.RefreshTokenFile, doctorRefreshValue, time.Now().Add(24*time.Hour))
	return fx
}

func (fx doctorFixture) writeToken(t *testing.T, name, value string, expires time.Time) {
	t.Helper()
	if err := auth.SaveToPath(&auth.Token{Value: value, Expires: expires}, filepath.Join(fx.tokenDir, name)); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

func (fx doctorFixture) removeToken(t *testing.T, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(fx.tokenDir, name)); err != nil {
		t.Fatalf("removing %s: %v", name, err)
	}
}

// installShim writes an op-forward shim stamped with the fixture version.
func (fx doctorFixture) installShim(t *testing.T) {
	t.Helper()
	writeFile(t, fx.shim, renderShim("/opt/op-forward", "/usr/bin/op"), 0o755)
}

// fakeOpScript models a desktop-app-integration op: whoami reports "not signed
// in" while authenticated reads work. FAKE_VAULT_EXIT makes the read fail with
// FAKE_VAULT_STDERR.
const fakeOpScript = `case "$1" in
  --version) echo 2.41.0 ;;
  whoami) echo "[ERROR] 2026/10/17 09:09:10 account is not signed in" >&2; exit 1 ;;
  account) printf '%s' '[{"url":"my.1password.com"}]' ;;
  vault)
    if [ -n "$FAKE_VAULT_SLEEP" ]; then
      exec sleep "$FAKE_VAULT_SLEEP"
    fi
    if [ -n "$FAKE_VAULT_EXIT" ]; then
      echo "$FAKE_VAULT_STDERR" >&2
      exit "$FAKE_VAULT_EXIT"
    fi
    echo '[{"name":"SECRET-VAULT-NAME"}]'
    ;;
esac`

// installFakeOp puts the fake op on PATH and returns its invocation log.
func installFakeOp(t *testing.T) string {
	t.Helper()
	return fakeBin(t, "op", fakeOpScript)
}

// startRealDaemon runs the actual daemon on an ephemeral loopback port, backed
// by the fixture's access token, and returns its tcp:// endpoint.
func startRealDaemon(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	ep := endpoint.Endpoint{Network: "tcp", Address: ln.Addr().String()}
	srv := daemon.New(
		&auth.Token{Value: doctorAccessValue, Expires: time.Now().Add(time.Hour), TTL: auth.AccessTokenTTL},
		&auth.Token{Value: doctorRefreshValue, Expires: time.Now().Add(24 * time.Hour), TTL: auth.RefreshTokenTTL},
		ep, doctorTestVersion)
	// Serve returns when the listener is closed during cleanup; that error is
	// the expected shutdown signal.
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = ln.Close() })
	return ep.String()
}

// closedEndpoint returns a loopback endpoint on which nothing is listening.
func closedEndpoint(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return "tcp://" + addr
}

// doctorStub is a daemon whose /health and /health/op responses the test
// controls, and which records how /health/op was asked.
type doctorStub struct {
	srv    *httptest.Server
	health http.HandlerFunc
	op     http.HandlerFunc

	mu      sync.Mutex
	queries []string
	auths   []string
}

func newDoctorStub(t *testing.T) *doctorStub {
	t.Helper()
	s := &doctorStub{
		health: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) },
		op: func(w http.ResponseWriter, _ *http.Request) {
			writeOpReport(w, opcheck.Report{
				OpPresent: true, OpVersion: "2.41.0",
				Integration: opcheck.IntegrationReachable, AccountCount: intPtr(1),
				Session: opcheck.SessionNotProbed,
			})
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { s.health(w, r) })
	mux.HandleFunc("/health/op", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.queries = append(s.queries, r.URL.RawQuery)
		s.auths = append(s.auths, r.Header.Get("Authorization"))
		s.mu.Unlock()
		s.op(w, r)
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *doctorStub) endpoint() string { return "tcp://" + s.srv.Listener.Addr().String() }

func (s *doctorStub) opQueries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...)
}

func writeOpReport(w http.ResponseWriter, rep opcheck.Report) {
	_ = json.NewEncoder(w).Encode(rep)
}

func intPtr(n int) *int { return &n }

// runDoctorCmd drives the real dispatcher with the given doctor flags.
func runDoctorCmd(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	setArgs(t, append([]string{"op-forward", "doctor"}, args...)...)
	out := captureStdio(t)
	err = Execute()
	stdout, stderr = out()
	return stdout, stderr, err
}

// runDoctorJSON runs doctor with --json and decodes the report.
func runDoctorJSON(t *testing.T, args ...string) (doctorReport, error) {
	t.Helper()
	stdout, stderr, err := runDoctorCmd(t, append([]string{"--json"}, args...)...)
	var rep doctorReport
	if jsonErr := json.Unmarshal([]byte(stdout), &rep); jsonErr != nil {
		t.Fatalf("--json output does not parse: %v\nstdout: %s\nstderr: %s", jsonErr, stdout, stderr)
	}
	return rep, err
}

func (r doctorReport) check(t *testing.T, id string) doctorCheck {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("report has no %q check; ids: %v", id, r.ids())
	return doctorCheck{}
}

func (r doctorReport) ids() []string {
	ids := make([]string, len(r.Checks))
	for i, c := range r.Checks {
		ids[i] = c.ID
	}
	return ids
}

// failing returns the ids of the checks that failed.
func (r doctorReport) failing() []string {
	var ids []string
	for _, c := range r.Checks {
		if c.Status == statusFail {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

func assertStatus(t *testing.T, rep doctorReport, id, want string) {
	t.Helper()
	if got := rep.check(t, id); got.Status != want {
		t.Errorf("%s status = %q (%s), want %q", id, got.Status, got.Summary, want)
	}
}

func assertExit(t *testing.T, err error, want int) {
	t.Helper()
	if want == 0 {
		if err != nil {
			t.Fatalf("doctor error = %v, want success", err)
		}
		return
	}
	var status ExitStatus
	if !errors.As(err, &status) || int(status) != want {
		t.Fatalf("doctor error = %v, want exit status %d", err, want)
	}
}

// ---------- The incident ----------

// THE INCIDENT, host role, end to end: whoami exits 1 with "account is not
// signed in" while an authenticated read succeeds. Doctor must report the
// session healthy and must never run whoami. This fails if anyone wires
// whoami in as the probe.
func TestDoctorHostSessionHealthyWhenWhoamiIsAFalseNegative(t *testing.T) {
	fx := newDoctorFixture(t)
	logPath := installFakeOp(t)
	ep := startRealDaemon(t)
	_ = fx

	rep, err := runDoctorJSON(t, "--listen", ep, "--interactive")

	assertExit(t, err, 0)
	if rep.Role != roleHost {
		t.Fatalf("role = %q, want host (a real op on PATH and no shim)", rep.Role)
	}
	assertStatus(t, rep, "op_session", statusOK)
	if got := rep.check(t, "op_session").Data["session"]; got != "healthy" {
		t.Errorf("op_session data session = %v, want healthy", got)
	}
	for _, call := range readLogLines(t, logPath) {
		if strings.HasPrefix(call, "whoami") {
			t.Fatalf("doctor invoked %q; whoami must never be the session probe", call)
		}
	}
}

// The same incident from a client machine: the verdict comes from the daemon's
// /health/op and must also be healthy.
func TestDoctorClientSessionHealthyWhenWhoamiIsAFalseNegative(t *testing.T) {
	fx := newDoctorFixture(t)
	fx.installShim(t)
	logPath := installFakeOp(t)
	ep := startRealDaemon(t)

	rep, err := runDoctorJSON(t, "--addr", ep, "--interactive")

	assertExit(t, err, 0)
	if rep.Role != roleClient {
		t.Fatalf("role = %q, want client (an op-forward shim is installed)", rep.Role)
	}
	op := rep.check(t, "daemon_op")
	if op.Status != statusOK || op.Data["session"] != "healthy" {
		t.Errorf("daemon_op = %q session=%v (%s), want ok/healthy", op.Status, op.Data["session"], op.Summary)
	}
	for _, call := range readLogLines(t, logPath) {
		if strings.HasPrefix(call, "whoami") {
			t.Fatalf("daemon ran %q; whoami must never be the session probe", call)
		}
	}
}

// ---------- Layer attribution ----------

// Inverse of the incident: the authenticated read genuinely fails on the host.
// Doctor reports a host-side session failure with the host action, and every
// layer of the chain is reported healthy rather than blamed.
func TestDoctorBlamesTheHostSessionNotTheChain(t *testing.T) {
	tests := []struct {
		name  string
		shim  bool
		flag  string
		check string
	}{
		{"host role", false, "--listen", "op_session"},
		{"client role", true, "--addr", "daemon_op"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newDoctorFixture(t)
			if tt.shim {
				fx.installShim(t)
			}
			installFakeOp(t)
			t.Setenv("FAKE_VAULT_EXIT", "1")
			t.Setenv("FAKE_VAULT_STDERR", "[ERROR] account is not signed in")
			ep := startRealDaemon(t)

			rep, err := runDoctorJSON(t, tt.flag, ep, "--interactive")

			assertExit(t, err, doctorExitFailed)
			if got := rep.failing(); len(got) != 1 || got[0] != tt.check {
				t.Fatalf("failing checks = %v, want only [%s]", got, tt.check)
			}
			failed := rep.check(t, tt.check)
			if !strings.Contains(failed.Summary, "host-side 1Password session") {
				t.Errorf("summary %q does not name the host-side session", failed.Summary)
			}
			if !strings.Contains(failed.Action, "1Password") {
				t.Errorf("action %q does not give the host action", failed.Action)
			}
			for _, layer := range []string{"endpoint", "daemon_health", "tokens"} {
				assertStatus(t, rep, layer, statusOK)
			}
		})
	}
}

// A daemon that cannot be reached is an endpoint/daemon-layer failure only.
// The host op layer must not be reported at all, since nothing was asked of it.
func TestDoctorUnreachableDaemonIsReportedAsTheEndpointLayer(t *testing.T) {
	fx := newDoctorFixture(t)
	fx.installShim(t)
	installFakeOp(t)

	rep, err := runDoctorJSON(t, "--addr", closedEndpoint(t))

	assertExit(t, err, doctorExitFailed)
	if got := rep.failing(); len(got) != 1 || got[0] != "endpoint" {
		t.Fatalf("failing checks = %v, want only [endpoint]", got)
	}
	if action := rep.check(t, "endpoint").Action; !strings.Contains(action, "SSH tunnel") {
		t.Errorf("endpoint action %q should mention starting the daemon and the SSH tunnel", action)
	}
	for _, id := range []string{"daemon_health", "daemon_op", "op_cli", "op_integration", "op_session"} {
		assertStatus(t, rep, id, statusSkip)
	}
}

func TestDoctorHostRoleUnreachableDaemonStillChecksTheLocalOp(t *testing.T) {
	newDoctorFixture(t)
	installFakeOp(t)

	rep, err := runDoctorJSON(t, "--listen", closedEndpoint(t))

	assertExit(t, err, doctorExitFailed)
	if got := rep.failing(); len(got) != 1 || got[0] != "endpoint" {
		t.Fatalf("failing checks = %v, want only [endpoint]", got)
	}
	if action := rep.check(t, "endpoint").Action; strings.Contains(action, "SSH") {
		t.Errorf("host-role action %q should not talk about an SSH tunnel", action)
	}
	assertStatus(t, rep, "op_cli", statusOK)
	assertStatus(t, rep, "op_integration", statusOK)
}

// Without an op binary the machine is a client: the host-side checks are
// skipped with a note, and their absence is not a failure.
func TestDoctorClientRoleWithoutOpIsNotAFailure(t *testing.T) {
	fx := newDoctorFixture(t)
	fx.installShim(t)
	stub := newDoctorStub(t)

	rep, err := runDoctorJSON(t, "--addr", stub.endpoint())

	assertExit(t, err, 0)
	if rep.Role != roleClient {
		t.Fatalf("role = %q, want client", rep.Role)
	}
	for _, id := range []string{"op_cli", "op_integration", "op_session"} {
		c := rep.check(t, id)
		if c.Status != statusSkip || !strings.Contains(c.Summary, "client role") {
			t.Errorf("%s = %q %q, want a skip noting the client role", id, c.Status, c.Summary)
		}
	}
	if rep.Status != statusOK {
		t.Errorf("overall status = %q, want ok", rep.Status)
	}
}

// ---------- Default is non-interactive ----------

// Without --interactive no authenticated command may run, the session check
// says it was skipped, and the report says how to run it.
func TestDoctorDefaultNeverRunsAnAuthenticatedProbe(t *testing.T) {
	newDoctorFixture(t)
	logPath := installFakeOp(t)
	ep := startRealDaemon(t)

	rep, err := runDoctorJSON(t, "--listen", ep)

	assertExit(t, err, 0)
	session := rep.check(t, "op_session")
	if session.Status != statusSkip {
		t.Errorf("op_session status = %q, want skip when the probe was not run", session.Status)
	}
	if !strings.Contains(session.Summary, "not run") || !strings.Contains(session.Action, "--interactive") {
		t.Errorf("op_session must state the probe was skipped and how to run it: %q / %q", session.Summary, session.Action)
	}
	daemonOp := rep.check(t, "daemon_op")
	if !strings.Contains(daemonOp.Action, "--interactive") {
		t.Errorf("daemon_op action %q should say how to run the probe", daemonOp.Action)
	}
	for _, call := range readLogLines(t, logPath) {
		if strings.HasPrefix(call, "vault") || strings.HasPrefix(call, "whoami") || strings.HasPrefix(call, "item") {
			t.Errorf("default doctor ran %q; only metadata commands are allowed", call)
		}
	}
}

// --interactive in the client role asks the daemon to probe; in the host role
// the local check probes and the daemon is asked for metadata only, so a
// single run raises at most one prompt.
func TestDoctorInteractiveProbesExactlyOnce(t *testing.T) {
	tests := []struct {
		name      string
		shim      bool
		flag      string
		wantQuery string
	}{
		{"client asks the daemon to probe", true, "--addr", "probe=1"},
		{"host probes locally and asks the daemon for metadata only", false, "--listen", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newDoctorFixture(t)
			if tt.shim {
				fx.installShim(t)
			} else {
				installFakeOp(t)
			}
			stub := newDoctorStub(t)

			_, err := runDoctorJSON(t, tt.flag, stub.endpoint(), "--interactive")

			assertExit(t, err, 0)
			queries := stub.opQueries()
			if len(queries) != 1 || queries[0] != tt.wantQuery {
				t.Errorf("/health/op queries = %q, want [%q]", queries, tt.wantQuery)
			}
		})
	}
}

// ---------- Daemon answers ----------

func TestDoctorClassifiesDaemonOpAnswers(t *testing.T) {
	failing := func(w http.ResponseWriter, status int) { http.Error(w, "x", status) }
	tests := []struct {
		name       string
		op         http.HandlerFunc
		wantStatus string
		wantText   string
	}{
		{"token rejected", func(w http.ResponseWriter, _ *http.Request) { failing(w, http.StatusUnauthorized) }, statusWarn, "rejected the access token"},
		{"daemon predates the route", func(w http.ResponseWriter, _ *http.Request) { failing(w, http.StatusNotFound) }, statusWarn, "predates /health/op"},
		{"probe already running", func(w http.ResponseWriter, _ *http.Request) { failing(w, http.StatusConflict) }, statusWarn, "already running"},
		{"server error", func(w http.ResponseWriter, _ *http.Request) { failing(w, http.StatusInternalServerError) }, statusFail, "HTTP 500"},
		{"unreadable body", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }, statusFail, "unreadable body"},
		{"connection dropped", func(w http.ResponseWriter, _ *http.Request) { panic(http.ErrAbortHandler) }, statusFail, "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newDoctorFixture(t)
			fx.installShim(t)
			stub := newDoctorStub(t)
			stub.op = tt.op

			rep, _ := runDoctorJSON(t, "--addr", stub.endpoint())

			c := rep.check(t, "daemon_op")
			if c.Status != tt.wantStatus || !strings.Contains(c.Summary, tt.wantText) {
				t.Errorf("daemon_op = %q %q, want %q containing %q", c.Status, c.Summary, tt.wantStatus, tt.wantText)
			}
			if c.Action == "" {
				t.Error("daemon_op gave no action")
			}
		})
	}
}

func TestDoctorWithoutAValidAccessTokenCannotAskTheDaemonButSaysSo(t *testing.T) {
	fx := newDoctorFixture(t)
	fx.installShim(t)
	fx.removeToken(t, auth.AccessTokenFile)
	stub := newDoctorStub(t)

	rep, err := runDoctorJSON(t, "--addr", stub.endpoint())

	assertExit(t, err, 0)
	c := rep.check(t, "daemon_op")
	if c.Status != statusWarn || !strings.Contains(c.Summary, "no valid access token") {
		t.Errorf("daemon_op = %q %q, want a warn about the missing access token", c.Status, c.Summary)
	}
	if n := len(stub.opQueries()); n != 0 {
		t.Errorf("doctor sent %d unauthenticated /health/op requests", n)
	}
	assertStatus(t, rep, "tokens", statusOK)
}

func TestDoctorFallsBackToTheLegacyTokenForTheDaemonQuery(t *testing.T) {
	fx := newDoctorFixture(t)
	fx.installShim(t)
	fx.removeToken(t, auth.AccessTokenFile)
	fx.writeToken(t, auth.LegacyTokenFile, doctorLegacyValue, time.Now().Add(time.Hour))
	stub := newDoctorStub(t)

	if _, err := runDoctorJSON(t, "--addr", stub.endpoint()); err != nil {
		t.Fatalf("doctor error: %v", err)
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.auths) != 1 || stub.auths[0] != "Bearer "+doctorLegacyValue {
		t.Errorf("/health/op authorization = %q, want the legacy token as bearer", stub.auths)
	}
}

func TestDoctorReportsAnUnhealthyHealthRoute(t *testing.T) {
	tests := []struct {
		name   string
		health http.HandlerFunc
		want   string
	}{
		{"non-ok status", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"starting"}`)) }, "without a healthy status"},
		{"http error", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "x", http.StatusServiceUnavailable) }, "HTTP 503"},
		{"not json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hello")) }, "without a healthy status"},
		{"connection dropped", func(w http.ResponseWriter, _ *http.Request) { panic(http.ErrAbortHandler) }, "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newDoctorFixture(t)
			fx.installShim(t)
			stub := newDoctorStub(t)
			stub.health = tt.health

			rep, err := runDoctorJSON(t, "--addr", stub.endpoint())

			assertExit(t, err, doctorExitFailed)
			c := rep.check(t, "daemon_health")
			if c.Status != statusFail || !strings.Contains(c.Summary, tt.want) {
				t.Errorf("daemon_health = %q %q, want a failure containing %q", c.Status, c.Summary, tt.want)
			}
		})
	}
}

// ---------- Shim ----------

func TestDoctorShimStates(t *testing.T) {
	// The table renders shims from the running version, so it must be pinned
	// before the table is built.
	withVersion(t, doctorTestVersion)
	const oldShim = "#!/bin/bash\n# op-forward shim — forwards op commands to host daemon via SSH tunnel.\n# Installed by op-forward. Do not edit directly.\n\nREAL_OP=\"/usr/bin/op\"\n"
	staleShim := strings.Replace(renderShim("/opt/op-forward", "/usr/bin/op"), "9.8.7-test", "0.0.1", 1)

	tests := []struct {
		name       string
		role       string
		content    string // empty means no file
		wantState  string
		wantStatus string
		wantText   string
	}{
		{"current", roleClient, renderShim("/opt/op-forward", "/usr/bin/op"), shimCurrent, statusOK, "current"},
		{"stale", roleClient, staleShim, shimStale, statusWarn, "0.0.1"},
		{"pre-stamping shim", roleClient, oldShim, shimUnstamped, statusWarn, "unknown version, pre-dates stamping"},
		{"not an op-forward shim on a client", roleClient, "#!/bin/sh\necho hi\n", shimNotShim, statusWarn, "not an op-forward shim"},
		{"no shim on a client", roleClient, "", shimAbsent, statusWarn, "no op shim"},
		{"real op in the shim location on a host", roleHost, "#!/bin/sh\necho hi\n", shimNotShim, statusOK, "real op"},
		{"no shim on a host", roleHost, "", shimAbsent, statusSkip, "expected on the daemon host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newDoctorFixture(t)
			if tt.content != "" {
				writeFile(t, fx.shim, tt.content, 0o755)
			}

			c := (&doctor{role: tt.role, shimPath: fx.shim}).checkShim()

			if c.Status != tt.wantStatus || c.Data["state"] != tt.wantState || !strings.Contains(c.Summary, tt.wantText) {
				t.Errorf("shim = %q/%v %q, want %q/%s containing %q", c.Status, c.Data["state"], c.Summary, tt.wantStatus, tt.wantState, tt.wantText)
			}
			if tt.wantState == shimStale || tt.wantState == shimUnstamped {
				if !strings.Contains(c.Action, "re-run op-forward install") {
					t.Errorf("action %q should say to re-run op-forward install", c.Action)
				}
			}
			if tt.wantStatus == statusWarn && c.Action == "" {
				t.Error("a warning must carry an action")
			}
		})
	}
}

// A stale or unstamped shim still forwards correctly, so it is a warning and
// the run still succeeds.
func TestDoctorStaleShimDoesNotFailTheRun(t *testing.T) {
	fx := newDoctorFixture(t)
	writeFile(t, fx.shim, strings.Replace(renderShim("/opt/op-forward", ""), "9.8.7-test", "0.0.1", 1), 0o755)
	stub := newDoctorStub(t)

	rep, err := runDoctorJSON(t, "--addr", stub.endpoint())

	assertExit(t, err, 0)
	assertStatus(t, rep, "shim", statusWarn)
	if rep.Status != statusWarn {
		t.Errorf("overall status = %q, want warn", rep.Status)
	}
}

func TestDoctorUnreadableShimIsAWarning(t *testing.T) {
	fx := newDoctorFixture(t)
	// A directory where the shim file should be makes the read fail with an
	// error other than "not exist".
	if err := os.MkdirAll(fx.shim, 0o755); err != nil {
		t.Fatal(err)
	}

	c := (&doctor{role: roleClient, shimPath: fx.shim}).checkShim()

	if c.Status != statusWarn || c.Data["state"] != shimUnreadable {
		t.Errorf("shim = %q/%v, want warn/unreadable", c.Status, c.Data["state"])
	}
}

// ---------- Tokens ----------

func TestDoctorTokenChainStates(t *testing.T) {
	future, past := time.Now().Add(time.Hour), time.Now().Add(-time.Hour)
	tests := []struct {
		name       string
		role       string
		access     *time.Time
		refresh    *time.Time
		legacy     *time.Time
		malformed  bool
		wantStatus string
		wantText   string
	}{
		{"all valid", roleClient, &future, &future, nil, false, statusOK, "access and refresh tokens are valid"},
		{"access expired, refresh valid", roleClient, &past, &future, nil, false, statusOK, "renews it"},
		{"access missing, refresh valid", roleClient, nil, &future, nil, false, statusOK, "renews it"},
		{"access valid, no refresh", roleClient, &future, nil, nil, false, statusWarn, "no valid refresh token"},
		{"access valid, refresh expired", roleClient, &future, &past, nil, false, statusWarn, "no valid refresh token"},
		{"legacy only", roleClient, nil, nil, &future, false, statusWarn, "no valid refresh token"},
		{"nothing", roleClient, nil, nil, nil, false, statusFail, "no usable token files"},
		{"everything expired", roleClient, &past, &past, &past, false, statusFail, "no usable token files"},
		{"malformed refresh file", roleClient, nil, nil, nil, true, statusFail, "no usable token files"},
		{"nothing on a host", roleHost, nil, nil, nil, false, statusFail, "no usable token files"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newDoctorFixture(t)
			for _, name := range []string{auth.AccessTokenFile, auth.RefreshTokenFile} {
				fx.removeToken(t, name)
			}
			for name, expires := range map[string]*time.Time{auth.AccessTokenFile: tt.access, auth.RefreshTokenFile: tt.refresh, auth.LegacyTokenFile: tt.legacy} {
				if expires != nil {
					fx.writeToken(t, name, "VALUE-"+name, *expires)
				}
			}
			if tt.malformed {
				writeFile(t, filepath.Join(fx.tokenDir, auth.RefreshTokenFile), "no-expiry-line", 0o600)
			}

			c := (&doctor{role: tt.role}).checkTokens()

			if c.Status != tt.wantStatus || !strings.Contains(c.Summary, tt.wantText) {
				t.Errorf("tokens = %q %q, want %q containing %q", c.Status, c.Summary, tt.wantStatus, tt.wantText)
			}
			if tt.wantStatus != statusOK && c.Action == "" {
				t.Error("a non-ok token check must carry an action")
			}
			if tt.role == roleHost && !strings.Contains(c.Action, "op-forward serve") {
				t.Errorf("host action %q should say to start the daemon", c.Action)
			}
			encoded, _ := json.Marshal(c)
			if strings.Contains(string(encoded), "VALUE-") {
				t.Errorf("token check leaks a token value: %s", encoded)
			}
		})
	}
}

// ---------- Rendering, flags, and the report contract ----------

// The JSON report is consumed by automation: the top-level keys, the check
// ids and their order, and the per-check keys are a stable contract.
func TestDoctorJSONShapeIsStable(t *testing.T) {
	fx := newDoctorFixture(t)
	fx.installShim(t)
	stub := newDoctorStub(t)

	stdout, _, err := runDoctorCmd(t, "--json", "--addr", stub.endpoint())

	assertExit(t, err, 0)
	var doc map[string]any
	if jsonErr := json.Unmarshal([]byte(stdout), &doc); jsonErr != nil {
		t.Fatalf("--json output does not parse: %v\n%s", jsonErr, stdout)
	}
	wantTop := []string{"version", "role", "endpoint", "interactive", "status", "exit_code", "checks"}
	if len(doc) != len(wantTop) {
		t.Errorf("top-level has %d keys, want exactly %v: %v", len(doc), wantTop, doc)
	}
	for _, key := range wantTop {
		if _, ok := doc[key]; !ok {
			t.Errorf("top-level key %q is missing", key)
		}
	}
	if doc["version"] != doctorTestVersion || doc["role"] != "client" || doc["endpoint"] != stub.endpoint() {
		t.Errorf("version/role/endpoint = %v/%v/%v", doc["version"], doc["role"], doc["endpoint"])
	}

	var rep doctorReport
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatal(err)
	}
	wantIDs := "binary shim endpoint daemon_health daemon_op tokens op_cli op_integration op_session"
	if got := strings.Join(rep.ids(), " "); got != wantIDs {
		t.Errorf("check ids = %q, want %q", got, wantIDs)
	}
	valid := map[string]bool{statusOK: true, statusWarn: true, statusFail: true, statusSkip: true}
	for _, c := range rep.Checks {
		if !valid[c.Status] || c.Summary == "" {
			t.Errorf("check %s has status %q and summary %q", c.ID, c.Status, c.Summary)
		}
	}
	daemonOp := rep.check(t, "daemon_op")
	for _, key := range []string{"op_present", "op_version", "integration", "account_count", "session", "session_reason"} {
		if _, ok := daemonOp.Data[key]; !ok {
			t.Errorf("daemon_op data is missing %q: %v", key, daemonOp.Data)
		}
	}
}

// No token value, vault name, or account identifier may appear anywhere in
// either output format, whatever the daemon and op return.
func TestDoctorOutputNeverContainsSecrets(t *testing.T) {
	fx := newDoctorFixture(t)
	fx.installShim(t)
	fx.writeToken(t, auth.LegacyTokenFile, doctorLegacyValue, time.Now().Add(time.Hour))
	installFakeOp(t)
	t.Setenv("FAKE_VAULT_EXIT", "1")
	t.Setenv("FAKE_VAULT_STDERR", "[ERROR] account is not signed in "+doctorAccessValue+" "+doctorRefreshValue)
	ep := startRealDaemon(t)

	for _, format := range [][]string{{"--json"}, {}} {
		args := append(append([]string{}, format...), "--addr", ep, "--interactive")
		stdout, stderr, _ := runDoctorCmd(t, args...)
		for _, secret := range []string{doctorAccessValue, doctorRefreshValue, doctorLegacyValue, "SECRET-VAULT-NAME", "my.1password.com"} {
			if strings.Contains(stdout+stderr, secret) {
				t.Errorf("output %v leaks %q:\n%s", format, secret, stdout)
			}
		}
		// Partial leaks count too: no 8-character window of any token.
		for _, secret := range []string{doctorAccessValue, doctorRefreshValue} {
			if strings.Contains(stdout+stderr, secret[:12]) {
				t.Errorf("output %v leaks a prefix of a token", format)
			}
		}
	}
}

func TestDoctorHumanReportShowsStatusesAndActions(t *testing.T) {
	fx := newDoctorFixture(t)
	fx.installShim(t)
	fx.removeToken(t, auth.AccessTokenFile)
	fx.removeToken(t, auth.RefreshTokenFile)

	stdout, stderr, err := runDoctorCmd(t, "--addr", closedEndpoint(t))

	assertExit(t, err, doctorExitFailed)
	if stderr != "" {
		t.Errorf("report must go to stdout only, stderr was %q", stderr)
	}
	for _, want := range []string{
		"op-forward doctor " + doctorTestVersion,
		"role: client",
		"[ok  ] binary",
		"[FAIL] endpoint",
		"[FAIL] tokens",
		"[skip] daemon_health",
		"-> start the daemon",
		"Result: fail (2 failure(s), 0 warning(s))",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("report is missing %q:\n%s", want, stdout)
		}
	}
}

func TestDoctorReportsAnInvalidEndpointWithoutProbing(t *testing.T) {
	fx := newDoctorFixture(t)
	fx.installShim(t)

	stdout, _, err := runDoctorCmd(t, "--addr", "not-an-endpoint")

	assertExit(t, err, doctorExitFailed)
	if !strings.Contains(stdout, "invalid endpoint") || !strings.Contains(stdout, "endpoint: (unresolved)") {
		t.Errorf("report does not flag the invalid endpoint:\n%s", stdout)
	}
	jsonRep, _ := runDoctorJSON(t, "--addr", "not-an-endpoint")
	if jsonRep.Endpoint != "" {
		t.Errorf("endpoint = %q, want empty for an unresolvable endpoint", jsonRep.Endpoint)
	}
}

// Endpoint resolution follows the existing commands: a client dials --addr
// or --host/--port, a host binds --listen or --port, and the client-only
// OP_FORWARD_ADDR environment variable is ignored on a host.
func TestDoctorEndpointResolution(t *testing.T) {
	tests := []struct {
		name string
		shim bool
		env  map[string]string
		args []string
		want string
	}{
		{"client defaults to the loopback default port", true, nil, nil, "tcp://127.0.0.1:" + strconv.Itoa(endpoint.DefaultPort)},
		{"client --host and --port", true, nil, []string{"--host", "127.0.0.2", "--port", "4101"}, "tcp://127.0.0.2:4101"},
		{"client --addr", true, nil, []string{"--addr", "unix:///tmp/op.sock"}, "unix:///tmp/op.sock"},
		{"client environment endpoint", true, map[string]string{endpoint.EnvAddr: "tcp://127.0.0.1:4102"}, nil, "tcp://127.0.0.1:4102"},
		{"host --listen", false, nil, []string{"--listen", "tcp://127.0.0.1:4103"}, "tcp://127.0.0.1:4103"},
		{"host --port", false, nil, []string{"--port", "4104"}, "tcp://127.0.0.1:4104"},
		{"host --addr", false, nil, []string{"--addr", "tcp://127.0.0.1:4105"}, "tcp://127.0.0.1:4105"},
		{"host ignores the client environment endpoint", false, map[string]string{endpoint.EnvAddr: "tcp://127.0.0.1:4106"}, []string{"--port", "4107"}, "tcp://127.0.0.1:4107"},
		{"host environment listen endpoint", false, map[string]string{endpoint.EnvListen: "tcp://127.0.0.1:4108"}, nil, "tcp://127.0.0.1:4108"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newDoctorFixture(t)
			if tt.shim {
				fx.installShim(t)
			} else {
				installFakeOp(t)
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			rep, _ := runDoctorJSON(t, tt.args...)

			if rep.Endpoint != tt.want {
				t.Errorf("endpoint = %q, want %q", rep.Endpoint, tt.want)
			}
		})
	}
}

func TestDoctorRoleSelection(t *testing.T) {
	t.Run("an explicit role wins", func(t *testing.T) {
		fx := newDoctorFixture(t)
		fx.installShim(t)
		installFakeOp(t)
		if got := resolveRole(roleHost, fx.shim); got != roleHost {
			t.Errorf("role = %q, want host", got)
		}
		if got := resolveRole(roleClient, fx.shim); got != roleClient {
			t.Errorf("role = %q, want client", got)
		}
	})
	t.Run("a shim marks a client even when a real op exists", func(t *testing.T) {
		fx := newDoctorFixture(t)
		fx.installShim(t)
		installFakeOp(t)
		if got := resolveRole(roleAuto, fx.shim); got != roleClient {
			t.Errorf("role = %q, want client", got)
		}
	})
	t.Run("a real op without a shim marks the host", func(t *testing.T) {
		fx := newDoctorFixture(t)
		installFakeOp(t)
		if got := resolveRole(roleAuto, fx.shim); got != roleHost {
			t.Errorf("role = %q, want host", got)
		}
	})
	t.Run("neither marks a client", func(t *testing.T) {
		fx := newDoctorFixture(t)
		if got := resolveRole(roleAuto, fx.shim); got != roleClient {
			t.Errorf("role = %q, want client", got)
		}
	})
	t.Run("--role host forces the host checks and reports a missing op", func(t *testing.T) {
		newDoctorFixture(t)
		rep, err := runDoctorJSON(t, "--role", "host", "--listen", closedEndpoint(t))
		assertExit(t, err, doctorExitFailed)
		assertStatus(t, rep, "op_cli", statusFail)
		assertStatus(t, rep, "op_integration", statusSkip)
	})
}

func TestDoctorUsageErrors(t *testing.T) {
	newDoctorFixture(t)

	_, stderr, err := runDoctorCmd(t, "--role", "bogus")
	assertExit(t, err, doctorExitUsage)
	if !strings.Contains(stderr, `invalid --role "bogus"`) {
		t.Errorf("stderr = %q, want the invalid role named", stderr)
	}

	_, _, err = runDoctorCmd(t, "--no-such-flag")
	assertExit(t, err, doctorExitUsage)

	// A help request ends the process successfully without running checks.
	_, _, err = runDoctorCmd(t, "-h")
	var status ExitStatus
	if !errors.As(err, &status) || status != 0 {
		t.Errorf("doctor -h error = %v, want ExitStatus(0)", err)
	}
}

// ---------- Op classification ----------

func TestDescribeOpClassification(t *testing.T) {
	two := 2
	tests := []struct {
		name       string
		rep        opcheck.Report
		cliStatus  string
		intStatus  string
		sessStatus string
		sessText   string
	}{
		{
			"no op", opcheck.Report{Integration: opcheck.IntegrationUnavailable, Session: opcheck.SessionNotProbed},
			statusFail, statusSkip, statusSkip, "not run",
		},
		{
			"op that cannot report a version",
			opcheck.Report{OpPresent: true, Integration: opcheck.IntegrationReachable, AccountCount: &two, Session: opcheck.SessionNotProbed},
			statusFail, statusOK, statusSkip, "not run",
		},
		{
			"integration unreachable",
			opcheck.Report{OpPresent: true, OpVersion: "2.41.0", Integration: opcheck.IntegrationUnreachable, Session: opcheck.SessionNotProbed},
			statusOK, statusFail, statusSkip, "not run",
		},
		{
			"reachable with no accounts",
			opcheck.Report{OpPresent: true, OpVersion: "2.41.0", Integration: opcheck.IntegrationReachable, AccountCount: intPtr(0), Session: opcheck.SessionNotProbed},
			statusOK, statusWarn, statusSkip, "not run",
		},
		{
			"reachable with an unknown account count",
			opcheck.Report{OpPresent: true, OpVersion: "2.41.0", Integration: opcheck.IntegrationReachable, Session: opcheck.SessionNotProbed},
			statusOK, statusOK, statusSkip, "not run",
		},
		{
			"healthy session",
			opcheck.Report{OpPresent: true, OpVersion: "2.41.0", Integration: opcheck.IntegrationReachable, AccountCount: &two, Session: opcheck.SessionHealthy},
			statusOK, statusOK, statusOK, "healthy",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli, integ, sess := describeOp(tt.rep, "the daemon host")
			if cli.status != tt.cliStatus || integ.status != tt.intStatus || sess.status != tt.sessStatus {
				t.Errorf("statuses = %s/%s/%s, want %s/%s/%s", cli.status, integ.status, sess.status, tt.cliStatus, tt.intStatus, tt.sessStatus)
			}
			if !strings.Contains(sess.summary, tt.sessText) {
				t.Errorf("session summary %q does not contain %q", sess.summary, tt.sessText)
			}
		})
	}
}

func TestDescribeSessionFailureReasonsNameTheHostAction(t *testing.T) {
	for _, reason := range []string{
		opcheck.ReasonNotSignedIn, opcheck.ReasonAuthDenied, opcheck.ReasonAppLocked,
		opcheck.ReasonTimeout, opcheck.ReasonOpError, "a_reason_added_by_a_newer_daemon",
	} {
		t.Run(reason, func(t *testing.T) {
			rep := opcheck.Report{OpPresent: true, OpVersion: "2.41.0", Session: opcheck.SessionFailed, SessionReason: reason}

			sess := describeSession(rep, "this host")

			if sess.status != statusFail {
				t.Errorf("status = %q, want fail", sess.status)
			}
			if !strings.Contains(sess.summary, "host-side 1Password session") || !strings.Contains(sess.summary, "chain delivered the request") {
				t.Errorf("summary %q should attribute the failure to the host and clear the chain", sess.summary)
			}
			if sess.action == "" {
				t.Error("a failed session must carry a host action")
			}
		})
	}
}

// ---------- Helpers ----------

func TestDoctorGetFailureModes(t *testing.T) {
	t.Run("invalid request", func(t *testing.T) {
		ep := endpoint.TCP("127.0.0.1", 1)
		if _, _, err := doctorGet(ep, "/bad\x7fpath", "", time.Second); err == nil {
			t.Error("doctorGet accepted an invalid URL")
		}
	})
	t.Run("truncated body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "1000")
			_, _ = w.Write([]byte("short"))
			w.(http.Flusher).Flush()
		}))
		t.Cleanup(srv.Close)
		ep := endpoint.Endpoint{Network: "tcp", Address: srv.Listener.Addr().String()}
		if _, _, err := doctorGet(ep, "/health", "", time.Second); err == nil {
			t.Error("doctorGet reported success for a truncated body")
		}
	})
	t.Run("bearer is sent only when given", func(t *testing.T) {
		var got []string
		srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got = append(got, r.Header.Get("Authorization"))
		}))
		t.Cleanup(srv.Close)
		ep := endpoint.Endpoint{Network: "tcp", Address: srv.Listener.Addr().String()}
		_, _, _ = doctorGet(ep, "/x", "", time.Second)
		_, _, _ = doctorGet(ep, "/x", "tok", time.Second)
		if len(got) != 2 || got[0] != "" || got[1] != "Bearer tok" {
			t.Errorf("authorization headers = %q", got)
		}
	})
}

func TestFoldOpComponentsPicksTheMostSevereAndKeepsTheProbeNote(t *testing.T) {
	rep := opcheck.Report{}
	ok := opComponent{statusOK, "fine", ""}
	skip := opComponent{statusSkip, "skipped", "run it with --interactive"}
	warn := opComponent{statusWarn, "hmm", "check it"}
	fail := opComponent{statusFail, "broken", "fix it"}

	if c := foldOpComponents("x", rep, ok, ok, skip); c.Status != statusOK || c.Action != "run it with --interactive" {
		t.Errorf("ok+skip = %q %q, want ok carrying the probe note", c.Status, c.Action)
	}
	if c := foldOpComponents("x", rep, ok, warn, skip); c.Status != statusWarn || c.Action != "check it" {
		t.Errorf("warn = %q %q, want warn with its own action", c.Status, c.Action)
	}
	if c := foldOpComponents("x", rep, warn, fail, skip); c.Status != statusFail || c.Action != "fix it" {
		t.Errorf("fail = %q %q, want fail with its own action", c.Status, c.Action)
	}
	if c := foldOpComponents("x", rep, skip, skip); c.Status != statusSkip {
		t.Errorf("all skipped = %q, want skip", c.Status)
	}
}

func TestDoctorHumanReportCountsWarnings(t *testing.T) {
	fx := newDoctorFixture(t)
	writeFile(t, fx.shim, strings.Replace(renderShim("/opt/op-forward", ""), doctorTestVersion, "0.0.1", 1), 0o755)
	stub := newDoctorStub(t)

	stdout, _, err := runDoctorCmd(t, "--addr", stub.endpoint())

	assertExit(t, err, 0)
	for _, want := range []string{"[WARN] shim", "-> re-run op-forward install", "Result: warn (0 failure(s), 1 warning(s))"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("report is missing %q:\n%s", want, stdout)
		}
	}
}

// ---------- Locked app ----------

// The measured symptom matrix of a locked 1Password app: metadata answers, the
// authenticated read hangs silently, and whoami would say "not signed in".
// Doctor must name the real cause and the real action (unlock or approve on
// the host) and must not call it a signed-out account or blame the chain.
func TestDoctorHostReportsALockedAppFromTheSilentProbe(t *testing.T) {
	fx := newDoctorFixture(t)
	logPath := installFakeOp(t)
	t.Setenv("FAKE_VAULT_SLEEP", "30")
	d := &doctor{role: roleHost, interactive: true, shimPath: fx.shim, probeTimeout: 300 * time.Millisecond}
	out := captureStdio(t)

	checks := d.checkLocalOp()
	_, stderr := out()

	var session doctorCheck
	for _, c := range checks {
		if c.ID == "op_session" {
			session = c
		}
	}
	if session.Status != statusFail || session.Data["session_reason"] != "app_locked" {
		t.Fatalf("op_session = %q reason=%v (%s), want fail/app_locked", session.Status, session.Data["session_reason"], session.Summary)
	}
	if !strings.Contains(session.Summary, "locked or an approval is pending") || strings.Contains(session.Summary, "not signed in") {
		t.Errorf("summary %q should name a locked app, not a signed-out account", session.Summary)
	}
	if !strings.Contains(session.Action, "unlock the 1Password app on the host") {
		t.Errorf("action %q should tell the operator to unlock the app on the host", session.Action)
	}
	if !strings.Contains(stderr, "waiting up to 300ms") || !strings.Contains(stderr, "unlock or approve it there now") {
		t.Errorf("stderr lacks the waiting-for-approval notice: %q", stderr)
	}
	for _, call := range readLogLines(t, logPath) {
		if strings.HasPrefix(call, "whoami") {
			t.Fatalf("doctor invoked %q", call)
		}
	}
}

// Client role: the daemon reports app_locked, and doctor fails only the
// daemon_op check with the unlock advice while the chain stays green.
func TestDoctorClientReportsALockedAppFromTheDaemon(t *testing.T) {
	fx := newDoctorFixture(t)
	fx.installShim(t)
	stub := newDoctorStub(t)
	stub.op = func(w http.ResponseWriter, _ *http.Request) {
		writeOpReport(w, opcheck.Report{
			OpPresent: true, OpVersion: "2.41.0",
			Integration: opcheck.IntegrationReachable, AccountCount: intPtr(2),
			Session: opcheck.SessionFailed, SessionReason: opcheck.ReasonAppLocked,
		})
	}

	rep, err := runDoctorJSON(t, "--addr", stub.endpoint(), "--interactive")

	assertExit(t, err, doctorExitFailed)
	if got := rep.failing(); len(got) != 1 || got[0] != "daemon_op" {
		t.Fatalf("failing checks = %v, want only [daemon_op]", got)
	}
	c := rep.check(t, "daemon_op")
	if !strings.Contains(c.Summary, "locked or an approval is pending") || !strings.Contains(c.Action, "unlock the 1Password app on the host") {
		t.Errorf("daemon_op = %q / %q, want the locked-app explanation and action", c.Summary, c.Action)
	}
}

// --interactive announces the wait on stderr in both formats, leaving stdout
// valid JSON; the default run announces nothing because it never waits.
func TestDoctorAnnouncesTheApprovalWaitOnlyWhenInteractive(t *testing.T) {
	fx := newDoctorFixture(t)
	fx.installShim(t)
	stub := newDoctorStub(t)

	stdout, stderr, err := runDoctorCmd(t, "--json", "--addr", stub.endpoint(), "--interactive")
	assertExit(t, err, 0)
	if !json.Valid([]byte(stdout)) {
		t.Errorf("stdout is not valid JSON with --interactive:\n%s", stdout)
	}
	if want := "waiting up to " + opcheck.DefaultProbeTimeout.String(); !strings.Contains(stderr, want) {
		t.Errorf("stderr %q does not contain %q", stderr, want)
	}

	_, stderr, err = runDoctorCmd(t, "--addr", stub.endpoint())
	assertExit(t, err, 0)
	if stderr != "" {
		t.Errorf("a default run wrote to stderr: %q", stderr)
	}
}
