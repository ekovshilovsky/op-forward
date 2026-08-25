package cmd

import (
	"strings"
	"testing"
)

func TestRenderPlistEscapesXML(t *testing.T) {
	plist := renderPlist("com.example", "/usr/local/bin/op-forward", "unix:///Users/me/R&D/op-forward.sock", "/Users/me", "/Users/me/Library/Logs/op-forward.log")
	if strings.Contains(plist, "R&D") {
		t.Fatal("raw ampersand written into plist")
	}
	if !strings.Contains(plist, "R&amp;D") {
		t.Fatal("ampersand not escaped as &amp;")
	}
	if !strings.Contains(plist, "<string>--listen</string>") {
		t.Fatal("plist does not pass --listen")
	}
}
