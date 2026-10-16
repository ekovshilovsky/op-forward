package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// fakeLaunchctlScript succeeds by default. FAKE_EXIT_LOAD makes the legacy
// "load" subcommand fail, which is what pushes serviceInstall onto the
// bootout/bootstrap path; FAKE_EXIT_BOOTSTRAP makes that path fail as well.
const fakeLaunchctlScript = `
case "$1" in
  load)
    if [ -n "$FAKE_EXIT_LOAD" ]; then exit 1; fi
    ;;
  bootstrap)
    if [ -n "$FAKE_EXIT_BOOTSTRAP" ]; then exit 1; fi
    ;;
esac
exit 0
`

// serviceFixture isolates everything the launchd service commands touch: HOME
// (where the plist and logs live), PATH (so the fake launchctl shadows the real
// one and no test can ever alter the developer's launchd state), and the
// endpoint environment variables.
type serviceFixture struct {
	home      string
	plistPath string
	logPath   string // launchctl invocation log
}

func newServiceFixture(t *testing.T) serviceFixture {
	t.Helper()
	withUmask(t, 0o022)
	home := tempHome(t)
	t.Setenv(envListenName, "")
	t.Setenv(envPortName, "")
	return serviceFixture{
		home:      home,
		plistPath: filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"),
		logPath:   fakeBin(t, "launchctl", fakeLaunchctlScript),
	}
}

// Names of the endpoint environment variables, spelled out so a rename in the
// endpoint package surfaces as a failing test rather than a silently ignored
// variable.
const (
	envListenName = "OP_FORWARD_LISTEN"
	envPortName   = "OP_FORWARD_PORT"
)

func currentUID() string { return strconv.Itoa(os.Getuid()) }

func TestServiceInstallWritesPlistAndLoadsService(t *testing.T) {
	fx := newServiceFixture(t)
	t.Setenv(envListenName, "tcp://127.0.0.1:19234")

	out := captureStdio(t)
	err := serviceInstall()
	stdout, _ := out()
	if err != nil {
		t.Fatalf("serviceInstall() error: %v", err)
	}

	assertMode(t, fx.plistPath, 0o644)
	assertMode(t, filepath.Dir(fx.plistPath), 0o755)

	exe := resolvedExecutable(t)
	plist := readFileString(t, fx.plistPath)
	logFile := filepath.Join(fx.home, "Library", "Logs", "op-forward.log")
	for _, want := range []string{
		"<string>" + launchdLabel + "</string>",
		"<string>" + exe + "</string>\n        <string>serve</string>",
		"<string>--listen</string>\n        <string>tcp://127.0.0.1:19234</string>",
		"<key>HOME</key>\n        <string>" + fx.home + "</string>",
		"<key>StandardOutPath</key>\n    <string>" + logFile + "</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist does not contain %q:\n%s", want, plist)
		}
	}

	wantCalls := []string{
		"unload " + fx.plistPath,
		"load -w " + fx.plistPath,
	}
	assertCalls(t, fx.logPath, wantCalls)

	for _, want := range []string{
		"Launchd service installed and loaded.",
		"plist: " + fx.plistPath,
		"logs:  " + logFile,
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout is missing %q:\n%s", want, stdout)
		}
	}
}

// With no explicit listen endpoint the plist must carry the loopback endpoint
// derived from OP_FORWARD_PORT, because launchd does not inherit the installing
// shell's environment and the daemon could otherwise bind a different port.
func TestServiceInstallBakesPortEnvironmentIntoPlist(t *testing.T) {
	fx := newServiceFixture(t)
	t.Setenv(envPortName, "19876")

	_ = captureStdio(t)
	if err := serviceInstall(); err != nil {
		t.Fatalf("serviceInstall() error: %v", err)
	}
	want := "<string>--listen</string>\n        <string>tcp://127.0.0.1:19876</string>"
	if plist := readFileString(t, fx.plistPath); !strings.Contains(plist, want) {
		t.Errorf("plist does not carry the port from the environment:\n%s", plist)
	}
}

// Newer macOS releases reject the legacy "load" verb. The installer must then
// fall back to bootout followed by bootstrap in the current user's GUI domain.
func TestServiceInstallFallsBackToBootstrapWhenLoadFails(t *testing.T) {
	fx := newServiceFixture(t)
	t.Setenv("FAKE_EXIT_LOAD", "1")

	out := captureStdio(t)
	err := serviceInstall()
	stdout, _ := out()
	if err != nil {
		t.Fatalf("serviceInstall() error: %v", err)
	}

	domain := "gui/" + currentUID()
	assertCalls(t, fx.logPath, []string{
		"unload " + fx.plistPath,
		"load -w " + fx.plistPath,
		"bootout " + domain + " " + fx.plistPath,
		"bootstrap " + domain + " " + fx.plistPath,
	})
	if !strings.Contains(stdout, "Launchd service installed and loaded.") {
		t.Errorf("success banner missing after a successful bootstrap:\n%s", stdout)
	}
}

