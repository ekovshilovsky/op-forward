package cmd

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// updateFixture prepares everything performUpdate touches beyond the network:
// a stand-in "installed binary" the update may overwrite, a fake pgrep that
// reports no daemon by default, and an endpoint environment that cannot leak
// from the invoking shell. Replacing the stand-in instead of the running test
// binary is what makes the end-to-end update path safe to execute.
type updateFixture struct {
	binPath  string
	pgrepLog string
	stub     *githubStub
}

const (
	installedContents = "installed-binary-v1"
	releasedContents  = "released-binary-v2"
)

// newUpdateFixture serves a v2.0.0 release containing releasedContents and
// reports the running version as 1.0.0-test.
func newUpdateFixture(t *testing.T) updateFixture {
	t.Helper()
	withUmask(t, 0o022)
	withVersion(t, "1.0.0-test")
	t.Setenv(envListenName, "")
	t.Setenv(envPortName, "")

	binPath := filepath.Join(t.TempDir(), "op-forward")
	writeFile(t, binPath, installedContents, 0o755)

	release := releaseJSONFor(t, "v2.0.0",
		githubAsset{Name: platformAssetName("2.0.0"), BrowserDownloadURL: assetURL("asset")})
	tarball := makeTarGz(t, tarEntry{Name: "op-forward", Body: releasedContents})

	pgrepLog := fakeBin(t, "pgrep", fakePgrepScript)
	t.Setenv("FAKE_PGREP_OUT", "")
	t.Setenv("FAKE_PGREP_EXIT", "1")

	return updateFixture{
		binPath:  binPath,
		pgrepLog: pgrepLog,
		stub:     fakeGitHub(t, release, tarball),
	}
}

// resolveTo returns a resolve function that reports path.
func resolveTo(path string) func() (string, error) {
	return func() (string, error) { return path, nil }
}

// failIfResolved returns a resolve function that fails the test when called,
// for the branches that must stop before the executable is located.
func failIfResolved(t *testing.T) func() (string, error) {
	t.Helper()
	return func() (string, error) {
		t.Error("the executable path was resolved on a branch that must not replace the binary")
		return "", errors.New("unexpected resolve")
	}
}

// ---------- replaceBinaryAt ----------

func TestReplaceBinaryAtOverwritesTargetAndRemovesStagedFile(t *testing.T) {
	withUmask(t, 0o022)
	target := filepath.Join(t.TempDir(), "op-forward")
	writeFile(t, target, installedContents, 0o600)

	if err := replaceBinaryAt(target, []byte(releasedContents)); err != nil {
		t.Fatalf("replaceBinaryAt() error: %v", err)
	}

	if got := readFileString(t, target); got != releasedContents {
		t.Errorf("target contents = %q, want %q", got, releasedContents)
	}
	// The replacement must be executable even though the file it replaced
	// was not.
	assertMode(t, target, 0o755)
	if _, err := os.Stat(target + ".update"); !os.IsNotExist(err) {
		t.Errorf("the staged file must not outlive a successful replace (stat err: %v)", err)
	}
}

func TestReplaceBinaryAtCreatesTargetWhenNoneExists(t *testing.T) {
	withUmask(t, 0o022)
	target := filepath.Join(t.TempDir(), "op-forward")

	if err := replaceBinaryAt(target, []byte(releasedContents)); err != nil {
		t.Fatalf("replaceBinaryAt() error: %v", err)
	}
	if got := readFileString(t, target); got != releasedContents {
		t.Errorf("target contents = %q, want %q", got, releasedContents)
	}
}

