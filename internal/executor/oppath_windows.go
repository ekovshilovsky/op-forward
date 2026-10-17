//go:build windows

package executor

// ValidateResolvedPath checks that the op binary resolved from PATH can be
// executed without a shell. Windows runs a .bat or .cmd file through
// cmd.exe, which re-parses the whole argument string (the CVE-2024-24576
// class), so the resolved file must be a native .exe image; anything else is
// refused with a *NotNativeExecutableError. This is what makes "no shell is
// involved" true for the daemon on Windows, independent of which characters
// Request.Validate rejects.
func ValidateResolvedPath(path string) error { return checkWindowsImage(path) }
