package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
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
        <string>--port</string>
        <string>%d</string>
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

func runService() error {
	if len(os.Args) < 3 {
		fmt.Println("Usage: op-forward service [install|uninstall]")
		return nil
	}

	if runtime.GOOS != "darwin" {
		return fmt.Errorf("service management is only supported on macOS")
	}

	switch os.Args[2] {
	case "install":
		return serviceInstall()
	case "uninstall":
		return serviceUninstall()
	default:
		return fmt.Errorf("unknown service command: %s", os.Args[2])
	}
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

	port := DefaultPort
	if p := os.Getenv("OP_FORWARD_PORT"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil {
			port = parsed
		}
	}

	logPath := filepath.Join(home, "Library", "Logs", "op-forward.log")
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	plistPath := filepath.Join(plistDir, launchdLabel+".plist")

	if err := os.MkdirAll(plistDir, 0755); err != nil {
		return fmt.Errorf("creating LaunchAgents directory: %w", err)
	}

	plist := fmt.Sprintf(plistTemplate, launchdLabel, binPath, port, home, logPath, logPath)
	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		return fmt.Errorf("writing plist: %w", err)
	}

	// Unload if already loaded, then load
	exec.Command("launchctl", "unload", plistPath).Run()
	if err := exec.Command("launchctl", "load", "-w", plistPath).Run(); err != nil {
		// Try bootstrap for newer macOS
		uid := ""
		if u, uerr := user.Current(); uerr == nil {
			uid = u.Uid
		}
		if uid == "" {
			uid = "501"
		}
		exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%s", uid), plistPath).Run()
		if err := exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%s", uid), plistPath).Run(); err != nil {
			return fmt.Errorf("loading launchd service: %w", err)
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

	exec.Command("launchctl", "unload", plistPath).Run()
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing plist: %w", err)
	}

	fmt.Println("Launchd service uninstalled.")
	return nil
}
