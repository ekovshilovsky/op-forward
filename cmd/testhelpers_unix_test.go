//go:build !windows

package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// fakeBin installs an executable shell script named name into a fresh temp
// directory and prepends that directory to PATH for the rest of the test. The
// script first appends its own arguments ("$@" joined by spaces, one line per
// invocation) to a log file and then runs the caller-supplied body. The path
// of that log file is returned; it does not exist until the script has run at
// least once.
//
// The body selects its behavior from environment variables the test sets with
// t.Setenv (for example FAKE_EXIT_LOAD=1), because the script inherits the
// test process environment.
func fakeBin(t *testing.T, name, script string) string {
	t.Helper()

	dir := t.TempDir()
	logPath := filepath.Join(dir, name+".log")

	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&sb, "printf '%%s\\n' \"$*\" >> '%s'\n", logPath)
	sb.WriteString(script)
	sb.WriteString("\n")

	binPath := filepath.Join(dir, name)
	// #nosec G306 -- the fake binary is a test fixture that must be executable
	if err := os.WriteFile(binPath, []byte(sb.String()), 0o755); err != nil {
		t.Fatalf("writing fake %s: %v", name, err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// readLogLines returns the non-empty lines of a fakeBin log file, or nil when
// the script never ran (the log file was never created).
func readLogLines(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading %s: %v", logPath, err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// fakeAssetHost is the host portion of every release asset URL the GitHub
// stub serves. The tests build their release metadata with assetURL so the
// stub recognizes the downloads.
const fakeAssetHost = "github.com"

// assetURL returns the download URL the stub serves for a release asset name.
func assetURL(name string) string {
	return "https://" + fakeAssetHost + "/ekovshilovsky/op-forward/releases/download/test/" + name
}

// githubStub is the handle returned by fakeGitHub. Tests adjust its fields to
// drive the failure branches of the update helpers, and read Requests to
// assert which URLs were actually fetched.
type githubStub struct {
	mu sync.Mutex

	// APIStatus and AssetStatus override the HTTP status of the release
	// metadata and asset responses. Zero means 200.
	APIStatus   int
	AssetStatus int
	// APIErr and AssetErr make the corresponding request fail at the
	// transport layer, as a dropped network connection would.
	APIErr   error
	AssetErr error

	releaseJSON string
	tarball     []byte
	requests    []string
}

// Requests returns the URLs of every intercepted (non-loopback) request, in
// order.
func (g *githubStub) Requests() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.requests...)
}

// fakeGitHub swaps http.DefaultTransport for a RoundTripper that serves the
// GitHub release API response and the release asset from memory. Requests to
// 127.0.0.1 (httptest servers) pass through to the real transport so tests can
// mix the stub with local servers. A request to any other host fails the test,
// which keeps every run hermetic: an unintended outbound call is a test
// failure instead of a silent network dependency.
//
// http.Get and the clients built by endpoint.HTTPClient both resolve the
// default transport at request time, so swapping it here intercepts them.
func fakeGitHub(t *testing.T, releaseJSON string, tarball []byte) *githubStub {
	t.Helper()

	stub := &githubStub{releaseJSON: releaseJSON, tarball: tarball}
	base := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		host := req.URL.Hostname()
		if host == "127.0.0.1" {
			return base.RoundTrip(req)
		}

		stub.mu.Lock()
		stub.requests = append(stub.requests, req.URL.String())
		apiStatus, assetStatus := stub.APIStatus, stub.AssetStatus
		apiErr, assetErr := stub.APIErr, stub.AssetErr
		stub.mu.Unlock()

		switch host {
		case "api.github.com":
			want := fmt.Sprintf("/repos/%s/%s/releases/latest", githubOwner, githubRepo)
			if req.URL.Path != want {
				t.Errorf("unexpected GitHub API path %q, want %q", req.URL.Path, want)
				return nil, fmt.Errorf("unexpected GitHub API path %q", req.URL.Path)
			}
			if apiErr != nil {
				return nil, apiErr
			}
			return stubResponse(req, apiStatus, []byte(stub.releaseJSON)), nil
		case fakeAssetHost:
			if assetErr != nil {
				return nil, assetErr
			}
			return stubResponse(req, assetStatus, stub.tarball), nil
		default:
			t.Errorf("test made a real network request to %s; tests must be hermetic", req.URL)
			return nil, fmt.Errorf("blocked non-hermetic request to %s", req.URL.Host)
		}
	})
	t.Cleanup(func() { http.DefaultTransport = base })
	return stub
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func stubResponse(req *http.Request, status int, body []byte) *http.Response {
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

// platformAssetName is the release asset name downloadReleaseBinary looks for
// on the machine running the tests.
func platformAssetName(version string) string {
	return fmt.Sprintf("op-forward_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
}

// releaseJSONFor renders GitHub release metadata for a tag and a set of assets.
func releaseJSONFor(t *testing.T, tag string, assets ...githubAsset) string {
	t.Helper()
	data, err := json.Marshal(githubRelease{TagName: tag, Assets: assets})
	if err != nil {
		t.Fatalf("encoding release JSON: %v", err)
	}
	return string(data)
}

// tarEntry describes one member of a test archive.
type tarEntry struct {
	Name string
	Body string
	Dir  bool
}

// makeTarGz builds a gzip-compressed tar archive in memory.
func makeTarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.Name, Mode: 0o755}
		if e.Dir {
			hdr.Typeflag = tar.TypeDir
		} else {
			hdr.Typeflag = tar.TypeReg
			hdr.Size = int64(len(e.Body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("writing tar header %s: %v", e.Name, err)
		}
		if !e.Dir {
			if _, err := tw.Write([]byte(e.Body)); err != nil {
				t.Fatalf("writing tar body %s: %v", e.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}
	return buf.Bytes()
}

// withVersion sets the package Version for the test so header assertions
// compare against a distinctive value instead of the "dev" default.
func withVersion(t *testing.T, v string) {
	t.Helper()
	saved := Version
	Version = v
	t.Cleanup(func() { Version = saved })
}

// writeFile writes a fixture file, creating parent directories.
func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// withUmask pins the process umask so file-mode assertions do not depend on
// the invoking user's shell configuration.
func withUmask(t *testing.T, mask int) {
	t.Helper()
	saved := syscall.Umask(mask)
	t.Cleanup(func() { syscall.Umask(saved) })
}
