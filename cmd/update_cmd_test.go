//go:build !windows

package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const testTag = "v1.2.3"

// ---------- fetchLatestRelease ----------

func TestFetchLatestReleaseParsesReleaseMetadata(t *testing.T) {
	assets := []githubAsset{
		{Name: "op-forward_1.2.3_linux_amd64.tar.gz", BrowserDownloadURL: assetURL("a")},
		{Name: "checksums.txt", BrowserDownloadURL: assetURL("b")},
	}
	stub := fakeGitHub(t, releaseJSONFor(t, testTag, assets...), nil)

	release, err := fetchLatestRelease()
	if err != nil {
		t.Fatalf("fetchLatestRelease() error: %v", err)
	}
	if release.TagName != testTag {
		t.Errorf("TagName = %q, want %q", release.TagName, testTag)
	}
	if len(release.Assets) != 2 || release.Assets[0] != assets[0] || release.Assets[1] != assets[1] {
		t.Errorf("Assets = %+v, want %+v", release.Assets, assets)
	}
	wantURL := "https://api.github.com/repos/ekovshilovsky/op-forward/releases/latest"
	if reqs := stub.Requests(); len(reqs) != 1 || reqs[0] != wantURL {
		t.Errorf("requests = %v, want exactly [%s]", reqs, wantURL)
	}
}

func TestFetchLatestReleaseRejectsNon200(t *testing.T) {
	stub := fakeGitHub(t, `{"message":"rate limited"}`, nil)
	stub.APIStatus = http.StatusForbidden

	release, err := fetchLatestRelease()
	if release != nil {
		t.Errorf("release = %+v, want nil", release)
	}
	if err == nil || !strings.Contains(err.Error(), "GitHub API returned status 403") {
		t.Fatalf("error = %v, want the HTTP status", err)
	}
}

func TestFetchLatestReleaseRejectsInvalidJSON(t *testing.T) {
	fakeGitHub(t, `{"tag_name": `, nil)

	release, err := fetchLatestRelease()
	if release != nil {
		t.Errorf("release = %+v, want nil", release)
	}
	if err == nil || !strings.Contains(err.Error(), "parsing release info") {
		t.Fatalf("error = %v, want a 'parsing release info' error", err)
	}
}

func TestFetchLatestReleaseReportsTransportFailure(t *testing.T) {
	stub := fakeGitHub(t, "", nil)
	cause := errors.New("network unreachable")
	stub.APIErr = cause

	release, err := fetchLatestRelease()
	if release != nil {
		t.Errorf("release = %+v, want nil", release)
	}
	if err == nil || !strings.Contains(err.Error(), "checking for updates") || !errors.Is(err, cause) {
		t.Fatalf("error = %v, want a 'checking for updates' error wrapping the cause", err)
	}
}

// ---------- downloadReleaseBinary ----------

func TestDownloadReleaseBinaryPicksExactPlatformAsset(t *testing.T) {
	otherArch := "arm64"
	if runtime.GOARCH == "arm64" {
		otherArch = "amd64"
	}
	want := platformAssetName("1.2.3")
	release := &githubRelease{TagName: testTag, Assets: []githubAsset{
		{Name: "op-forward_1.2.3_" + runtime.GOOS + "_" + otherArch + ".tar.gz", BrowserDownloadURL: assetURL("wrong-arch")},
		{Name: platformAssetName("9.9.9"), BrowserDownloadURL: assetURL("wrong-version")},
		{Name: want, BrowserDownloadURL: assetURL(want)},
		{Name: "checksums.txt", BrowserDownloadURL: assetURL("checksums")},
	}}
	archive := makeTarGz(t,
		tarEntry{Name: "LICENSE", Body: "license text"},
		tarEntry{Name: "op-forward", Body: "#!new-binary"},
	)
	stub := fakeGitHub(t, "", archive)

	out := captureStdio(t)
	data, err := downloadReleaseBinary(release, "1.2.3")
	stdout, _ := out()
	if err != nil {
		t.Fatalf("downloadReleaseBinary() error: %v", err)
	}
	if string(data) != "#!new-binary" {
		t.Errorf("binary = %q, want the op-forward archive entry", data)
	}
	if reqs := stub.Requests(); len(reqs) != 1 || reqs[0] != assetURL(want) {
		t.Errorf("requests = %v, want only the exact platform asset %s", reqs, assetURL(want))
	}
	if !strings.Contains(stdout, "Downloading "+want+"...") {
		t.Errorf("stdout = %q, want the download announcement", stdout)
	}
}

