//go:build !windows

package cmd

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ekovshilovsky/op-forward/internal/auth"
	"github.com/ekovshilovsky/op-forward/internal/endpoint"
)

// ---------- Configuration and token path helpers ----------

func TestGetProxyTimeout(t *testing.T) {
	t.Setenv("OP_FORWARD_FETCH_TIMEOUT_MS", "")
	if got := getProxyTimeout(); got != 60000 {
		t.Fatalf("default proxy timeout = %d, want 60000", got)
	}

	t.Setenv("OP_FORWARD_FETCH_TIMEOUT_MS", "2500")
	if got := getProxyTimeout(); got != 2500 {
		t.Fatalf("proxy timeout = %d, want 2500", got)
	}

	// Non-numeric, zero, and negative values must revert to the default. A
	// zero timeout would reach the HTTP layer as "no deadline" and let a hung
	// daemon block the shim indefinitely.
	for _, raw := range []string{"later", "0", "-1"} {
		t.Setenv("OP_FORWARD_FETCH_TIMEOUT_MS", raw)
		if got := getProxyTimeout(); got != 60000 {
			t.Fatalf("proxy timeout with %q = %d, want the 60000 default", raw, got)
		}
	}
}

func TestExplicitFlagsReportsOnlyTypedFlags(t *testing.T) {
	newSet := func() *flag.FlagSet {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.String("host", "127.0.0.1", "")
		fs.Int("port", 18340, "")
		return fs
	}

	fs := newSet()
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if got := explicitFlags(fs); len(got) != 0 {
		t.Fatalf("explicitFlags with nothing typed = %v, want empty", got)
	}

	fs = newSet()
	if err := fs.Parse([]string{"-host", "example.test"}); err != nil {
		t.Fatal(err)
	}
	got := explicitFlags(fs)
	if !got["host"] || got["port"] || len(got) != 1 {
		t.Fatalf("explicitFlags after typing only --host = %v, want exactly {host}", got)
	}
}

func TestProxyTokenPathUnderTokenDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OP_FORWARD_TOKEN_FILE", "")
	t.Setenv("OP_FORWARD_TOKEN_DIR", dir)

	for _, name := range []string{auth.AccessTokenFile, auth.RefreshTokenFile, auth.LegacyTokenFile} {
		if got, want := proxyTokenPath(name), filepath.Join(dir, name); got != want {
			t.Errorf("proxyTokenPath(%q) = %q, want %q", name, got, want)
		}
	}
}

// With no override variables set, the proxy must place all three token files
// in the per-user cache directory under the op-forward folder, the same place
// the daemon writes them. A mismatch would leave the proxy reading files the
// daemon never wrote.
func TestProxyTokenPathUnderDefaultHome(t *testing.T) {
	t.Setenv("OP_FORWARD_TOKEN_FILE", "")
	t.Setenv("OP_FORWARD_TOKEN_DIR", "")
	home := tempHome(t)

	cacheRoot := filepath.Join(home, ".cache")
	if runtime.GOOS == "darwin" {
		cacheRoot = filepath.Join(home, "Library", "Caches")
	}
	for _, name := range []string{auth.AccessTokenFile, auth.RefreshTokenFile, auth.LegacyTokenFile} {
		want := filepath.Join(cacheRoot, auth.CacheDirName, name)
		if got := proxyTokenPath(name); got != want {
			t.Errorf("proxyTokenPath(%q) = %q, want %q", name, got, want)
		}
	}
}

// ---------- Token file reading ----------

func expiryString(offset time.Duration) string {
	return time.Now().Add(offset).UTC().Format(time.RFC3339)
}

func TestReadToken(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		writeFile(t, path, content, 0o600)
		return path
	}

	cases := []struct {
		name      string
		content   string
		wantValue string
		wantValid bool
	}{
		{"future expiry is valid", "tok-future\n" + expiryString(time.Hour) + "\n", "tok-future", true},
		{"past expiry returns the value but is invalid", "tok-past\n" + expiryString(-time.Hour) + "\n", "tok-past", false},
		{"single line has no expiry so is invalid", "tok-bare\n", "tok-bare", false},
		{"single line without trailing newline", "tok-bare", "tok-bare", false},
		{"garbage expiry returns the value but is invalid", "tok-garbage\nnot-a-timestamp\n", "tok-garbage", false},
		{"empty file yields nothing", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotValue, gotValid := readToken(write(strings.ReplaceAll(tc.name, " ", "_")+".token", tc.content))
			if gotValue != tc.wantValue || gotValid != tc.wantValid {
				t.Fatalf("readToken() = (%q, %v), want (%q, %v)", gotValue, gotValid, tc.wantValue, tc.wantValid)
			}
		})
	}

	t.Run("missing file yields nothing", func(t *testing.T) {
		gotValue, gotValid := readToken(filepath.Join(dir, "absent.token"))
		if gotValue != "" || gotValid {
			t.Fatalf("readToken(missing) = (%q, %v), want (\"\", false)", gotValue, gotValid)
		}
	})
}

