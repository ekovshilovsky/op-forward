package executor

import (
	"errors"
	"strings"
	"testing"
)

// The Windows rule is exercised through checkWindowsImage, which is
// platform-neutral, so every build host verifies it.
func TestCheckWindowsImage(t *testing.T) {
	accepted := []string{
		`C:\Program Files\1Password CLI\op.exe`,
		`C:\Tools\op.EXE`,
		`C:\Tools\op.Exe`,
		`C:/Tools/op.exe`,
		`op.exe`,
		`C:\dir.cmd\op.exe`, // A batch-looking directory name does not matter.
	}
	for _, path := range accepted {
		if err := checkWindowsImage(path); err != nil {
			t.Errorf("checkWindowsImage(%q) = %v, want accepted", path, err)
		}
	}

	refused := []struct {
		path, name, ext string
	}{
		{`C:\Tools\op.cmd`, "op.cmd", ".cmd"},
		{`C:\Tools\op.CMD`, "op.CMD", ".CMD"},
		{`C:\Tools\op.bat`, "op.bat", ".bat"},
		{`C:\Tools\op.com`, "op.com", ".com"},
		{`C:\Tools\op.ps1`, "op.ps1", ".ps1"},
		{`C:\Tools\op`, "op", ""},
		{`C:\Tools\op.exe.cmd`, "op.exe.cmd", ".cmd"},
		{`C:\Tools\op.exe.bat`, "op.exe.bat", ".bat"},
		// Windows treats names with trailing dots or spaces as the batch file
		// they resemble, so anything that is not exactly ".exe" must fail.
		{`C:\Tools\op.cmd.`, "op.cmd.", "."},
		{`C:\Tools\op.bat `, "op.bat ", ".bat "},
		{`C:\Tools\op.exe `, "op.exe ", ".exe "},
		{`C:\Tools\op.exe.`, "op.exe.", "."},
		{`C:/Tools/op.cmd`, "op.cmd", ".cmd"},
	}
	for _, tc := range refused {
		err := checkWindowsImage(tc.path)
		var nne *NotNativeExecutableError
		if !errors.As(err, &nne) {
			t.Errorf("checkWindowsImage(%q) = %v, want *NotNativeExecutableError", tc.path, err)
			continue
		}
		if nne.Name != tc.name || nne.Ext != tc.ext {
			t.Errorf("checkWindowsImage(%q) = {Name:%q Ext:%q}, want {Name:%q Ext:%q}", tc.path, nne.Name, nne.Ext, tc.name, tc.ext)
		}
	}
}

func TestNotNativeExecutableErrorExplainsTheRefusal(t *testing.T) {
	err := checkWindowsImage(`C:\Users\someone\bin\op.cmd`)
	if err == nil {
		t.Fatal("checkWindowsImage accepted op.cmd")
	}
	for _, fragment := range []string{`"op.cmd"`, `".cmd"`, "op.exe", "cmd.exe", "re-parses arguments", "refuses"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error = %q, want it to contain %q", err, fragment)
		}
	}
	// The directory is the host user's profile path and the message is
	// returned to remote clients, so it must not appear.
	for _, leak := range []string{"someone", `C:\`, "Users"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error = %q, must not disclose %q", err, leak)
		}
	}
}

func TestNotNativeExecutableErrorWithoutExtension(t *testing.T) {
	err := checkWindowsImage(`C:\Tools\op`)
	if err == nil || !strings.Contains(err.Error(), "no extension") {
		t.Fatalf("error = %v, want it to say the name has no extension", err)
	}
}