func TestServiceInstallReportsBootstrapFailure(t *testing.T) {
	fx := newServiceFixture(t)
	t.Setenv("FAKE_EXIT_LOAD", "1")
	t.Setenv("FAKE_EXIT_BOOTSTRAP", "1")

	out := captureStdio(t)
	err := serviceInstall()
	stdout, _ := out()
	if err == nil || !strings.Contains(err.Error(), "loading launchd service") {
		t.Fatalf("error = %v, want a 'loading launchd service' error", err)
	}
	if strings.Contains(stdout, "installed and loaded") {
		t.Errorf("success banner printed although the service did not load:\n%s", stdout)
	}
	// The plist stays on disk so the operator can inspect or load it by hand.
	if _, statErr := os.Stat(fx.plistPath); statErr != nil {
		t.Errorf("plist should remain after a failed load: %v", statErr)
	}
}

func TestServiceInstallRejectsInvalidListenEndpointBeforeWritingAnything(t *testing.T) {
	fx := newServiceFixture(t)
	t.Setenv(envListenName, "not-an-endpoint")

	err := serviceInstall()
	if err == nil || !strings.Contains(err.Error(), "not-an-endpoint") {
		t.Fatalf("error = %v, want the parse error naming the bad endpoint", err)
	}
	if _, statErr := os.Stat(filepath.Join(fx.home, "Library")); !os.IsNotExist(statErr) {
		t.Errorf("nothing may be written for an invalid endpoint (stat err: %v)", statErr)
	}
	assertCalls(t, fx.logPath, nil)
}

func TestServiceInstallFailsWithoutHomeDirectory(t *testing.T) {
	fx := newServiceFixture(t)
	t.Setenv("HOME", "")

	err := serviceInstall()
	if err == nil || !strings.Contains(err.Error(), "determining home directory") {
		t.Fatalf("error = %v, want a 'determining home directory' error", err)
	}
	assertCalls(t, fx.logPath, nil)
}

func TestServiceInstallFailsWhenLaunchAgentsDirectoryCannotBeCreated(t *testing.T) {
	fx := newServiceFixture(t)
	// A regular file where "Library" should be blocks MkdirAll.
	writeFile(t, filepath.Join(fx.home, "Library"), "not a directory", 0o644)

	err := serviceInstall()
	if err == nil || !strings.Contains(err.Error(), "creating LaunchAgents directory") {
		t.Fatalf("error = %v, want a 'creating LaunchAgents directory' error", err)
	}
	assertCalls(t, fx.logPath, nil)
}

func TestServiceInstallFailsWhenPlistCannotBeWritten(t *testing.T) {
	fx := newServiceFixture(t)
	// A directory squatting on the plist path makes the write fail.
	if err := os.MkdirAll(fx.plistPath, 0o755); err != nil {
		t.Fatal(err)
	}

	err := serviceInstall()
	if err == nil || !strings.Contains(err.Error(), "writing plist") {
		t.Fatalf("error = %v, want a 'writing plist' error", err)
	}
	// launchctl must not be asked to load a job whose definition was not written.
	assertCalls(t, fx.logPath, nil)
}

// ---------- serviceUninstall ----------

func TestServiceUninstallRemovesPlistAndLeavesTokensAlone(t *testing.T) {
	fx := newServiceFixture(t)
	writeFile(t, fx.plistPath, "<plist/>", 0o644)
	tokenPath := filepath.Join(fx.home, "Library", "Caches", "op-forward", "refresh.token")
	const tokenContents = "secret-refresh\n2030-01-01T00:00:00Z\n"
	writeFile(t, tokenPath, tokenContents, 0o600)

	out := captureStdio(t)
	err := serviceUninstall()
	stdout, _ := out()
	if err != nil {
		t.Fatalf("serviceUninstall() error: %v", err)
	}

	if _, statErr := os.Stat(fx.plistPath); !os.IsNotExist(statErr) {
		t.Errorf("plist should be removed (stat err: %v)", statErr)
	}
	assertCalls(t, fx.logPath, []string{"unload " + fx.plistPath})
	if got := readFileString(t, tokenPath); got != tokenContents {
		t.Errorf("uninstall must not touch cached tokens; refresh.token is now %q", got)
	}
	if !strings.Contains(stdout, "Launchd service uninstalled.") {
		t.Errorf("stdout = %q, want the uninstall confirmation", stdout)
	}
}

func TestServiceUninstallToleratesMissingPlist(t *testing.T) {
	fx := newServiceFixture(t)

	out := captureStdio(t)
	err := serviceUninstall()
	stdout, _ := out()
	if err != nil {
		t.Fatalf("serviceUninstall() with nothing installed = %v, want nil", err)
	}
	if !strings.Contains(stdout, "Launchd service uninstalled.") {
		t.Errorf("stdout = %q, want the uninstall confirmation", stdout)
	}
	assertCalls(t, fx.logPath, []string{"unload " + fx.plistPath})
}

