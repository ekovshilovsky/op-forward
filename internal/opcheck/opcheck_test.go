package opcheck

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeOpScript models a 1Password CLI on a desktop-app-integration host: the
// CLI holds no accounts of its own, so whoami reports "not signed in" while
// authenticated reads still succeed through the app. Behavior is selected
// through environment variables the tests set with t.Setenv.
//
// Every invocation is appended to FAKE_OP_LOG so tests can assert which
// commands were (and were never) run.
const fakeOpScript = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_OP_LOG"
case "$1" in
  --version)
    echo "${FAKE_OP_VERSION-2.41.0}"
    exit "${FAKE_OP_VERSION_EXIT:-0}"
    ;;
  whoami)
    echo "[ERROR] 2026/10/17 09:09:10 account is not signed in" >&2
    exit 1
    ;;
  account)
    if [ "${FAKE_OP_ACCOUNT_SLEEP:-0}" != "0" ]; then
      exec sleep "$FAKE_OP_ACCOUNT_SLEEP"
    fi
    if [ -n "${FAKE_OP_ACCOUNTS+set}" ]; then
      printf '%s' "$FAKE_OP_ACCOUNTS"
    else
      printf '%s' '[{"url":"my.1password.com"}]'
    fi
    exit "${FAKE_OP_ACCOUNT_EXIT:-0}"
    ;;
  vault)
    if [ "${FAKE_OP_VAULT_SLEEP:-0}" != "0" ]; then
      # A hung read that has already said something (not the silent hang of a
      # locked app).
      [ -n "$FAKE_OP_VAULT_PRE_STDOUT" ] && printf '%s' "$FAKE_OP_VAULT_PRE_STDOUT"
      [ -n "$FAKE_OP_VAULT_PRE_STDERR" ] && printf '%s' "$FAKE_OP_VAULT_PRE_STDERR" >&2
      exec sleep "$FAKE_OP_VAULT_SLEEP"
    fi
    if [ "${FAKE_OP_VAULT_EXIT:-0}" != "0" ]; then
      echo "${FAKE_OP_VAULT_STDERR:-[ERROR] vault list failed}" >&2
      exit "$FAKE_OP_VAULT_EXIT"
    fi
    echo '[{"id":"abc","name":"SECRET-VAULT-NAME"}]'
    exit 0
    ;;
