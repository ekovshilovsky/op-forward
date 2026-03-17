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

	resp, err := http.Get(fmt.Sprintf(
		"https://api.github.com/repos/%s/%s/releases/latest",
		githubOwner, githubRepo,
	))
	if err != nil {
		return fmt.Errorf("checking for updates: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return fmt.Errorf("parsing release info: %w", err)
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	if latestVersion == Version {
		fmt.Printf("Already up to date (v%s)\n", Version)
		return nil
	}

	fmt.Printf("New version available: v%s → v%s\n", Version, latestVersion)

	// Locate the tarball matching the current platform
	wantName := fmt.Sprintf("op-forward_%s_%s_%s.tar.gz",
		latestVersion, runtime.GOOS, runtime.GOARCH)

	var downloadURL string
	for _, asset := range release.Assets {
		if asset.Name == wantName {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("no release binary found for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	fmt.Printf("Downloading %s...\n", wantName)

	dlResp, err := http.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("downloading update: %w", err)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status %d", dlResp.StatusCode)
	}

	newBinary, err := extractBinaryFromTarGz(dlResp.Body)
	if err != nil {
		return err
	}

	// Resolve the real path of the running binary (follows symlinks)
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("determining binary path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("resolving binary path: %w", err)
	}

	// Atomic replacement: write to a temp file in the same directory, then rename
	tmpPath := execPath + ".update"
	if err := os.WriteFile(tmpPath, newBinary, 0755); err != nil {
		return fmt.Errorf("writing update: %w", err)
	}
	if err := os.Rename(tmpPath, execPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("replacing binary: %w", err)
	}

	fmt.Printf("Updated to v%s\n", latestVersion)

	// Signal the running daemon to exit so launchd restarts it with the new binary.
	// The daemon's KeepAlive plist directive causes launchd to respawn it automatically.
	if pid := findDaemonPID(execPath); pid > 0 {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			fmt.Printf("Could not restart daemon (PID %d): %v — restart manually with: launchctl kickstart gui/$(id -u)/com.op-forward.daemon\n", pid, err)
		} else {
			fmt.Printf("Daemon restarted (PID %d terminated, launchd will respawn).\n", pid)
		}
	} else {
		fmt.Println("No running daemon found. It will pick up the new version on next start.")
	}

	return nil
}

// findDaemonPID locates the running op-forward serve process by scanning /proc
// or using the binary path. Returns 0 if no daemon is found.
func findDaemonPID(binPath string) int {
	// Use pgrep to find the daemon PID — portable across macOS and Linux
	out, err := osexec.Command("pgrep", "-f", binPath+" serve").Output()
	if err != nil {
		return 0
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return 0
	}
	// Take the first PID if multiple lines
	parts := strings.SplitN(line, "\n", 2)
	pid, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0
	}
	// Don't kill ourselves
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