// Staging happens beside the target, so a target in a directory that cannot
// be written fails before the existing binary is touched. A missing directory
// is used because it fails identically for privileged and unprivileged users.
func TestReplaceBinaryAtReportsStagingFailure(t *testing.T) {
	target := filepath.Join(t.TempDir(), "missing", "op-forward")

	err := replaceBinaryAt(target, []byte(releasedContents))
	if err == nil || !strings.Contains(err.Error(), "writing update") {
		t.Fatalf("error = %v, want a 'writing update' error", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error %v does not wrap the underlying write failure", err)
	}
}

func TestReplaceBinaryAtReportsReadOnlyDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not restrict a privileged user")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "op-forward")
	writeFile(t, target, installedContents, 0o755)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := replaceBinaryAt(target, []byte(releasedContents))
	if err == nil || !strings.Contains(err.Error(), "writing update") {
		t.Fatalf("error = %v, want a 'writing update' error", err)
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Errorf("error %v does not wrap the permission failure", err)
	}
	if got := readFileString(t, target); got != installedContents {
		t.Errorf("the existing binary was modified by a failed update: %q", got)
	}
}

// A non-empty directory at the target path cannot be replaced by a file, which
// makes the rename fail after the staged file was written successfully.
func TestReplaceBinaryAtRemovesStagedFileWhenRenameFails(t *testing.T) {
	target := filepath.Join(t.TempDir(), "op-forward")
	writeFile(t, filepath.Join(target, "child"), "x", 0o644)

	err := replaceBinaryAt(target, []byte(releasedContents))
	if err == nil || !strings.Contains(err.Error(), "replacing binary") {
		t.Fatalf("error = %v, want a 'replacing binary' error", err)
	}
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) {
		t.Errorf("error %v does not wrap the rename failure", err)
	}
	if _, statErr := os.Stat(target + ".update"); !os.IsNotExist(statErr) {
		t.Errorf("the staged file must be removed after a failed rename (stat err: %v)", statErr)
	}
}

// ---------- resolveExecutable ----------