func TestServiceUninstallReportsRemovalFailure(t *testing.T) {
	fx := newServiceFixture(t)
	// A non-empty directory at the plist path cannot be removed by os.Remove,
	// and the failure is not a "does not exist" condition.
	writeFile(t, filepath.Join(fx.plistPath, "child"), "x", 0o644)

	out := captureStdio(t)
	err := serviceUninstall()
	stdout, _ := out()
	if err == nil || !strings.Contains(err.Error(), "removing plist") {
		t.Fatalf("error = %v, want a 'removing plist' error", err)
	}
	if strings.Contains(stdout, "uninstalled") {
		t.Errorf("success banner printed although removal failed:\n%s", stdout)
	}
}

func TestServiceUninstallFailsWithoutHomeDirectory(t *testing.T) {
	fx := newServiceFixture(t)
	t.Setenv("HOME", "")

	err := serviceUninstall()
	if err == nil || !strings.Contains(err.Error(), "determining home directory") {
		t.Fatalf("error = %v, want a 'determining home directory' error", err)
	}
	assertCalls(t, fx.logPath, nil)
}

// ---------- runService dispatch ----------

func TestRunServiceWithoutSubcommandPrintsUsage(t *testing.T) {
	fx := newServiceFixture(t)
	setArgs(t, "op-forward", "service")

	out := captureStdio(t)
	err := runService()
	stdout, _ := out()
	if err != nil {
		t.Fatalf("runService() without a subcommand = %v, want nil", err)
	}
	if !strings.Contains(stdout, "Usage: op-forward service [install|uninstall]") {
		t.Errorf("stdout = %q, want the service usage line", stdout)
	}
	assertCalls(t, fx.logPath, nil)
}

func TestExecuteServiceDispatchesToServiceCommand(t *testing.T) {
	_ = newServiceFixture(t)
	setArgs(t, "op-forward", "service")

	out := captureStdio(t)
	err := Execute()
	stdout, _ := out()
	if err != nil {
		t.Fatalf("Execute(service) = %v, want nil", err)
	}
	if !strings.Contains(stdout, "Usage: op-forward service") {
		t.Errorf("Execute did not reach runService; stdout = %q", stdout)
	}
}

// Off macOS the service commands must refuse before touching the filesystem.
// This test can only run on a non-darwin host: the platform check reads
// runtime.GOOS directly, which cannot be substituted.
func TestRunServiceRefusesOutsideMacOSBeforeWritingFiles(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("exercises the non-macOS refusal, which is unreachable on darwin")
	}
	fx := newServiceFixture(t)

	for _, sub := range []string{"install", "uninstall", "bogus"} {
		setArgs(t, "op-forward", "service", sub)
		err := runService()
		if err == nil || !strings.Contains(err.Error(), "only supported on macOS") {
			t.Errorf("runService(%s) error = %v, want the macOS-only refusal", sub, err)
		}
	}
	if _, err := os.Stat(filepath.Join(fx.home, "Library")); !os.IsNotExist(err) {
		t.Errorf("no files may be written off macOS (stat err: %v)", err)
	}
	assertCalls(t, fx.logPath, nil)
}

func TestRunServiceDispatchesInstallAndUninstallOnMacOS(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("service dispatch past the platform check only runs on darwin")
	}
	fx := newServiceFixture(t)

	setArgs(t, "op-forward", "service", "install")
	_ = captureStdio(t)
	if err := runService(); err != nil {
		t.Fatalf("runService(install) error: %v", err)
	}
	if _, err := os.Stat(fx.plistPath); err != nil {
		t.Fatalf("install did not write the plist: %v", err)
	}

	setArgs(t, "op-forward", "service", "uninstall")
	if err := runService(); err != nil {
		t.Fatalf("runService(uninstall) error: %v", err)
	}
	if _, err := os.Stat(fx.plistPath); !os.IsNotExist(err) {
		t.Fatalf("uninstall did not remove the plist (stat err: %v)", err)
	}
}

func TestRunServiceRejectsUnknownSubcommandOnMacOS(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the unknown-subcommand branch is only reachable on darwin")
	}
	fx := newServiceFixture(t)
	setArgs(t, "op-forward", "service", "restart")

	err := runService()
	if err == nil || !strings.Contains(err.Error(), "unknown service command: restart") {
		t.Fatalf("error = %v, want an 'unknown service command' error", err)
	}
	assertCalls(t, fx.logPath, nil)
	if _, statErr := os.Stat(fx.plistPath); !os.IsNotExist(statErr) {
		t.Errorf("an unknown subcommand must not write a plist (stat err: %v)", statErr)
	}
}

// ---------- helpers ----------

func resolvedExecutable(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("cannot determine the running executable: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatalf("resolving %s: %v", exe, err)
	}
	return resolved
}

func assertCalls(t *testing.T, logPath string, want []string) {
	t.Helper()
	got := readLogLines(t, logPath)
	if len(got) != len(want) {
		t.Fatalf("recorded calls = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call %d = %q, want %q (all calls: %q)", i, got[i], want[i], got)
		}
	}
}
