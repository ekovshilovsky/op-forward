package executor

import (
	"strings"
	"testing"
)

func TestValidate_EmptyArgs(t *testing.T) {
	req := &Request{Args: []string{}}
	if err := req.Validate(); err == nil {
		t.Error("Validate() should reject empty args")
	}
}

func TestValidate_BlockedSubcommands(t *testing.T) {
	blocked := []string{"signin", "signout", "update", "completion"}
	for _, cmd := range blocked {
		req := &Request{Args: []string{cmd}}
		err := req.Validate()
		if err == nil {
			t.Errorf("Validate() should block subcommand %q", cmd)
		}
		if err != nil && !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("Validate(%q) error = %q, should mention 'not allowed'", cmd, err)
		}
	}
}

func TestValidate_AllowedSubcommands(t *testing.T) {
	allowed := []string{
		"account", "item", "vault", "document", "whoami",
		"read", "inject", "run", "plugin", "connect",
	}
	for _, cmd := range allowed {
		req := &Request{Args: []string{cmd, "list"}}
		if err := req.Validate(); err != nil {
			t.Errorf("Validate() should allow subcommand %q, got: %v", cmd, err)
		}
	}
}

func TestValidate_ShellMetacharacters(t *testing.T) {
	tests := []struct {
		name string
		arg  string
	}{
		{"backtick", "test`whoami`"},
		{"dollar", "test$(id)"},
		{"pipe", "test|cat /etc/passwd"},
		{"semicolon", "test; rm -rf /"},
		{"ampersand", "test & echo pwned"},
		{"newline", "test\nmalicious"},
		{"carriage return", "test\rmalicious"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &Request{Args: []string{"item", "get", tt.arg}}
			err := req.Validate()
			if err == nil {
				t.Errorf("Validate() should reject arg with %s", tt.name)
			}
			if err != nil && !strings.Contains(err.Error(), "disallowed character") {
				t.Errorf("error = %q, should mention 'disallowed character'", err)
			}
		})
	}
}

func TestValidate_LegitimateArguments(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"simple get", []string{"item", "get", "abc123"}},
		{"with fields", []string{"item", "get", "abc123", "--fields", "username"}},
		{"with reveal", []string{"item", "get", "abc123", "--fields", "password", "--reveal"}},
		{"uuid format", []string{"item", "get", "puag4io26exg6xdv2lutur2k7q"}},
		{"vault list", []string{"vault", "list"}},
		{"account list", []string{"account", "list"}},
		{"with format", []string{"item", "list", "--format", "json"}},
		{"with dashes", []string{"item", "get", "my-item-name"}},
		{"with dots", []string{"item", "get", "my.item.name"}},
		{"with slashes", []string{"read", "op://vault/item/field"}},
		{"with equals", []string{"item", "get", "--vault=Personal"}},
		{"with spaces", []string{"item", "get", "My Item Name"}},
		{"with at sign", []string{"item", "get", "user@example.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &Request{Args: tt.args}
			if err := req.Validate(); err != nil {
				t.Errorf("Validate() rejected legitimate args %v: %v", tt.args, err)
			}
		})
	}
}

func TestValidate_TimeoutClamping(t *testing.T) {
	// Timeout values are handled in Execute(), not Validate(),
	// but we verify the request passes validation regardless of timeout
	req := &Request{
		Args:      []string{"account", "list"},
		TimeoutMs: 999999,
	}
	if err := req.Validate(); err != nil {
		t.Errorf("Validate() should not reject based on timeout: %v", err)
	}
}

func TestValidate_SingleArg(t *testing.T) {
	req := &Request{Args: []string{"whoami"}}
	if err := req.Validate(); err != nil {
		t.Errorf("Validate() should allow single-arg commands: %v", err)
	}
}
