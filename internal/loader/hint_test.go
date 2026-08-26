package loader

import (
	"runtime"
	"strings"
	"testing"
)

func TestNewerGoHint(t *testing.T) {
	msg := "package example.com/x: /tmp/x/a.go:1:1: package requires newer Go version go1.99 (application built with go1.25)"
	hint := newerGoHint(msg)
	if hint == "" {
		t.Fatal("no hint for a requires-newer-Go error")
	}
	if !strings.Contains(hint, "GOTOOLCHAIN=go1.99.0 go install github.com/softmapio/softmap/cmd/softmap@latest") {
		t.Errorf("hint lacks the reinstall command for the required version:\n%s", hint)
	}
	if !strings.Contains(hint, runtime.Version()) {
		t.Errorf("hint does not name the toolchain this binary was built with:\n%s", hint)
	}
}

func TestNewerGoHintUnrelatedError(t *testing.T) {
	if hint := newerGoHint("package example.com/x: a.go:5:2: undefined: fmt.Printlnn"); hint != "" {
		t.Errorf("hint produced for an unrelated load error: %q", hint)
	}
}