esac
exit 0
`

// installFakeOp places the fake op first on PATH and returns the invocation
// log path. The remaining PATH is kept so helpers such as sleep resolve.
func installFakeOp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "invocations.log")
	// #nosec G306 -- the fake op is a test fixture that must be executable
	if err := os.WriteFile(filepath.Join(dir, "op"), []byte(fakeOpScript), 0o755); err != nil {
		t.Fatalf("writing fake op: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_OP_LOG", logPath)
	return logPath
}

func readInvocations(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading %s: %v", logPath, err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestCheckReportsAbsentOpWithoutRunningAnything(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	rep := Check(context.Background(), Options{Authenticated: true})

	want := Report{Integration: IntegrationUnavailable, Session: SessionNotProbed}
	if rep != want {
		t.Fatalf("Check() = %+v, want %+v", rep, want)
	}
}

// The default run is metadata only: no authenticated command may be issued,
// because that is what can raise a biometric prompt on the host.
func TestCheckDefaultIsMetadataOnlyAndNeverAuthenticates(t *testing.T) {
	logPath := installFakeOp(t)

	rep := Check(context.Background(), Options{})

	if !rep.OpPresent || rep.OpVersion != "2.41.0" {
		t.Errorf("op presence/version = %v/%q, want true/2.41.0", rep.OpPresent, rep.OpVersion)
	}
	if rep.Integration != IntegrationReachable {
		t.Errorf("Integration = %q, want %q", rep.Integration, IntegrationReachable)
	}
	if rep.AccountCount == nil || *rep.AccountCount != 1 {
		t.Errorf("AccountCount = %v, want 1", rep.AccountCount)
	}
	if rep.Session != SessionNotProbed || rep.SessionReason != ReasonNone {
		t.Errorf("session = %q/%q, want %q with no reason", rep.Session, rep.SessionReason, SessionNotProbed)
	}
	for _, call := range readInvocations(t, logPath) {
		if strings.HasPrefix(call, "vault") || strings.HasPrefix(call, "item") || strings.HasPrefix(call, "whoami") {
			t.Errorf("metadata-only check ran %q", call)
		}
	}
}

// THE INCIDENT: whoami exits 1 with "account is not signed in" while an
// authenticated read succeeds. The session is healthy, and whoami must never
// be consulted. This test fails if whoami is ever wired in as the probe,
// either because the session would be reported failed or because the
// invocation log would contain it.
func TestCheckSessionHealthyWhenWhoamiFailsButAuthenticatedReadSucceeds(t *testing.T) {
	logPath := installFakeOp(t)

	rep := Check(context.Background(), Options{Authenticated: true})

	if rep.Session != SessionHealthy {
		t.Fatalf("Session = %q (%s), want %q: whoami is a false negative on desktop-app-integration hosts",
			rep.Session, rep.SessionReason, SessionHealthy)
	}
	for _, call := range readInvocations(t, logPath) {
		if strings.HasPrefix(call, "whoami") {
			t.Fatalf("health check invoked %q; whoami must never be the probe", call)
		}
	}
}

func TestCheckClassifiesAuthenticatedProbeFailures(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   string
	}{
		{"not signed in", "[ERROR] account is not signed in", ReasonNotSignedIn},
		{"prompt dismissed", "[ERROR] authorization prompt dismissed, please try again", ReasonAuthDenied},
		{"app locked", "[ERROR] 1Password is locked", ReasonAppLocked},
		{"unrecognized message", "[ERROR] something unexpected", ReasonOpError},
		{"empty stderr", "", ReasonOpError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installFakeOp(t)
			t.Setenv("FAKE_OP_VAULT_EXIT", "1")
			t.Setenv("FAKE_OP_VAULT_STDERR", tt.stderr)

			rep := Check(context.Background(), Options{Authenticated: true})

			if rep.Session != SessionFailed || rep.SessionReason != tt.want {
				t.Errorf("session = %q/%q, want %q/%q", rep.Session, rep.SessionReason, SessionFailed, tt.want)
			}
		})
	}
}

// The measured behavior of a LOCKED 1Password app, encoded as fixtures: "op
// account list" still answers, but an authenticated read hangs with no output
// (op itself only gives up after about a minute). Classification must rest on
// that behavior, because "op whoami" calls a locked app "not signed in". The
// sleeps are far longer than the injected timeouts, so the tests stay fast and
// never depend on wall-clock delays.
func TestCheckClassifiesTheProbeHangOfALockedApp(t *testing.T) {
	tests := []struct {
		name            string
		env             map[string]string
		wantIntegration string
		wantReason      string
	}{
		{
			// (a) The most common real failure: metadata works, the read is silent.
			name:            "metadata answers and the read hangs silently: app locked",
			env:             map[string]string{"FAKE_OP_VAULT_SLEEP": "30"},
			wantIntegration: IntegrationReachable,
			wantReason:      ReasonAppLocked,
		},
		{
			// (b) The integration itself is not answering: say nothing about the
			// app's lock state.
			name:            "account list also hangs: plain timeout, not a locked app",
			env:             map[string]string{"FAKE_OP_ACCOUNT_SLEEP": "30", "FAKE_OP_VAULT_SLEEP": "30"},
			wantIntegration: IntegrationUnreachable,
			wantReason:      ReasonTimeout,
		},
		{
			name:            "account list fails and the read hangs: plain timeout",
			env:             map[string]string{"FAKE_OP_ACCOUNT_EXIT": "1", "FAKE_OP_VAULT_SLEEP": "30"},
			wantIntegration: IntegrationUnreachable,
			wantReason:      ReasonTimeout,
		},
		{
			name:            "the read printed to stderr before hanging: not the silent locked pattern",
			env:             map[string]string{"FAKE_OP_VAULT_SLEEP": "30", "FAKE_OP_VAULT_PRE_STDERR": "working..."},
			wantIntegration: IntegrationReachable,
			wantReason:      ReasonTimeout,
		},
		{
			name:            "the read printed to stdout before hanging: not the silent locked pattern",
			env:             map[string]string{"FAKE_OP_VAULT_SLEEP": "30", "FAKE_OP_VAULT_PRE_STDOUT": "["},
			wantIntegration: IntegrationReachable,
			wantReason:      ReasonTimeout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installFakeOp(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			start := time.Now()
			rep := Check(context.Background(), Options{
				Authenticated:   true,
				MetadataTimeout: 300 * time.Millisecond,
				ProbeTimeout:    300 * time.Millisecond,
			})

			if rep.Integration != tt.wantIntegration {
				t.Errorf("Integration = %q, want %q", rep.Integration, tt.wantIntegration)
			}
			if rep.Session != SessionFailed || rep.SessionReason != tt.wantReason {
				t.Errorf("session = %q/%q, want %q/%q", rep.Session, rep.SessionReason, SessionFailed, tt.wantReason)
			}
			if elapsed := time.Since(start); elapsed > 10*time.Second {
				t.Errorf("check took %v despite 300ms timeouts", elapsed)
			}
		})
	}
}

// Whoami's "not signed in" wording must not be what classifies a locked app:
// here whoami would say "not signed in" instantly, yet the silent hang is
// reported as a locked app, and whoami is still never run.
func TestCheckLockedAppIsNotMistakenForSignedOut(t *testing.T) {
	logPath := installFakeOp(t)
	t.Setenv("FAKE_OP_VAULT_SLEEP", "30")

	rep := Check(context.Background(), Options{Authenticated: true, ProbeTimeout: 300 * time.Millisecond})

	if rep.SessionReason != ReasonAppLocked {
		t.Errorf("SessionReason = %q, want %q", rep.SessionReason, ReasonAppLocked)
	}
	for _, call := range readInvocations(t, logPath) {
		if strings.HasPrefix(call, "whoami") {
			t.Fatalf("health check invoked %q", call)
		}
	}
}

func TestDefaultProbeTimeoutIsBoundedButAllowsAnApproval(t *testing.T) {
	if DefaultProbeTimeout < 10*time.Second || DefaultProbeTimeout > 30*time.Second {
		t.Errorf("DefaultProbeTimeout = %v, want long enough to approve a prompt and short enough not to hang a health check", DefaultProbeTimeout)
	}
}

func TestCheckAccountMetadataClassification(t *testing.T) {
	tests := []struct {
		name        string
		accounts    string
		exit        string
		integration string
		count       *int
	}{
		{"two accounts", `[{"a":1},{"a":2}]`, "0", IntegrationReachable, intPtr(2)},
		{"no accounts", `[]`, "0", IntegrationReachable, intPtr(0)},
		{"unparsable output after success", `not json`, "0", IntegrationReachable, nil},
		{"command fails", ``, "1", IntegrationUnreachable, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installFakeOp(t)
			t.Setenv("FAKE_OP_ACCOUNTS", tt.accounts)
			t.Setenv("FAKE_OP_ACCOUNT_EXIT", tt.exit)

			rep := Check(context.Background(), Options{})

			if rep.Integration != tt.integration {
				t.Errorf("Integration = %q, want %q", rep.Integration, tt.integration)
			}
			switch {
			case tt.count == nil && rep.AccountCount != nil:
				t.Errorf("AccountCount = %d, want nil", *rep.AccountCount)
			case tt.count != nil && (rep.AccountCount == nil || *rep.AccountCount != *tt.count):
				t.Errorf("AccountCount = %v, want %d", rep.AccountCount, *tt.count)
			}
		})
	}
}

func TestCheckRejectsImplausibleVersionOutput(t *testing.T) {
	tests := []struct {
		name    string
		version string
		exit    string
		want    string
	}{
		{"plain version", "2.41.0", "0", "2.41.0"},
		{"free text is not a version", "hello there, this is not a version", "0", ""},
		{"empty output", "", "0", ""},
		{"command fails", "2.41.0", "1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installFakeOp(t)
			t.Setenv("FAKE_OP_VERSION", tt.version)
			t.Setenv("FAKE_OP_VERSION_EXIT", tt.exit)

			if got := Check(context.Background(), Options{}).OpVersion; got != tt.want {
				t.Errorf("OpVersion = %q, want %q", got, tt.want)
			}
		})
	}
}

// The report is served by a diagnostic endpoint: the vault listing the probe
// reads must never appear in it, in any serialized form.
func TestReportNeverContainsProbeOutput(t *testing.T) {
	installFakeOp(t)

	rep := Check(context.Background(), Options{Authenticated: true})
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshalling report: %v", err)
	}

	for _, leaked := range []string{"SECRET-VAULT-NAME", "my.1password.com", `"id"`} {
		if strings.Contains(string(data), leaked) {
			t.Errorf("serialized report leaks %q: %s", leaked, data)
		}
	}
}

func TestReportJSONFieldNamesAreStable(t *testing.T) {
	data, err := json.Marshal(Report{Integration: IntegrationUnavailable, Session: SessionNotProbed})
	if err != nil {
		t.Fatalf("marshalling report: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("report JSON does not parse: %v", err)
	}
	for _, field := range []string{"op_present", "op_version", "integration", "account_count", "session", "session_reason"} {
		if _, ok := got[field]; !ok {
			t.Errorf("report JSON is missing field %q: %s", field, data)
		}
	}
	if len(got) != 6 {
		t.Errorf("report JSON has %d fields, want exactly 6: %s", len(got), data)
	}
}

func TestLocate(t *testing.T) {
	writeOp := func(t *testing.T, dir, content string, mode os.FileMode) string {
		t.Helper()
		path := filepath.Join(dir, "op")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	const shimText = "#!/bin/sh\n# op-forward shim — forwards op commands\n"

	t.Run("finds an executable op", func(t *testing.T) {
		dir := t.TempDir()
		want := writeOp(t, dir, "#!/bin/sh\n", 0o755)
		t.Setenv("PATH", dir)
		if got, ok := Locate(); !ok || got != want {
			t.Errorf("Locate() = %q, %v; want %q, true", got, ok, want)
		}
	})

	t.Run("skips the shim and finds the real op behind it", func(t *testing.T) {
		shimDir, realDir := t.TempDir(), t.TempDir()
		writeOp(t, shimDir, shimText, 0o755)
		want := writeOp(t, realDir, "#!/bin/sh\n", 0o755)
		t.Setenv("PATH", shimDir+string(os.PathListSeparator)+realDir)
		if got, ok := Locate(); !ok || got != want {
			t.Errorf("Locate() = %q, %v; want the real op %q", got, ok, want)
		}
	})

	t.Run("a shim alone is not an op", func(t *testing.T) {
		dir := t.TempDir()
		writeOp(t, dir, shimText, 0o755)
		t.Setenv("PATH", dir)
		if got, ok := Locate(); ok {
			t.Errorf("Locate() = %q, true; a shim must not count as the real op", got)
		}
	})

	t.Run("ignores non-executable files, directories and relative entries", func(t *testing.T) {
		plain := t.TempDir()
		writeOp(t, plain, "#!/bin/sh\n", 0o644)
		withDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(withDir, "op"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", strings.Join([]string{plain, withDir, "relative/dir", ""}, string(os.PathListSeparator)))
		if got, ok := Locate(); ok {
			t.Errorf("Locate() = %q, true; want not found", got)
		}
	})
}

func TestIsShimOnMissingFile(t *testing.T) {
	if IsShim(filepath.Join(t.TempDir(), "absent")) {
		t.Error("IsShim() = true for a file that does not exist")
	}
}

func TestCappedBufferKeepsOnlyTheLeadingBytes(t *testing.T) {
	var b cappedBuffer
	chunk := strings.Repeat("x", stderrCapBytes)

	for i := 0; i < 3; i++ {
		n, err := b.Write([]byte(chunk))
		if err != nil || n != len(chunk) {
			t.Fatalf("Write() = %d, %v; want the full length reported and no error", n, err)
		}
	}

	if got := len(b.String()); got != stderrCapBytes {
		t.Errorf("retained %d bytes, want %d", got, stderrCapBytes)
	}
}

func intPtr(n int) *int { return &n }
