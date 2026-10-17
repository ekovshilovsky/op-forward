package executor

import (
	"strings"
	"testing"
)

// ---------- Validate boundaries ----------

func TestValidate_TooManyArguments(t *testing.T) {
	args := make([]string, MaxArgs+1)
	args[0] = "item"
	for i := 1; i < len(args); i++ {
		args[i] = "x"
	}

	err := (&Request{Args: args}).Validate()
	if err == nil || !strings.Contains(err.Error(), "too many arguments: 65 (max 64)") {
		t.Fatalf("Validate() error = %v, want the 65-argument rejection", err)
	}
}

func TestValidate_ArgumentCountBoundaryIsInclusive(t *testing.T) {
	args := make([]string, MaxArgs)
	args[0] = "item"
	for i := 1; i < len(args); i++ {
		args[i] = "x"
	}

	if err := (&Request{Args: args}).Validate(); err != nil {
		t.Fatalf("Validate() rejected exactly %d arguments: %v", MaxArgs, err)
	}
}

func TestValidate_OversizedArgument(t *testing.T) {
	req := &Request{Args: []string{"item", "get", strings.Repeat("a", MaxArgLength+1)}}

	err := req.Validate()
	if err == nil || !strings.Contains(err.Error(), "argument too long: 4097 bytes (max 4096)") {
		t.Fatalf("Validate() error = %v, want the 4097-byte rejection", err)
	}
}

func TestValidate_ArgumentLengthBoundaryIsInclusive(t *testing.T) {
	req := &Request{Args: []string{"item", "get", strings.Repeat("a", MaxArgLength)}}

	if err := req.Validate(); err != nil {
		t.Fatalf("Validate() rejected a %d-byte argument: %v", MaxArgLength, err)
	}
}

// ---------- Execute without a usable op ----------

func TestExecute_OpNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	res, err := Execute(&Request{Args: []string{"whoami"}})
	if err == nil || !strings.Contains(err.Error(), "op CLI not found in PATH") {
		t.Fatalf("Execute() error = %v, want the op-not-found error", err)
	}
	if res != nil {
		t.Errorf("Execute() result = %+v, want nil alongside the error", res)
	}
}

func TestExecute_InvalidRequestReturnsResultNotError(t *testing.T) {
	// PATH deliberately holds no op: validation must reject the request
	// before the binary lookup is attempted.
	t.Setenv("PATH", t.TempDir())

	res, err := Execute(&Request{Args: []string{"signin"}})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if res.ExitCode != 1 || !strings.Contains(res.Stderr, "not allowed") {
		t.Fatalf("Execute() = %+v, want exit 1 with a 'not allowed' message", res)
	}
}