func TestDownloadReleaseBinaryFailsWhenPlatformAssetMissing(t *testing.T) {
	release := &githubRelease{TagName: testTag, Assets: []githubAsset{
		{Name: "op-forward_1.2.3_plan9_mips.tar.gz", BrowserDownloadURL: assetURL("other")},
	}}
	stub := fakeGitHub(t, "", nil)

	data, err := downloadReleaseBinary(release, "1.2.3")
	if data != nil {
		t.Errorf("data = %q, want nil", data)
	}
	want := "no release binary found for " + runtime.GOOS + "/" + runtime.GOARCH
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want %q", err, want)
	}
	if reqs := stub.Requests(); len(reqs) != 0 {
		t.Errorf("nothing may be downloaded when no asset matches, got %v", reqs)
	}
}

func platformRelease() *githubRelease {
	name := platformAssetName("1.2.3")
	return &githubRelease{TagName: testTag, Assets: []githubAsset{{Name: name, BrowserDownloadURL: assetURL(name)}}}
}

func TestDownloadReleaseBinaryRejectsNon200(t *testing.T) {
	stub := fakeGitHub(t, "", []byte("Not Found"))
	stub.AssetStatus = http.StatusNotFound

	_ = captureStdio(t)
	data, err := downloadReleaseBinary(platformRelease(), "1.2.3")
	if data != nil {
		t.Errorf("data = %q, want nil", data)
	}
	if err == nil || !strings.Contains(err.Error(), "download returned status 404") {
		t.Fatalf("error = %v, want the HTTP status", err)
	}
}

func TestDownloadReleaseBinaryReportsTransportFailure(t *testing.T) {
	stub := fakeGitHub(t, "", nil)
	cause := errors.New("connection reset")
	stub.AssetErr = cause

	_ = captureStdio(t)
	_, err := downloadReleaseBinary(platformRelease(), "1.2.3")
	if err == nil || !strings.Contains(err.Error(), "downloading update") || !errors.Is(err, cause) {
		t.Fatalf("error = %v, want a 'downloading update' error wrapping the cause", err)
	}
}

func TestDownloadReleaseBinaryRejectsCorruptArchive(t *testing.T) {
	fakeGitHub(t, "", []byte("this is not a gzip stream"))

	_ = captureStdio(t)
	data, err := downloadReleaseBinary(platformRelease(), "1.2.3")
	if data != nil {
		t.Errorf("data = %q, want nil", data)
	}
	if err == nil || !strings.Contains(err.Error(), "decompressing update") {
		t.Fatalf("error = %v, want a 'decompressing update' error", err)
	}
}

// ---------- extractBinaryFromTarGz ----------

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// truncatedEntryTarGz builds a well-formed gzip stream whose tar payload
// declares a 100-byte op-forward entry but ends after ten bytes.
func truncatedEntryTarGz(t *testing.T) []byte {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	if err := tw.WriteHeader(&tar.Header{Name: "op-forward", Typeflag: tar.TypeReg, Mode: 0o755, Size: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	// The tar writer is intentionally neither flushed nor closed: doing so
	// would fail because 90 declared bytes were never written.
	return gzipBytes(t, raw.Bytes())
}

func TestExtractBinaryFromTarGz(t *testing.T) {
	large := strings.Repeat("0123456789abcdef", 8192) // 128 KiB, spans many tar blocks

	cases := []struct {
		name    string
		archive func(t *testing.T) []byte
		want    string // expected binary contents on success
		wantErr string // expected error fragment on failure
	}{
		{
			name: "valid archive returns the op-forward entry",
			archive: func(t *testing.T) []byte {
				return makeTarGz(t, tarEntry{Name: "README.md", Body: "docs"}, tarEntry{Name: "op-forward", Body: "binary-v2"})
			},
			want: "binary-v2",
		},
		{
			name: "large entry is read in full",
			archive: func(t *testing.T) []byte {
				return makeTarGz(t, tarEntry{Name: "op-forward", Body: large})
			},
			want: large,
		},
		{
			name: "archive without the entry",
			archive: func(t *testing.T) []byte {
				return makeTarGz(t, tarEntry{Name: "README.md", Body: "docs"})
			},
			wantErr: "op-forward binary not found in tarball",
		},
		{
			// Only the exact top-level name matches. A nested copy must not be
			// mistaken for the binary.
			name: "entry nested in a directory is not matched",
			archive: func(t *testing.T) []byte {
				return makeTarGz(t, tarEntry{Name: "dist/", Dir: true}, tarEntry{Name: "dist/op-forward", Body: "nested"})
			},
			wantErr: "op-forward binary not found in tarball",
		},
		{
			name: "backup file with a similar name is not matched",
			archive: func(t *testing.T) []byte {
				return makeTarGz(t, tarEntry{Name: "op-forward.bak", Body: "stale"})
			},
			wantErr: "op-forward binary not found in tarball",
		},
		{
			name:    "stream that is not gzip",
			archive: func(t *testing.T) []byte { return []byte("plain text, definitely not gzip") },
			wantErr: "decompressing update",
		},
		{
			name: "gzip stream that is not a tar archive",
			archive: func(t *testing.T) []byte {
				return gzipBytes(t, bytes.Repeat([]byte("x"), 1024))
			},
			wantErr: "reading tarball",
		},
		{
			name:    "entry shorter than its declared size",
			archive: truncatedEntryTarGz,
			wantErr: "extracting binary",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := extractBinaryFromTarGz(bytes.NewReader(tc.archive(t)))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
				}
				if data != nil {
					t.Errorf("data = %d bytes, want nil on error", len(data))
				}
				return
			}
			if err != nil {
				t.Fatalf("extractBinaryFromTarGz() error: %v", err)
			}
			if string(data) != tc.want {
				t.Errorf("extracted %d bytes, want %d", len(data), len(tc.want))
			}
		})
	}
}

