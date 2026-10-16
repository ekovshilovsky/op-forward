package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- launchdUID ----------

func TestLaunchdUID(t *testing.T) {
	cases := []struct {
		name string
		uid  string
		want string
	}{
		{"reported uid is used as is", "502", "502"},
		{"root", "0", "0"},
		{"unresolvable user falls back to the first macOS account", "", "501"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := launchdUID(tc.uid); got != tc.want {
				t.Fatalf("launchdUID(%q) = %q, want %q", tc.uid, got, tc.want)
			}
		})
	}
}

// ---------- dispatchService ----------

func TestDispatchServicePrintsUsageWithoutSubcommandOnAnyPlatform(t *testing.T) {
	fx := newServiceFixture(t)

	for _, goos := range []string{"darwin", "linux", "windows"} {
		out := captureStdio(t)
		err := dispatchService(goos, []string{"op-forward", "service"})
		stdout, _ := out()
		if err != nil {
			t.Errorf("dispatchService(%s) without a subcommand = %v, want nil", goos, err)
		}
		if !strings.Contains(stdout, "Usage: op-forward service [install|uninstall]") {
			t.Errorf("dispatchService(%s) stdout = %q, want the service usage line", goos, stdout)
		}
	}
	assertCalls(t, fx.logPath, nil)
}

func TestDispatchServiceRefusesNonDarwinBeforeWritingFiles(t *testing.T) {
	fx := newServiceFixture(t)

	for _, goos := range []string{"linux", "windows", "freebsd"} {
		for _, sub := range []string{"install", "uninstall", "bogus"} {
			err := dispatchService(goos, []string{"op-forward", "service", sub})
			if err == nil || err.Error() != "service management is only supported on macOS" {
				t.Errorf("dispatchService(%s, %s) error = %v, want the macOS-only refusal", goos, sub, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(fx.home, "Library")); !os.IsNotExist(err) {
		t.Errorf("no files may be written off macOS (stat err: %v)", err)
	}
	assertCalls(t, fx.logPath, nil)
}

func TestDispatchServiceRoutesInstallAndUninstallOnDarwin(t *testing.T) {
	fx := newServiceFixture(t)
	_ = captureStdio(t)

	if err := dispatchService("darwin", []string{"op-forward", "service", "install"}); err != nil {
		t.Fatalf("dispatchService(install) error: %v", err)
	}
	if _, err := os.Stat(fx.plistPath); err != nil {
		t.Fatalf("install did not write the plist: %v", err)
	}

	if err := dispatchService("darwin", []string{"op-forward", "service", "uninstall"}); err != nil {
		t.Fatalf("dispatchService(uninstall) error: %v", err)
	}
	if _, err := os.Stat(fx.plistPath); !os.IsNotExist(err) {
		t.Fatalf("uninstall did not remove the plist (stat err: %v)", err)
	}
}

func TestDispatchServiceRejectsUnknownSubcommandOnDarwin(t *testing.T) {
	fx := newServiceFixture(t)

	err := dispatchService("darwin", []string{"op-forward", "service", "restart"})
	if err == nil || err.Error() != "unknown service command: restart" {
		t.Fatalf("error = %v, want the unknown-subcommand error", err)
	}
	assertCalls(t, fx.logPath, nil)
	if _, statErr := os.Stat(fx.plistPath); !os.IsNotExist(statErr) {
		t.Errorf("an unknown subcommand must not write a plist (stat err: %v)", statErr)
	}
}
