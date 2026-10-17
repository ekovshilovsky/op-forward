//go:build !windows

package executor

// ValidateResolvedPath checks that the op binary resolved from PATH can be
// executed without a shell. On unix the program is executed directly by
// path (execve) and no interpreter re-parses its arguments, so there is
// nothing to refuse and the check always succeeds.
func ValidateResolvedPath(string) error { return nil }
