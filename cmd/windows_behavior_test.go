package cmd

import (
	"strings"
	"testing"

	"github.com/ekovshilovsky/op-forward/internal/endpoint"
	"github.com/ekovshilovsky/op-forward/internal/opcheck"
)

// The tests in this file drive the platform-dependent decisions with an
// explicit target platform, so the Windows behavior is verified on every
// build host rather than only on Windows.

func TestDefaultShimPath(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	if got := defaultShimPath("windows"); got != "" {
		t.Errorf("defaultShimPath(windows) = %q, want empty: no shim location exists on Windows", got)
	}
	for _, goos := range []string{"darwin", "linux"} {
		if got := defaultShimPath(goos); !strings.HasSuffix(got, "op") || !strings.Contains(got, ".local") {
			t.Errorf("defaultShimPath(%s) = %q, want the ~/.local/bin/op shim location", goos, got)
		}
	}
}

// A Windows machine must never be told to install a shim, in either role:
// the shim is a POSIX script and no Windows equivalent exists.
func TestCheckShimIsNotApplicableOnWindows(t *testing.T) {
	for _, role := range []string{roleHost, roleClient} {
		c := (&doctor{role: role, goos: "windows"}).checkShim()
		if c.Status != statusSkip {
			t.Errorf("role %s: shim status = %q, want %q so it is never a failure or warning", role, c.Status, statusSkip)
		}
		if c.Action != "" {
			t.Errorf("role %s: shim action = %q, want none", role, c.Action)
		}
		if !strings.Contains(c.Summary, "Windows") {
			t.Errorf("role %s: shim summary = %q, want it to say why the check does not apply", role, c.Summary)
		}
	}
}

func TestResolveRoleWithoutShimPathUsesRealOp(t *testing.T) {
	// An empty shim path (the Windows case) is never a shim, so only an
	// explicit role or a real op on PATH can decide the role.
	if got := resolveRole(roleClient, ""); got != roleClient {
		t.Errorf("resolveRole(client, \"\") = %q", got)
	}
	t.Setenv("PATH", t.TempDir())
	if got := resolveRole(roleAuto, ""); got != roleClient {
		t.Errorf("resolveRole(auto, \"\") with no op = %q, want client", got)
	}
}

func TestWindowsHostEndpointActionDoesNotSuggestServiceInstall(t *testing.T) {
	ep := endpoint.TCP("127.0.0.1", 1) // Nothing listens on port 1.
	c, reachable := (&doctor{role: roleHost, goos: "windows"}).checkEndpoint(ep, nil)
	if reachable {
		t.Skip("something is listening on 127.0.0.1:1")
	}
	if strings.Contains(c.Action, "service install") {
		t.Errorf("action = %q, must not recommend macOS-only service management on Windows", c.Action)
	}
	if !strings.Contains(c.Action, "op-forward serve") {
		t.Errorf("action = %q, want it to point at op-forward serve", c.Action)
	}
}

func TestSelfUpdateUnsupportedOnWindows(t *testing.T) {
	reason := selfUpdateUnsupported("windows")
	for _, fragment := range []string{"not supported on Windows", ".zip", "op-forward.exe"} {
		if !strings.Contains(reason, fragment) {
			t.Errorf("reason = %q, want it to mention %q", reason, fragment)
		}
	}
	for _, goos := range []string{"darwin", "linux"} {
		if got := selfUpdateUnsupported(goos); got != "" {
			t.Errorf("selfUpdateUnsupported(%s) = %q, want empty", goos, got)
		}
	}
}

func TestShimInstallUnsupportedOnWindows(t *testing.T) {
	reason := shimInstallUnsupported("windows")
	if !strings.Contains(reason, "not supported on Windows") || !strings.Contains(reason, "op-forward serve") {
		t.Errorf("reason = %q, want the Windows refusal pointing at op-forward serve", reason)
	}
	for _, goos := range []string{"darwin", "linux"} {
		if got := shimInstallUnsupported(goos); got != "" {
			t.Errorf("shimInstallUnsupported(%s) = %q, want empty", goos, got)
		}
	}
}

// A Windows user must be told what to do instead, not just what is missing.
func TestDispatchServiceWindowsRefusalPointsToServe(t *testing.T) {
	err := dispatchService("windows", []string{"op-forward", "service", "install"})
	if err == nil {
		t.Fatal("dispatchService(windows) succeeded, want a refusal")
	}
	for _, fragment := range []string{"only supported on macOS", "op-forward serve", "Task Scheduler"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error = %q, want it to mention %q", err, fragment)
		}
	}
}