// ---------- waitForHealth ----------

func TestWaitForHealthReportsNon200Status(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "starting up", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	ep := serverEndpoint(t, srv)

	err := waitForHealth(ep, 700*time.Millisecond)
	if err == nil {
		t.Fatal("waitForHealth() succeeded against a daemon answering 503")
	}
	for _, want := range []string{"no healthy response within 700ms", "health returned HTTP 503"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

// ---------- findDaemonPID ----------

const fakePgrepScript = `
printf '%s' "$FAKE_PGREP_OUT"
exit "${FAKE_PGREP_EXIT:-0}"
`

func TestFindDaemonPID(t *testing.T) {
	cases := []struct {
		name string
		out  string
		exit string
		want int
	}{
		{"no output and success status", "", "0", 0},
		{"no match exit status", "", "1", 0},
		{"non-numeric output", "not-a-pid\n", "0", 0},
		{"the test process itself is never returned", strconv.Itoa(os.Getpid()) + "\n", "0", 0},
		{"single valid pid", "4242\n", "0", 4242},
		{"first line wins when several match", "4242\n5151\n", "0", 4242},
		{"surrounding whitespace is ignored", "  4242  \n", "0", 4242},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logPath := fakeBin(t, "pgrep", fakePgrepScript)
			t.Setenv("FAKE_PGREP_OUT", tc.out)
			t.Setenv("FAKE_PGREP_EXIT", tc.exit)

			if got := findDaemonPID("/opt/op-forward"); got != tc.want {
				t.Fatalf("findDaemonPID() = %d, want %d", got, tc.want)
			}
			// The pattern anchors on the binary path so unrelated processes
			// named op-forward are not matched.
			assertCalls(t, logPath, []string{"-f /opt/op-forward serve"})
		})
	}
}

// ---------- restartDaemon ----------

func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting sleeper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

// waitSignal waits for the child to exit and returns the signal that ended it.
func waitSignal(t *testing.T, cmd *exec.Cmd) syscall.Signal {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		// A child killed by a signal reports a non-nil error from Wait;
		// which signal it was is asserted from ProcessState below.
		if err == nil {
			t.Fatal("expected the signaled child's Wait to report an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child process did not exit")
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		t.Fatalf("child was not ended by a signal: %v", cmd.ProcessState)
	}
	return status.Signal()
}

func TestRestartDaemonWithoutRunningDaemonSignalsNothing(t *testing.T) {
	logPath := fakeBin(t, "pgrep", fakePgrepScript)
	t.Setenv("FAKE_PGREP_EXIT", "1")
	bystander := startSleeper(t)

	out := captureStdio(t)
	restarted := restartDaemon("/opt/op-forward")
	stdout, _ := out()

	if restarted {
		t.Fatal("restartDaemon() = true although no daemon was found")
	}
	if !strings.Contains(stdout, "No running daemon found") {
		t.Errorf("stdout = %q, want the no-daemon notice", stdout)
	}
	assertCalls(t, logPath, []string{"-f /opt/op-forward serve"})

	// A pid of zero would signal the caller's whole process group, so the
	// bystander child must still be alive and untouched.
	if err := bystander.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("bystander process is gone: %v", err)
	}
	_ = bystander.Process.Kill()
	if sig := waitSignal(t, bystander); sig != syscall.SIGKILL {
		t.Errorf("bystander ended by %v, want only our own SIGKILL", sig)
	}
}

