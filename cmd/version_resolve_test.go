package cmd

import (
	"runtime/debug"
	"testing"
)

func buildInfoWithVersion(v string) *debug.BuildInfo {
	return &debug.BuildInfo{Main: debug.Module{Version: v}}
}

func TestResolveVersion(t *testing.T) {
	cases := []struct {
		name    string
		current string
		info    *debug.BuildInfo
		ok      bool
		want    string
	}{
		{"ldflags value wins over module version", "1.2.3", buildInfoWithVersion("v9.9.9"), true, "1.2.3"},
		{"module version replaces the default", "dev", buildInfoWithVersion("v1.0.0"), true, "1.0.0"},
		{"source-tree build keeps the default", "dev", buildInfoWithVersion("(devel)"), true, "dev"},
		{"empty module version keeps the default", "dev", buildInfoWithVersion(""), true, "dev"},
		{"missing build info keeps the default", "dev", nil, false, "dev"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveVersion(c.current, c.info, c.ok); got != c.want {
				t.Errorf("resolveVersion(%q, ...) = %q, want %q", c.current, got, c.want)
			}
		})
	}
}