func TestResolveExecutableReturnsSymlinkResolvedPath(t *testing.T) {
	got, err := resolveExecutable()
	if err != nil {
		t.Skipf("cannot determine the running executable: %v", err)
	}
	if want := resolvedExecutable(t); got != want {
		t.Errorf("resolveExecutable() = %q, want %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("resolveExecutable() = %q, want an absolute path", got)
	}
}

// ---------- performUpdate ----------

func TestPerformUpdateReplacesBinaryWhenNoDaemonIsRunning(t *testing.T) {
	fx := newUpdateFixture(t)

	out := captureStdio(t)
	err := performUpdate(resolveTo(fx.binPath), time.Second)
	stdout, _ := out()
	if err != nil {
		t.Fatalf("performUpdate() error: %v", err)
	}

	if got := readFileString(t, fx.binPath); got != releasedContents {
		t.Errorf("binary contents = %q, want %q", got, releasedContents)
	}
	assertMode(t, fx.binPath, 0o755)
	for _, want := range []string{
		"Current version: 1.0.0-test\n",
		"Checking for updates...\n",
		"New version available: v1.0.0-test → v2.0.0\n",
		"Downloading " + platformAssetName("2.0.0") + "...\n",
		"Updated to v2.0.0\n",
		"No running daemon found. It will pick up the new version on next start.\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout is missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "Daemon is serving again") {
		t.Errorf("no daemon was restarted, yet stdout reports one serving:\n%s", stdout)
	}
	if reqs := fx.stub.Requests(); len(reqs) != 2 {
		t.Errorf("requests = %v, want the release lookup and the asset download", reqs)
	}
	// The daemon is located by the path of the binary that was just replaced.
	assertCalls(t, fx.pgrepLog, []string{"-f " + fx.binPath + " serve"})
}

func TestPerformUpdateStopsWhenAlreadyUpToDate(t *testing.T) {
	fx := newUpdateFixture(t)
	withVersion(t, "2.0.0")

	out := captureStdio(t)
	err := performUpdate(failIfResolved(t), time.Second)
	stdout, _ := out()
	if err != nil {
		t.Fatalf("performUpdate() error: %v", err)
	}

	if !strings.Contains(stdout, "Already up to date (v2.0.0)\n") {
		t.Errorf("stdout = %q, want the up-to-date notice", stdout)
	}
	if got := readFileString(t, fx.binPath); got != installedContents {
		t.Errorf("an up-to-date run modified the binary: %q", got)
	}
	if reqs := fx.stub.Requests(); len(reqs) != 1 {
		t.Errorf("requests = %v, want only the release lookup", reqs)
	}
	assertCalls(t, fx.pgrepLog, nil)
}

func TestPerformUpdateLeavesBinaryAloneWhenDownloadFails(t *testing.T) {
	fx := newUpdateFixture(t)
	fx.stub.AssetStatus = http.StatusBadGateway

	_ = captureStdio(t)
	err := performUpdate(failIfResolved(t), time.Second)
	if err == nil || !strings.Contains(err.Error(), "download returned status 502") {
		t.Fatalf("error = %v, want the download failure", err)
	}
	if got := readFileString(t, fx.binPath); got != installedContents {
		t.Errorf("a failed download modified the binary: %q", got)
	}
	assertCalls(t, fx.pgrepLog, nil)
}

func TestPerformUpdateReportsUnresolvableExecutable(t *testing.T) {
	fx := newUpdateFixture(t)
	resolveErr := errors.New("determining binary path: boom")

	_ = captureStdio(t)
	err := performUpdate(func() (string, error) { return "", resolveErr }, time.Second)
	if !errors.Is(err, resolveErr) {
		t.Fatalf("error = %v, want the resolve failure returned unchanged", err)
	}
	if got := readFileString(t, fx.binPath); got != installedContents {
		t.Errorf("binary was modified although it could not be located: %q", got)
	}
	assertCalls(t, fx.pgrepLog, nil)
}

func TestPerformUpdateStopsBeforeRestartWhenReplaceFails(t *testing.T) {
	fx := newUpdateFixture(t)
	unwritable := filepath.Join(t.TempDir(), "missing", "op-forward")

	out := captureStdio(t)
	err := performUpdate(resolveTo(unwritable), time.Second)
	stdout, _ := out()
	if err == nil || !strings.Contains(err.Error(), "writing update") {
		t.Fatalf("error = %v, want a 'writing update' error", err)
	}
	if strings.Contains(stdout, "Updated to") {
		t.Errorf("success notice printed although the binary was not replaced:\n%s", stdout)
	}
	// The old daemon keeps running on the old binary, so it must not be signaled.
	assertCalls(t, fx.pgrepLog, nil)
}

// healthServer answers /health with 200 and reports each probe on the returned
// channel, so a test can assert the update actually waited on the daemon.
func healthServer(t *testing.T) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	probes := make(chan struct{}, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		select {
		case probes <- struct{}{}:
		default:
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, probes
}

func TestPerformUpdateRestartsDaemonAndConfirmsItIsServing(t *testing.T) {
	fx := newUpdateFixture(t)
	srv, probes := healthServer(t)
	ep := serverEndpoint(t, srv)
	t.Setenv(envListenName, ep.String())
	daemon := startSleeper(t)
	t.Setenv("FAKE_PGREP_OUT", strconv.Itoa(daemon.Process.Pid)+"\n")
	t.Setenv("FAKE_PGREP_EXIT", "0")

	out := captureStdio(t)
	err := performUpdate(resolveTo(fx.binPath), 5*time.Second)
	stdout, _ := out()
	if err != nil {
		t.Fatalf("performUpdate() error: %v", err)
	}

	if sig := waitSignal(t, daemon); sig != syscall.SIGTERM {
		t.Errorf("daemon ended by %v, want SIGTERM", sig)
	}
	select {
	case <-probes:
	default:
		t.Error("the update never probed the daemon's health endpoint")
	}
	for _, want := range []string{
		"Daemon stopped (PID " + strconv.Itoa(daemon.Process.Pid) + "); waiting for launchd to respawn it...\n",
		"Daemon is serving again on " + ep.String() + "\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout is missing %q:\n%s", want, stdout)
		}
	}
}