func TestRestartDaemonTerminatesTheDaemonProcess(t *testing.T) {
	fakeBin(t, "pgrep", fakePgrepScript)
	daemon := startSleeper(t)
	t.Setenv("FAKE_PGREP_OUT", strconv.Itoa(daemon.Process.Pid)+"\n")

	out := captureStdio(t)
	restarted := restartDaemon("/opt/op-forward")
	stdout, _ := out()

	if !restarted {
		t.Fatal("restartDaemon() = false although a daemon was signaled")
	}
	if sig := waitSignal(t, daemon); sig != syscall.SIGTERM {
		t.Errorf("daemon ended by %v, want SIGTERM", sig)
	}
	if want := "Daemon stopped (PID " + strconv.Itoa(daemon.Process.Pid) + ")"; !strings.Contains(stdout, want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout, want)
	}
}

func TestRestartDaemonReportsSignalFailure(t *testing.T) {
	fakeBin(t, "pgrep", fakePgrepScript)
	// A process that has already exited and been reaped gives a pid that no
	// longer exists, so delivering the signal fails with "no such process".
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatalf("running helper process: %v", err)
	}
	pid := gone.Process.Pid
	t.Setenv("FAKE_PGREP_OUT", strconv.Itoa(pid)+"\n")

	out := captureStdio(t)
	restarted := restartDaemon("/opt/op-forward")
	stdout, _ := out()

	if restarted {
		t.Fatal("restartDaemon() = true although the signal could not be delivered")
	}
	if want := "Could not restart daemon (PID " + strconv.Itoa(pid) + ")"; !strings.Contains(stdout, want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout, want)
	}
}

// ---------- runUpdate (paths that stop before replacing the binary) ----------

// runUpdate would overwrite the running test binary if it ever reached
// replaceBinaryAt, so only the paths that return earlier are exercised here;
// the replacement flow itself is driven against temp paths in
// update_perform_test.go.
func TestExecuteUpdateReportsAlreadyUpToDate(t *testing.T) {
	withVersion(t, "1.2.3-test")
	stub := fakeGitHub(t, releaseJSONFor(t, "v1.2.3-test"), nil)
	setArgs(t, "op-forward", "update")

	out := captureStdio(t)
	err := Execute()
	stdout, _ := out()
	if err != nil {
		t.Fatalf("Execute(update) error: %v", err)
	}
	for _, want := range []string{"Current version: 1.2.3-test", "Checking for updates...", "Already up to date (v1.2.3-test)"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout is missing %q:\n%s", want, stdout)
		}
	}
	if reqs := stub.Requests(); len(reqs) != 1 {
		t.Errorf("an up-to-date check must only query release metadata, got %v", reqs)
	}
}

func TestRunUpdateSurfacesReleaseLookupFailure(t *testing.T) {
	stub := fakeGitHub(t, "", nil)
	stub.APIStatus = http.StatusInternalServerError

	_ = captureStdio(t)
	err := runUpdate()
	if err == nil || !strings.Contains(err.Error(), "GitHub API returned status 500") {
		t.Fatalf("error = %v, want the release lookup failure", err)
	}
}

func TestRunUpdateStopsWhenNoAssetMatchesThisPlatform(t *testing.T) {
	withVersion(t, "1.0.0-test")
	stub := fakeGitHub(t, releaseJSONFor(t, "v9.9.9",
		githubAsset{Name: "op-forward_9.9.9_plan9_mips.tar.gz", BrowserDownloadURL: assetURL("other")}), nil)

	out := captureStdio(t)
	err := runUpdate()
	stdout, _ := out()
	if err == nil || !strings.Contains(err.Error(), "no release binary found") {
		t.Fatalf("error = %v, want a 'no release binary found' error", err)
	}
	if !strings.Contains(stdout, "New version available: v1.0.0-test") {
		t.Errorf("stdout = %q, want the new-version notice before the failure", stdout)
	}
	if reqs := stub.Requests(); len(reqs) != 1 {
		t.Errorf("no asset may be downloaded, got requests %v", reqs)
	}
}