// readTokenValue deliberately ignores expiry: the proxy needs the refresh
// token value even when its recorded expiry has passed, so the daemon can make
// the final call on whether it is still acceptable.
func TestReadTokenValue(t *testing.T) {
	dir := t.TempDir()
	expired := filepath.Join(dir, "expired.token")
	writeFile(t, expired, "tok-expired\n"+expiryString(-time.Hour)+"\n", 0o600)

	if got := readTokenValue(expired); got != "tok-expired" {
		t.Fatalf("readTokenValue(expired) = %q, want the stored value", got)
	}
	if got := readTokenValue(filepath.Join(dir, "absent.token")); got != "" {
		t.Fatalf("readTokenValue(missing) = %q, want empty", got)
	}
}

// ---------- Token file writing ----------

func TestSaveTokenFileWritesAtomicallyWithRestrictedModes(t *testing.T) {
	withUmask(t, 0o022)
	// The parent does not exist, so saveTokenFile must create it with the
	// owner-only mode used for the token directory.
	parent := filepath.Join(t.TempDir(), "cache", "op-forward")
	path := filepath.Join(parent, "access.token")
	expires := expiryString(time.Hour)

	out := captureStdio(t)
	saveTokenFile(path, "tok-value", expires)
	stdout, stderr := out()
	if stdout != "" || stderr != "" {
		t.Fatalf("a successful save must be silent; stdout=%q stderr=%q", stdout, stderr)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "tok-value\n" + expires + "\n"; string(data) != want {
		t.Fatalf("token file contents = %q, want %q", data, want)
	}
	assertMode(t, path, 0o600)
	assertMode(t, parent, 0o700)
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("staging file %s.tmp must not remain after a successful save (stat err: %v)", path, err)
	}

	// The saved file must round-trip through the reader the proxy uses.
	if value, valid := readToken(path); value != "tok-value" || !valid {
		t.Fatalf("readToken(saved) = (%q, %v), want (\"tok-value\", true)", value, valid)
	}

	// Saving again replaces the previous contents.
	saveTokenFile(path, "tok-second", expires)
	if value := readTokenValue(path); value != "tok-second" {
		t.Fatalf("second save left value %q, want tok-second", value)
	}
}

// A rename that cannot complete must not damage what is already on disk and
// must not leave the staging file behind. Pointing the destination at a
// non-empty directory forces rename to fail after the staging write succeeded.
func TestSaveTokenFileFailedRenameCleansUpAndWarns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.token")
	survivor := filepath.Join(path, "keep")
	writeFile(t, survivor, "untouched", 0o600)

	out := captureStdio(t)
	saveTokenFile(path, "tok-value", expiryString(time.Hour))
	_, stderr := out()

	if !strings.Contains(stderr, "could not cache token at "+path) {
		t.Fatalf("expected a cache warning naming %s, got %q", path, stderr)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("staging file must be removed after a failed rename (stat err: %v)", err)
	}
	if data, err := os.ReadFile(survivor); err != nil || string(data) != "untouched" {
		t.Fatalf("pre-existing content was damaged: %q, %v", data, err)
	}
}

// If the staging file cannot be written, the previously cached token must
// survive unchanged. A directory squatting on the staging path makes the write
// fail without depending on filesystem permissions.
func TestSaveTokenFileFailedStagingWriteKeepsPreviousToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.token")
	previous := "tok-old\n" + expiryString(time.Hour) + "\n"
	writeFile(t, path, previous, 0o600)
	if err := os.Mkdir(path+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}

	out := captureStdio(t)
	saveTokenFile(path, "tok-new", expiryString(2*time.Hour))
	_, stderr := out()

	if !strings.Contains(stderr, "could not cache token at "+path) {
		t.Fatalf("expected a cache warning, got %q", stderr)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != previous {
		t.Fatalf("previous token changed after a failed save: %q, %v", data, err)
	}
}