func TestPerformUpdateReportsDaemonThatDoesNotComeBack(t *testing.T) {
	fx := newUpdateFixture(t)
	// A port that was bound and released has nothing listening on it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadEndpoint := "tcp://" + ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("releasing the probe listener: %v", err)
	}
	t.Setenv(envListenName, deadEndpoint)
	daemon := startSleeper(t)
	t.Setenv("FAKE_PGREP_OUT", strconv.Itoa(daemon.Process.Pid)+"\n")
	t.Setenv("FAKE_PGREP_EXIT", "0")

	out := captureStdio(t)
	start := time.Now()
	err = performUpdate(resolveTo(fx.binPath), 600*time.Millisecond)
	stdout, _ := out()

	if err == nil {
		t.Fatal("performUpdate() succeeded although the daemon never answered")
	}
	for _, want := range []string{
		"daemon did not come back on " + deadEndpoint,
		"no healthy response within 600ms",
		"launchctl kickstart -k gui/$(id -u)/com.op-forward.daemon",
		"~/Library/Logs/op-forward.log",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the health wait ignored its timeout parameter and took %v", elapsed)
	}
	// The binary is replaced before the daemon is restarted, so the failure to
	// come back does not roll the update back.
	if got := readFileString(t, fx.binPath); got != releasedContents {
		t.Errorf("binary contents = %q, want the released binary", got)
	}
	if strings.Contains(stdout, "Daemon is serving again") {
		t.Errorf("success notice printed for a daemon that never answered:\n%s", stdout)
	}
	if sig := waitSignal(t, daemon); sig != syscall.SIGTERM {
		t.Errorf("daemon ended by %v, want SIGTERM", sig)
	}
}

func TestPerformUpdateRejectsInvalidListenEndpointAfterRestart(t *testing.T) {
	fx := newUpdateFixture(t)
	t.Setenv(envListenName, "not-an-endpoint")
	daemon := startSleeper(t)
	t.Setenv("FAKE_PGREP_OUT", strconv.Itoa(daemon.Process.Pid)+"\n")
	t.Setenv("FAKE_PGREP_EXIT", "0")

	_ = captureStdio(t)
	err := performUpdate(resolveTo(fx.binPath), time.Second)
	if err == nil || !strings.Contains(err.Error(), "not-an-endpoint") {
		t.Fatalf("error = %v, want the parse error naming the bad endpoint", err)
	}
	if got := readFileString(t, fx.binPath); got != releasedContents {
		t.Errorf("binary contents = %q, want the released binary", got)
	}
	if sig := waitSignal(t, daemon); sig != syscall.SIGTERM {
		t.Errorf("daemon ended by %v, want SIGTERM", sig)
	}
}

// The daemon cannot be signaled when its pid has already gone, and the update
// then must not wait on a health endpoint nobody is restarting.
func TestPerformUpdateSkipsHealthWaitWhenSignalFails(t *testing.T) {
	fx := newUpdateFixture(t)
	gone := startSleeper(t)
	if err := gone.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = waitSignal(t, gone)
	t.Setenv("FAKE_PGREP_OUT", strconv.Itoa(gone.Process.Pid)+"\n")
	t.Setenv("FAKE_PGREP_EXIT", "0")
	t.Setenv(envListenName, "not-an-endpoint")

	out := captureStdio(t)
	err := performUpdate(resolveTo(fx.binPath), time.Second)
	stdout, _ := out()
	if err != nil {
		t.Fatalf("performUpdate() error: %v", err)
	}
	if !strings.Contains(stdout, "Could not restart daemon (PID "+strconv.Itoa(gone.Process.Pid)+")") {
		t.Errorf("stdout = %q, want the signal failure notice", stdout)
	}
}
