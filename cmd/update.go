package cmd

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ekovshilovsky/op-forward/internal/endpoint"
)

const (
	githubOwner = "ekovshilovsky"
	githubRepo  = "op-forward"
)

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func runUpdate() error {
	fmt.Printf("Current version: %s\n", Version)
	fmt.Println("Checking for updates...")

	release, err := fetchLatestRelease()
	if err != nil {
		return err
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	if latestVersion == Version {
		fmt.Printf("Already up to date (v%s)\n", Version)
		return nil
	}

	fmt.Printf("New version available: v%s → v%s\n", Version, latestVersion)

	newBinary, err := downloadReleaseBinary(release, latestVersion)
	if err != nil {
		return err
	}

	execPath, err := replaceBinary(newBinary)
	if err != nil {
		return err
	}

	fmt.Printf("Updated to v%s\n", latestVersion)
	if restartDaemon(execPath) {
		// launchd respawns the daemon with a short throttle, so the health
		// endpoint is typically unreachable for several seconds. Report the
		// outcome instead of leaving the user to discover a daemon that
		// never came back.
		ep, err := endpoint.ForListen(os.Getenv(endpoint.EnvListen), endpoint.PortFromEnv())
		if err != nil {
			return err
		}
		if err := waitForHealth(ep, 30*time.Second); err != nil {
			return fmt.Errorf("daemon did not come back on %s: %w\nCheck ~/Library/Logs/op-forward.log or restart with: launchctl kickstart -k gui/$(id -u)/com.op-forward.daemon", ep, err)
		}
		fmt.Printf("Daemon is serving again on %s\n", ep)
	}
	return nil
}

// waitForHealth polls the daemon's health endpoint until it answers or the
// timeout elapses.
func waitForHealth(ep endpoint.Endpoint, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := ep.HTTPClient(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(ep.BaseURL() + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("health returned HTTP %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("no healthy response within %s (last error: %v)", timeout, lastErr)
}

// fetchLatestRelease queries the GitHub API for the most recent release.
func fetchLatestRelease() (*githubRelease, error) {
	resp, err := http.Get(fmt.Sprintf(
		"https://api.github.com/repos/%s/%s/releases/latest",
		githubOwner, githubRepo,
	))
	if err != nil {
		return nil, fmt.Errorf("checking for updates: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("parsing release info: %w", err)
	}
	return &release, nil
}

// downloadReleaseBinary finds and downloads the platform-appropriate binary
// from the release assets.
func downloadReleaseBinary(release *githubRelease, version string) ([]byte, error) {
	wantName := fmt.Sprintf("op-forward_%s_%s_%s.tar.gz",
		version, runtime.GOOS, runtime.GOARCH)

	var downloadURL string
	for _, asset := range release.Assets {
		if asset.Name == wantName {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return nil, fmt.Errorf("no release binary found for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	fmt.Printf("Downloading %s...\n", wantName)

	resp, err := http.Get(downloadURL)
	if err != nil {
		return nil, fmt.Errorf("downloading update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	return extractBinaryFromTarGz(resp.Body)
}

// replaceBinary atomically replaces the running binary with new contents.
// Returns the resolved path of the replaced binary.
func replaceBinary(newBinary []byte) (string, error) {
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("determining binary path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return "", fmt.Errorf("resolving binary path: %w", err)
	}

	tmpPath := execPath + ".update"
	if err := os.WriteFile(tmpPath, newBinary, 0755); err != nil {
		return "", fmt.Errorf("writing update: %w", err)
	}
	if err := os.Rename(tmpPath, execPath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("replacing binary: %w", err)
	}
	return execPath, nil
}

// restartDaemon sends SIGTERM to the running op-forward daemon so launchd
// respawns it with the updated binary. It reports whether a daemon was
// signaled, so the caller knows whether to wait for it to come back.
func restartDaemon(binPath string) bool {
	pid := findDaemonPID(binPath)
	if pid == 0 {
		fmt.Println("No running daemon found. It will pick up the new version on next start.")
		return false
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		fmt.Printf("Could not restart daemon (PID %d): %v — restart manually with: launchctl kickstart gui/$(id -u)/com.op-forward.daemon\n", pid, err)
		return false
	}
	fmt.Printf("Daemon stopped (PID %d); waiting for launchd to respawn it...\n", pid)
	return true
}

// findDaemonPID locates the running op-forward serve process.
// Returns 0 if no daemon is found.
func findDaemonPID(binPath string) int {
	out, err := osexec.Command("pgrep", "-f", binPath+" serve").Output()
	if err != nil {
		return 0
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return 0
	}
	parts := strings.SplitN(line, "\n", 2)
	pid, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0
	}
	if pid == os.Getpid() {
		return 0
	}
	return pid
}

// extractBinaryFromTarGz reads a .tar.gz stream and returns the op-forward binary contents.
func extractBinaryFromTarGz(r io.Reader) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("decompressing update: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading tarball: %w", err)
		}
		if hdr.Name == "op-forward" {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("extracting binary: %w", err)
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("op-forward binary not found in tarball")
}