func TestSaveTokenFileWarnsWhenParentCannotBeCreated(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	writeFile(t, blocker, "a regular file", 0o600)
	path := filepath.Join(blocker, "sub", "access.token")

	out := captureStdio(t)
	saveTokenFile(path, "tok-value", expiryString(time.Hour))
	_, stderr := out()

	if !strings.Contains(stderr, "could not cache token at "+path) {
		t.Fatalf("expected a cache warning, got %q", stderr)
	}
	if data, err := os.ReadFile(blocker); err != nil || string(data) != "a regular file" {
		t.Fatalf("blocking file was modified: %q, %v", data, err)
	}
}

// ---------- Operator-facing error messages ----------

func TestPrintNoTokenErrorGivesRecoveryHint(t *testing.T) {
	out := captureStdio(t)
	printNoTokenError()
	stdout, stderr := out()

	if stdout != "" {
		t.Fatalf("diagnostics must go to stderr, stdout was %q", stdout)
	}
	for _, want := range []string{
		"no authentication token found",
		"scp host:~/Library/Caches/op-forward/refresh.token ~/.cache/op-forward/refresh.token",
		"NOT a 1Password issue",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("message is missing %q:\n%s", want, stderr)
		}
	}
}

func TestPrintRefreshFailedErrorIncludesCauseAndRecoveryHint(t *testing.T) {
	out := captureStdio(t)
	printRefreshFailedError(errors.New("daemon said no"))
	stdout, stderr := out()

	if stdout != "" {
		t.Fatalf("diagnostics must go to stderr, stdout was %q", stdout)
	}
	for _, want := range []string{
		"token refresh failed",
		"daemon said no",
		"scp host:~/Library/Caches/op-forward/refresh.token ~/.cache/op-forward/refresh.token",
		"NOT a 1Password authentication issue",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("message is missing %q:\n%s", want, stderr)
		}
	}
}

// ---------- executeWithAuth ----------

type recordedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// requestRecorder captures the requests an httptest handler receives so the
// test can assert on exactly what went over the wire.
type requestRecorder struct {
	mu   sync.Mutex
	reqs []recordedRequest
}

func (r *requestRecorder) handler(status int, body string, headers map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		data, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.reqs = append(r.reqs, recordedRequest{Method: req.Method, Path: req.URL.Path, Header: req.Header.Clone(), Body: data})
		r.mu.Unlock()
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func (r *requestRecorder) only(t *testing.T) recordedRequest {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reqs) != 1 {
		t.Fatalf("server saw %d requests, want exactly 1", len(r.reqs))
	}
	return r.reqs[0]
}

