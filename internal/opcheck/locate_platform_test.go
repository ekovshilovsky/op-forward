package opcheck

import (
	"io/fs"
	"reflect"
	"testing"
	"testing/fstest"
)

func TestCandidateNames(t *testing.T) {
	cases := []struct {
		name    string
		goos    string
		pathExt string
		want    []string
	}{
		{"unix ignores PATHEXT", "linux", ".EXE;.CMD", []string{"op"}},
		{"darwin is bare name", "darwin", "", []string{"op"}},
		{"windows default when unset", "windows", "", []string{"op.com", "op.exe", "op.bat", "op.cmd"}},
		{"windows default when blank", "windows", "  ", []string{"op.com", "op.exe", "op.bat", "op.cmd"}},
		{"windows honors PATHEXT order", "windows", ".EXE;.CMD", []string{"op.exe", "op.cmd"}},
		{"windows tolerates stray separators and missing dot", "windows", ";EXE;; .Bat ;", []string{"op.exe", "op.bat"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := candidateNames(tc.goos, tc.pathExt); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("candidateNames(%q, %q) = %v, want %v", tc.goos, tc.pathExt, got, tc.want)
			}
		})
	}
}

func statOf(t *testing.T, mode fs.FileMode) fs.FileInfo {
	t.Helper()
	fsys := fstest.MapFS{"f": &fstest.MapFile{Mode: mode}}
	info, err := fs.Stat(fsys, "f")
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestIsExecutable(t *testing.T) {
	cases := []struct {
		name string
		goos string
		mode fs.FileMode
		want bool
	}{
		{"unix executable file", "linux", 0o755, true},
		{"unix file without execute bit", "linux", 0o644, false},
		{"unix directory", "darwin", fs.ModeDir | 0o755, false},
		// Go reports synthetic permissions on Windows with no execute bit, so
		// the execute-bit test must not be applied there.
		{"windows file without execute bit", "windows", 0o666, true},
		{"windows read-only file", "windows", 0o444, true},
		{"windows directory", "windows", fs.ModeDir | 0o777, false},
		{"windows reparse-point style irregular file", "windows", fs.ModeIrregular | 0o666, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isExecutable(tc.goos, statOf(t, tc.mode)); got != tc.want {
				t.Errorf("isExecutable(%s, %v) = %v, want %v", tc.goos, tc.mode, got, tc.want)
			}
		})
	}
}
