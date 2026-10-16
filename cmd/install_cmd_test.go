package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installFixture prepares an isolated HOME and a directory holding a stand-in
// for the real op binary. PATH is left for each test to set, because the PATH
// ordering is exactly what the install logic reasons about.
type installFixture struct {
	home    string
	shimDir string
	shim    string
	realDir string
	realOp  string
}

func newInstallFixture(t *testing.T) installFixture {
	t.Helper()
	withUmask(t, 0o022)
	home := tempHome(t)
	realDir := t.TempDir()
	realOp := filepath.Join(realDir, "op")
	writeFile(t, realOp, "#!/bin/sh\nexit 0\n", 0o755)
	shimDir := filepath.Join(home, ".local", "bin")
	return installFixture{
		home:    home,
		shimDir: shimDir,
		shim:    filepath.Join(shimDir, "op"),
		realDir: realDir,
		realOp:  realOp,
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

func TestRunInstallWritesExecutableShimWithRecordedPaths(t *testing.T) {
	fx := newInstallFixture(t)
	t.Setenv("PATH", fx.realDir)
	recordedBin := filepath.Join(fx.shimDir, "op-forward")
	writeFile(t, recordedBin, "#!/bin/sh\n", 0o755)

	setArgs(t, "op-forward", "install")
	out := captureStdio(t)
	err := runInstall()
	stdout, _ := out()
	if err != nil {
		t.Fatalf("runInstall() error: %v", err)
	}

	assertMode(t, fx.shim, 0o755)
	shim := readFileString(t, fx.shim)
	for _, want := range []string{
		`REAL_OP="` + fx.realOp + `"`,
		`OP_FORWARD_BIN="` + recordedBin + `"`,
	} {
		if !strings.Contains(shim, want) {
			t.Errorf("shim does not contain %s", want)
		}
	}
	if strings.Contains(shim, "{{") {
		t.Errorf("shim still contains an unreplaced template placeholder")
	}

	for _, want := range []string{
		"Shim installed:",
		"shim:      " + fx.shim,
		"real bin:  " + fx.realOp,
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout is missing %q:\n%s", want, stdout)
		}
	}
}

// When op-forward is not next to the shim, the installer records the path of
// the running executable instead, so the shim still points at a real binary.
func TestRunInstallRecordsRunningExecutableWhenNoSiblingBinary(t *testing.T) {
	fx := newInstallFixture(t)
	t.Setenv("PATH", fx.realDir)
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("cannot determine the running executable: %v", err)
	}

	setArgs(t, "op-forward", "install")
	_ = captureStdio(t)
	if err := runInstall(); err != nil {
		t.Fatalf("runInstall() error: %v", err)
	}

	if want := `OP_FORWARD_BIN="` + exe + `"`; !strings.Contains(readFileString(t, fx.shim), want) {
		t.Errorf("shim does not record the running executable %s", exe)
	}
}

func TestRunInstallReportsWhenShimWinsPathLookup(t *testing.T) {
	fx := newInstallFixture(t)
	t.Setenv("PATH", fx.shimDir+string(os.PathListSeparator)+fx.realDir)
	// A shim from an earlier install already sits first on PATH.
	writeFile(t, fx.shim, "stale shim from an earlier install", 0o755)

	setArgs(t, "op-forward", "install")
	out := captureStdio(t)
	err := runInstall()
	stdout, _ := out()
	if err != nil {
		t.Fatalf("runInstall() error: %v", err)
	}

	if want := "'which op' resolves to " + fx.shim + " (shim)\n"; !strings.Contains(stdout, want) {
		t.Errorf("stdout is missing %q:\n%s", want, stdout)
	}
	if strings.Contains(stdout, "NOT shim") {
		t.Errorf("stdout warns about PATH ordering although the shim is first:\n%s", stdout)
	}

	// Re-running the installer must replace the old shim and must never record
	// the shim as the real binary, which would make the shim exec itself.
	shim := readFileString(t, fx.shim)
	if strings.Contains(shim, "stale shim") {
		t.Errorf("the previous shim was not replaced")
	}
	if strings.Contains(shim, `REAL_OP="`+fx.shim+`"`) {
		t.Errorf("the shim recorded itself as the real op binary")
	}
	if want := `REAL_OP="` + fx.realOp + `"`; !strings.Contains(shim, want) {
		t.Errorf("shim does not contain %s", want)
	}
}

func TestRunInstallWarnsWhenRealOpShadowsShim(t *testing.T) {
	fx := newInstallFixture(t)
	t.Setenv("PATH", fx.realDir+string(os.PathListSeparator)+fx.shimDir)

	setArgs(t, "op-forward", "install")
	out := captureStdio(t)
	err := runInstall()
	stdout, _ := out()
	if err != nil {
		t.Fatalf("runInstall() error: %v", err)
	}

	want := "'which op' resolves to " + fx.realOp + " (NOT shim"
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout is missing %q:\n%s", want, stdout)
	}
	if !strings.Contains(stdout, fx.shimDir+" is first in PATH") {
		t.Errorf("warning does not tell the operator which directory to put first:\n%s", stdout)
	}
}