// ---------- refused op (non-.exe on a Windows host) ----------

func refusedReport(ext string) opcheck.Report {
	return opcheck.Report{
		OpPresent:   true,
		OpRefusal:   opcheck.RefusalNotNativeExecutable,
		OpExtension: ext,
		Integration: opcheck.IntegrationUnavailable,
		Session:     opcheck.SessionNotProbed,
	}
}

// A refused op fails the check, because every forwarded command resolves the
// same file and is refused; the message must say what was found, why, and
// what to do about it.
func TestDescribeCLIRefusedOpFailsWithExplanation(t *testing.T) {
	c := describeCLI(refusedReport(".cmd"), "the daemon host")
	if c.status != statusFail {
		t.Fatalf("status = %q, want %q", c.status, statusFail)
	}
	for _, fragment := range []string{"the daemon host", "op.cmd", "refuses", "op.exe", "cmd.exe", "re-parses arguments"} {
		if !strings.Contains(c.summary, fragment) {
			t.Errorf("summary = %q, want it to contain %q", c.summary, fragment)
		}
	}
	for _, fragment := range []string{"official 1Password CLI", "op.cmd", "restart the daemon"} {
		if !strings.Contains(c.action, fragment) {
			t.Errorf("action = %q, want it to contain %q", c.action, fragment)
		}
	}
}

func TestDescribeCLIRefusedOpWithoutReportedExtension(t *testing.T) {
	c := describeCLI(refusedReport(""), "this host")
	if c.status != statusFail || !strings.Contains(c.summary, "a file that is not op.exe") {
		t.Errorf("component = %+v, want a failure naming a file that is not op.exe", c)
	}
}

// The integration cannot be examined through a binary that is not run, and
// the report must say so rather than blame the missing CLI.
func TestDescribeIntegrationRefusedOpIsNotReportedAsMissing(t *testing.T) {
	c := describeIntegration(refusedReport(".bat"), "this host")
	if c.status != statusSkip {
		t.Errorf("status = %q, want %q", c.status, statusSkip)
	}
	if strings.Contains(c.summary, "no op CLI") || !strings.Contains(c.summary, "refuses") {
		t.Errorf("summary = %q, want the refusal rather than 'no op CLI'", c.summary)
	}
	if missing := describeIntegration(opcheck.Report{Integration: opcheck.IntegrationUnavailable}, "this host"); !strings.Contains(missing.summary, "no op CLI") {
		t.Errorf("missing-CLI summary = %q, want it unchanged", missing.summary)
	}
}

func TestOpCLIDataCarriesRefusalOnlyWhenSet(t *testing.T) {
	plain := opCLIData(opcheck.Report{OpPresent: true, OpVersion: "2.30.0"})
	if _, ok := plain["op_refusal"]; ok || len(plain) != 2 {
		t.Errorf("data for a runnable op = %v, want only op_present and op_version", plain)
	}
	refused := opCLIData(refusedReport(".cmd"))
	if refused["op_refusal"] != opcheck.RefusalNotNativeExecutable || refused["op_extension"] != ".cmd" {
		t.Errorf("data for a refused op = %v", refused)
	}
}

// A client diagnosing a Windows daemon sees the refusal through daemon_op.
func TestDaemonOpFoldReportsRefusedOp(t *testing.T) {
	rep := refusedReport(".cmd")
	cli, integ, sess := describeOp(rep, "the daemon host")

	c := foldOpComponents("daemon_op", rep, cli, integ, sess)
	if c.Status != statusFail {
		t.Errorf("status = %q, want %q", c.Status, statusFail)
	}
	if c.Data["op_refusal"] != opcheck.RefusalNotNativeExecutable || c.Data["op_extension"] != ".cmd" {
		t.Errorf("data = %v, want the refusal carried through", c.Data)
	}
	if !strings.Contains(c.Action, "op.exe") {
		t.Errorf("action = %q, want the op.exe remedy", c.Action)
	}

	ok := foldOpComponents("daemon_op", opcheck.Report{OpPresent: true, OpVersion: "2.30.0", Integration: opcheck.IntegrationReachable, Session: opcheck.SessionNotProbed},
		opComponent{statusOK, "op 2.30.0", ""})
	if _, present := ok.Data["op_refusal"]; present {
		t.Errorf("data for a healthy daemon = %v, must not carry refusal keys", ok.Data)
	}
}