func serverEndpoint(t *testing.T, srv *httptest.Server) endpoint.Endpoint {
	t.Helper()
	ep, err := endpoint.Parse("tcp://" + srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return ep
}

// truncatingServer advertises a body longer than it sends, then drops the
// connection, so the client's body read fails after the headers were accepted.
func truncatingServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(status)
		_, _ = w.Write([]byte("partial"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The daemon gates old clients on X-Client-Version and treats a missing header
// as "unknown, allow". A dropped header would therefore silently disable the
// version gate, which is why every header is asserted here.
func TestExecuteWithAuthSendsRequiredHeaders(t *testing.T) {
	withVersion(t, "9.8.7-test")
	rec := &requestRecorder{}
	srv := httptest.NewServer(rec.handler(http.StatusOK, `{"stdout":"ok"}`, nil))
	t.Cleanup(srv.Close)
	ep := serverEndpoint(t, srv)

	payload := []byte(`{"args":["whoami"],"timeout_ms":1000}`)
	resp, body, err := executeWithAuth(ep.HTTPClient(5*time.Second), ep, payload, "access-secret")
	if err != nil {
		t.Fatalf("executeWithAuth() error: %v", err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != `{"stdout":"ok"}` {
		t.Fatalf("response = %d %q, want 200 with the server body", resp.StatusCode, body)
	}

	got := rec.only(t)
	if got.Method != http.MethodPost || got.Path != "/op/execute" {
		t.Errorf("request = %s %s, want POST /op/execute", got.Method, got.Path)
	}
	wantHeaders := map[string]string{
		"Authorization":    "Bearer access-secret",
		"X-Client-Version": "9.8.7-test",
		"User-Agent":       "op-forward/9.8.7-test",
		"Content-Type":     "application/json",
	}
	for name, want := range wantHeaders {
		if have := got.Header.Get(name); have != want {
			t.Errorf("header %s = %q, want %q", name, have, want)
		}
	}
	if !bytes.Equal(got.Body, payload) {
		t.Errorf("request body = %q, want %q", got.Body, payload)
	}
}

// Non-200 statuses are not errors at this layer: the caller inspects the
// status to decide between refresh (401), upgrade (426), and relaying output.
func TestExecuteWithAuthRelaysStatusHeadersAndBody(t *testing.T) {
	rec := &requestRecorder{}
	srv := httptest.NewServer(rec.handler(http.StatusUnauthorized, "token expired", map[string]string{"X-Update-Available": "2.0.0"}))
	t.Cleanup(srv.Close)
	ep := serverEndpoint(t, srv)

	resp, body, err := executeWithAuth(ep.HTTPClient(5*time.Second), ep, []byte(`{}`), "tok")
	if err != nil {
		t.Fatalf("a 401 must be relayed, not returned as an error: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if string(body) != "token expired" {
		t.Errorf("body = %q, want the server body", body)
	}
	if resp.Header.Get("X-Update-Available") != "2.0.0" {
		t.Errorf("response headers were not relayed: %v", resp.Header)
	}
}

func TestExecuteWithAuthRejectsUnbuildableRequest(t *testing.T) {
	// A control character in the address makes the request URL unparseable, so
	// the failure happens while building the request, before any I/O.
	ep := endpoint.Endpoint{Network: "tcp", Address: "bad\x7fhost:1"}

	resp, body, err := executeWithAuth(&http.Client{}, ep, []byte(`{}`), "tok")
	if err == nil || !strings.Contains(err.Error(), "creating request") {
		t.Fatalf("error = %v, want a 'creating request' error", err)
	}
	if resp != nil || body != nil {
		t.Fatalf("no response expected, got resp=%v body=%q", resp, body)
	}
}

func TestExecuteWithAuthReportsTransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	ep := serverEndpoint(t, srv)
	srv.Close() // Nothing is listening on the address any more.

	resp, body, err := executeWithAuth(ep.HTTPClient(2*time.Second), ep, []byte(`{}`), "tok")
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("error = %v, want a transport (*url.Error) failure", err)
	}
	if resp != nil || body != nil {
		t.Fatalf("no response expected, got resp=%v body=%q", resp, body)
	}
}

func TestExecuteWithAuthReportsTruncatedBody(t *testing.T) {
	srv := truncatingServer(t, http.StatusOK)
	ep := serverEndpoint(t, srv)

	resp, _, err := executeWithAuth(ep.HTTPClient(5*time.Second), ep, []byte(`{}`), "tok")
	if err == nil || !strings.Contains(err.Error(), "reading response") {
		t.Fatalf("error = %v, want a 'reading response' error", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("error = %v, want it to wrap io.ErrUnexpectedEOF", err)
	}
	if resp != nil {
		t.Errorf("a failed body read must not return a response")
	}
}

// ---------- attemptRefresh ----------

func TestAttemptRefreshReturnsNewTokenPair(t *testing.T) {
	withVersion(t, "4.5.6-test")
	rec := &requestRecorder{}
	srv := httptest.NewServer(rec.handler(http.StatusOK,
		`{"access_token":"new-access","access_expires":"2030-01-01T00:00:00Z","refresh_token":"new-refresh","refresh_expires":"2030-02-01T00:00:00Z"}`, nil))
	t.Cleanup(srv.Close)
	ep := serverEndpoint(t, srv)

	tokens, err := attemptRefresh(ep.HTTPClient(5*time.Second), ep, "old-refresh")
	if err != nil {
		t.Fatalf("attemptRefresh() error: %v", err)
	}
	want := tokenRefreshResponse{
		AccessToken:    "new-access",
		AccessExpires:  "2030-01-01T00:00:00Z",
		RefreshToken:   "new-refresh",
		RefreshExpires: "2030-02-01T00:00:00Z",
	}
	if *tokens != want {
		t.Fatalf("tokens = %+v, want %+v", *tokens, want)
	}

	got := rec.only(t)
	if got.Method != http.MethodPost || got.Path != "/token/refresh" {
		t.Errorf("request = %s %s, want POST /token/refresh", got.Method, got.Path)
	}
	if have := got.Header.Get("Authorization"); have != "Bearer old-refresh" {
		t.Errorf("Authorization = %q, want the refresh token as a bearer credential", have)
	}
	if have := got.Header.Get("X-Client-Version"); have != "4.5.6-test" {
		t.Errorf("X-Client-Version = %q, want 4.5.6-test", have)
	}
	if len(got.Body) != 0 {
		t.Errorf("refresh request must have no body, got %q", got.Body)
	}
}

func TestAttemptRefreshUsesServerMessageOnRejection(t *testing.T) {
	rec := &requestRecorder{}
	srv := httptest.NewServer(rec.handler(http.StatusUnauthorized, `{"error":"invalid_token","message":"refresh token has expired"}`, nil))
	t.Cleanup(srv.Close)
	ep := serverEndpoint(t, srv)

	tokens, err := attemptRefresh(ep.HTTPClient(5*time.Second), ep, "old-refresh")
	if tokens != nil {
		t.Fatalf("tokens = %+v, want nil on rejection", tokens)
	}
	if err == nil || err.Error() != "refresh token has expired" {
		t.Fatalf("error = %v, want exactly the server's message", err)
	}
}

func TestAttemptRefreshReportsStatusAndBodyForPlainTextRejection(t *testing.T) {
	rec := &requestRecorder{}
	srv := httptest.NewServer(rec.handler(http.StatusUnauthorized, "unauthorized", nil))
	t.Cleanup(srv.Close)
	ep := serverEndpoint(t, srv)

	_, err := attemptRefresh(ep.HTTPClient(5*time.Second), ep, "old-refresh")
	if err == nil || err.Error() != "refresh returned HTTP 401: unauthorized" {
		t.Fatalf("error = %v, want status and raw body", err)
	}
}

func TestAttemptRefreshReportsStatusWhenJSONHasNoMessage(t *testing.T) {
	rec := &requestRecorder{}
	srv := httptest.NewServer(rec.handler(http.StatusInternalServerError, `{"error":"boom"}`, nil))
	t.Cleanup(srv.Close)
	ep := serverEndpoint(t, srv)

	_, err := attemptRefresh(ep.HTTPClient(5*time.Second), ep, "old-refresh")
	if err == nil || !strings.Contains(err.Error(), "refresh returned HTTP 500") {
		t.Fatalf("error = %v, want the HTTP status when no message is present", err)
	}
}

func TestAttemptRefreshRejectsMalformedSuccessBody(t *testing.T) {
	rec := &requestRecorder{}
	srv := httptest.NewServer(rec.handler(http.StatusOK, "this is not json", nil))
	t.Cleanup(srv.Close)
	ep := serverEndpoint(t, srv)

	tokens, err := attemptRefresh(ep.HTTPClient(5*time.Second), ep, "old-refresh")
	if tokens != nil {
		t.Fatalf("tokens = %+v, want nil", tokens)
	}
	if err == nil || !strings.Contains(err.Error(), "parsing refresh response") {
		t.Fatalf("error = %v, want a 'parsing refresh response' error", err)
	}
}

func TestAttemptRefreshReportsTransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	ep := serverEndpoint(t, srv)
	srv.Close()

	tokens, err := attemptRefresh(ep.HTTPClient(2*time.Second), ep, "old-refresh")
	if tokens != nil {
		t.Fatalf("tokens = %+v, want nil", tokens)
	}
	if err == nil || !strings.Contains(err.Error(), "refresh request failed") {
		t.Fatalf("error = %v, want a 'refresh request failed' error", err)
	}
}

func TestAttemptRefreshRejectsUnbuildableRequest(t *testing.T) {
	ep := endpoint.Endpoint{Network: "tcp", Address: "bad\x7fhost:1"}

	tokens, err := attemptRefresh(&http.Client{}, ep, "old-refresh")
	if tokens != nil {
		t.Fatalf("tokens = %+v, want nil", tokens)
	}
	if err == nil || !strings.Contains(err.Error(), "creating refresh request") {
		t.Fatalf("error = %v, want a 'creating refresh request' error", err)
	}
}

func TestAttemptRefreshReportsTruncatedBody(t *testing.T) {
	srv := truncatingServer(t, http.StatusOK)
	ep := serverEndpoint(t, srv)

	tokens, err := attemptRefresh(ep.HTTPClient(5*time.Second), ep, "old-refresh")
	if tokens != nil {
		t.Fatalf("tokens = %+v, want nil", tokens)
	}
	if err == nil || !strings.Contains(err.Error(), "reading refresh response") {
		t.Fatalf("error = %v, want a 'reading refresh response' error", err)
	}
}

// ---------- shared assertion helpers ----------

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("mode of %s = %04o, want %04o", path, got, want)
	}
}