func TestRunInstallFallsBackToDefaultOpWhenNoneOnPath(t *testing.T) {
	fx := newInstallFixture(t)
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	setArgs(t, "op-forward", "install")
	out := captureStdio(t)
	err := runInstall()
	stdout, _ := out()
	if err != nil {
		t.Fatalf("runInstall() error: %v", err)
	}

	if want := `REAL_OP="/usr/bin/op"`; !strings.Contains(readFileString(t, fx.shim), want) {
		t.Errorf("shim does not fall back to /usr/bin/op")
	}
	if strings.Contains(stdout, "PATH:") {
		t.Errorf("no PATH verification line is expected when op cannot be resolved:\n%s", stdout)
	}
}

// --port predates the environment-driven shim and is still passed by older
// deployment scripts. It must be accepted and must not be baked into the shim.
func TestRunInstallAcceptsLegacyPortFlag(t *testing.T) {
	fx := newInstallFixture(t)
	t.Setenv("PATH", fx.realDir)

	setArgs(t, "op-forward", "install", "--port", "9876")
	_ = captureStdio(t)
	if err := runInstall(); err != nil {
		t.Fatalf("runInstall(--port) error: %v", err)
	}
	if strings.Contains(readFileString(t, fx.shim), "9876") {
		t.Errorf("the legacy port value leaked into the shim")
	}
}

func TestExecuteInstallDispatchesToInstaller(t *testing.T) {
	fx := newInstallFixture(t)
	t.Setenv("PATH", fx.realDir)

	setArgs(t, "op-forward", "install")
	_ = captureStdio(t)
	if err := Execute(); err != nil {
		t.Fatalf("Execute(install) error: %v", err)
	}
	assertMode(t, fx.shim, 0o755)
}

func TestRunInstallFailsWhenShimDirectoryCannotBeCreated(t *testing.T) {
	fx := newInstallFixture(t)
	t.Setenv("PATH", fx.realDir)
	// A regular file where the ".local" directory should be blocks MkdirAll.
	writeFile(t, filepath.Join(fx.home, ".local"), "not a directory", 0o644)

	setArgs(t, "op-forward", "install")
	_ = captureStdio(t)
	err := runInstall()
	if err == nil || !strings.Contains(err.Error(), "creating shim directory") {
		t.Fatalf("error = %v, want a 'creating shim directory' error", err)
	}
}

func TestRunInstallFailsWhenShimCannotBeWritten(t *testing.T) {
	fx := newInstallFixture(t)
	t.Setenv("PATH", fx.realDir)
	// A directory squatting on the shim path makes the final write fail.
	if err := os.MkdirAll(fx.shim, 0o755); err != nil {
		t.Fatal(err)
	}

	setArgs(t, "op-forward", "install")
	out := captureStdio(t)
	err := runInstall()
	stdout, _ := out()
	if err == nil || !strings.Contains(err.Error(), "writing shim") {
		t.Fatalf("error = %v, want a 'writing shim' error", err)
	}
	if strings.Contains(stdout, "Shim installed") {
		t.Errorf("success banner printed although the shim was not written:\n%s", stdout)
	}
}

// ---------- findRealOp ----------

func TestFindRealOpSkipsShimDirectory(t *testing.T) {
	fx := newInstallFixture(t)
	// The shim itself is an executable named op in the first PATH entry.
	writeFile(t, fx.shim, "#!/bin/sh\n", 0o755)
	empty := t.TempDir()
	t.Setenv("PATH", strings.Join([]string{fx.shimDir, empty, fx.realDir}, string(os.PathListSeparator)))

	got := findRealOp()
	if got == fx.shim {
		t.Fatalf("findRealOp() returned the shim %s; the shim would exec itself", got)
	}
	if got != fx.realOp {
		t.Fatalf("findRealOp() = %q, want %q", got, fx.realOp)
	}
}

func TestFindRealOpPicksFirstMatchInPathOrder(t *testing.T) {
	fx := newInstallFixture(t)
	second := t.TempDir()
	writeFile(t, filepath.Join(second, "op"), "#!/bin/sh\n", 0o755)
	t.Setenv("PATH", fx.realDir+string(os.PathListSeparator)+second)

	if got := findRealOp(); got != fx.realOp {
		t.Fatalf("findRealOp() = %q, want the first match %q", got, fx.realOp)
	}
}

func TestFindRealOpIgnoresDirectoriesNamedOp(t *testing.T) {
	fx := newInstallFixture(t)
	decoy := t.TempDir()
	if err := os.Mkdir(filepath.Join(decoy, "op"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", decoy+string(os.PathListSeparator)+fx.realDir)

	if got := findRealOp(); got != fx.realOp {
		t.Fatalf("findRealOp() = %q, want %q (a directory named op is not a binary)", got, fx.realOp)
	}
}

func TestFindRealOpFallsBackToUsrBinOp(t *testing.T) {
	_ = newInstallFixture(t)
	t.Setenv("PATH", t.TempDir())

	if got := findRealOp(); got != "/usr/bin/op" {
		t.Fatalf("findRealOp() = %q, want the /usr/bin/op default", got)
	}
}
