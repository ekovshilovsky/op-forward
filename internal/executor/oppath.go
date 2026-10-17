package executor

import (
	"fmt"
	"strings"
)

// NotNativeExecutableError reports that the op binary found on PATH is not a
// native Windows executable image, so the daemon will not run it.
//
// Name is the file name only, never the directory: the error is returned to
// remote clients in an HTTP response body and the directory would disclose the
// host user's profile path. Ext is the extension including its leading dot, or
// empty when the name has none.
type NotNativeExecutableError struct {
	Name string
	Ext  string
}

func (e *NotNativeExecutableError) Error() string {
	ext := "no extension"
	if e.Ext != "" {
		ext = fmt.Sprintf("extension %q", e.Ext)
	}
	return fmt.Sprintf(
		"resolved op is %q (%s) but the daemon only runs a native op.exe: "+
			"the official 1Password CLI is op.exe, and a .bat or .cmd file would be run through cmd.exe, "+
			"which re-parses arguments, so the daemon refuses it",
		e.Name, ext)
}

// checkWindowsImage enforces the Windows rule on a resolved path: the
// extension must be exactly ".exe", compared case-insensitively.
//
// The comparison is an allowlist on the whole extension, so it fails closed.
// Windows runs a .bat or .cmd file through cmd.exe, and CreateProcess also
// treats a name with trailing dots or spaces (for example "op.cmd." or
// "op.bat ") as a batch file; none of those equals ".exe" and all are
// refused. The path is split on both separator styles by hand rather than
// with filepath so the rule behaves the same when it is exercised from a
// non-Windows build.
func checkWindowsImage(path string) error {
	name := path
	if i := strings.LastIndexAny(path, `\/`); i >= 0 {
		name = path[i+1:]
	}
	ext := ""
	if i := strings.LastIndex(name, "."); i >= 0 {
		ext = name[i:]
	}
	if strings.EqualFold(ext, ".exe") {
		return nil
	}
	return &NotNativeExecutableError{Name: name, Ext: ext}
}
