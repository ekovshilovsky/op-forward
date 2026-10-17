package cmd

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"

	"github.com/ekovshilovsky/op-forward/internal/endpoint"
)

const launchdLabel = "com.op-forward.daemon"

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>serve</string>
        <string>--listen</string>
        <string>%s</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
        <key>HOME</key>
        <string>%s</string>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
</dict>
</plist>
`

// renderPlist fills the launchd template, escaping every operator-supplied
// value for XML. The listen endpoint and the paths can legitimately contain
// characters such as "&" that would otherwise corrupt the plist and leave a
// broken LaunchAgent behind.
func renderPlist(label, binPath, listen, home, logPath string) string {
	return fmt.Sprintf(plistTemplate, xmlText(label), xmlText(binPath), xmlText(listen), xmlText(home), xmlText(logPath), xmlText(logPath))
}

func xmlText(s string) string {
	var buf bytes.Buffer
	// EscapeText only returns errors surfaced by the destination writer, and
	// bytes.Buffer writes never fail.
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func runService() error {
	return dispatchService(runtime.GOOS, os.Args)
}

// dispatchService routes the service subcommand found in args, where args has
// the shape of os.Args. Service management is launchd-based, so any goos other
// than "darwin" is refused before a subcommand is acted on.
func dispatchService(goos string, args []string) error {
	if len(args) < 3 {
		fmt.Println("Usage: op-forward service [install|uninstall]")
		return nil
	}

	if goos != "darwin" {
		if goos == "windows" {
			return fmt.Errorf("service management is only supported on macOS; on Windows run op-forward serve in a terminal, or register op-forward.exe serve as a Task Scheduler task or service yourself")
		}
		return fmt.Errorf("service management is only supported on macOS")
	}

	switch args[2] {
	case "install":
		return serviceInstall()
	case "uninstall":
		return serviceUninstall()
	default:
		return fmt.Errorf("unknown service command: %s", args[2])
	}
}

// defaultLaunchdUID is the user ID of the first account created on macOS. It
// addresses the GUI domain when the current user cannot be determined.
const defaultLaunchdUID = "501"

// launchdUID returns the user ID that addresses the per-user launchd GUI
// domain. An empty uid, which is what an unresolvable current user yields,
// selects defaultLaunchdUID.
func launchdUID(uid string) string {
	if uid == "" {
		return defaultLaunchdUID
	}
	return uid
}

func serviceInstall() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("determining home directory: %w", err)
	}

	binPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("determining binary path: %w", err)
	}
	// Resolve symlinks so the plist references the actual binary location
	binPath, err = filepath.EvalSymlinks(binPath)
	if err != nil {
		return fmt.Errorf("resolving binary path: %w", err)
	}

	// The endpoint is resolved now and written into the plist, because
	// launchd does not inherit the installing shell's environment.
	ep, err := endpoint.ForListen(os.Getenv(endpoint.EnvListen), endpoint.PortFromEnv())
	if err != nil {
		return err
	}

	logPath := filepath.Join(home, "Library", "Logs", "op-forward.log")
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	plistPath := filepath.Join(plistDir, launchdLabel+".plist")

	// #nosec G301 -- ~/Library/LaunchAgents is created with the 0755 mode macOS itself uses for it; it contains only launchd job definitions, never secrets
	if err := os.MkdirAll(plistDir, 0755); err != nil {
		return fmt.Errorf("creating LaunchAgents directory: %w", err)
	}

	plist := renderPlist(launchdLabel, binPath, ep.String(), home, logPath)
	// #nosec G306 -- launchd job definitions use the standard 0644 mode and launchd only requires that they not be group or world writable; the plist holds the binary path, listen endpoint and log paths, never a token
	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		return fmt.Errorf("writing plist: %w", err)
	}

	// Unload if already loaded, then load. The unload failing is expected on a
	// first install, when no job is loaded yet, so its result is discarded.
	// #nosec G204 -- fixed launchctl binary name; the only variable argument is the plist path, derived from the current user's home directory and a constant label for a file this process just wrote
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	// #nosec G204 -- fixed launchctl binary name; the only variable argument is the plist path, derived from the current user's home directory and a constant label for a file this process just wrote
	loadErr := exec.Command("launchctl", "load", "-w", plistPath).Run()
	if loadErr != nil {
		// Try bootstrap for newer macOS
		uid := ""
		if u, uerr := user.Current(); uerr == nil {
			uid = u.Uid
		}
		domain := fmt.Sprintf("gui/%s", launchdUID(uid))
		// As with the unload above, a failed bootout only means nothing was
		// bootstrapped for this user yet, so its result is discarded.
		// #nosec G204 -- fixed launchctl binary name; the domain target is built from the uid reported by user.Current (or the default 501) and the plist path is one this process just wrote
		_ = exec.Command("launchctl", "bootout", domain, plistPath).Run()
		// #nosec G204 -- fixed launchctl binary name; the domain target is built from the uid reported by user.Current (or the default 501) and the plist path is one this process just wrote
		bootstrapErr := exec.Command("launchctl", "bootstrap", domain, plistPath).Run()
		if bootstrapErr != nil {
			return fmt.Errorf("loading launchd service: %w", bootstrapErr)
		}
	}

	fmt.Printf("Launchd service installed and loaded.\n")
	fmt.Printf("  plist: %s\n", plistPath)
	fmt.Printf("  logs:  %s\n", logPath)
	return nil
}

func serviceUninstall() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("determining home directory: %w", err)
	}

	plistPath := filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")

	// Unload fails when the job is not loaded, which is an acceptable state
	// for an uninstall; the plist removal below is the step that matters.
	// #nosec G204 -- fixed launchctl binary name; the only variable argument is the plist path, derived from the current user's home directory and a constant label
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing plist: %w", err)
	}

	fmt.Println("Launchd service uninstalled.")
	return nil
}
