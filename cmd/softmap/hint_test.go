package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/softmapio/softmap/internal/entrypoints"
	"github.com/softmapio/softmap/internal/loader"
)

func loadToyshop(t *testing.T) *loader.Program {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "toyshop"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := loader.Load(dir)
	if err != nil {
		t.Fatalf("Load(toyshop): %v", err)
	}
	return p
}

// TestNoEntrypointsHint pins what a user sees when discovery finds nothing:
// the recognized frameworks, the flag, and names that exist in their own repo
// and actually resolve — the hint tells them to paste one, so a name that
// does not resolve would be a broken promise.
func TestNoEntrypointsHint(t *testing.T) {
	p := loadToyshop(t)

	names := exampleHandlerNames(p)
	if len(names) == 0 {
		t.Fatal("no example handlers found in a fixture full of handlers")
	}
	for _, name := range names {
		if _, err := entrypoints.Resolve(p, nil, "func:"+name); err != nil {
			t.Errorf("example %q does not resolve: %v", name, err)
		}
		short := name[strings.LastIndex(name, ".")+1:]
		if boilerplateMethod[short] {
			t.Errorf("example %q is an interface method, not a handler", name)
		}
	}

	hint := noEntrypointsHint(p, ".")
	want := append([]string{entrypoints.Frameworks, "--entrypoint", "issues/new/choose"}, names...)
	for _, w := range want {
		if !strings.Contains(hint, w) {
			t.Errorf("hint missing %q:\n%s", w, hint)
		}
	}
}

// TestExampleHandlerNamesStable: the hint must not change between runs, so
// the pick cannot depend on Go's map iteration order.
func TestExampleHandlerNamesStable(t *testing.T) {
	p := loadToyshop(t)
	first := strings.Join(exampleHandlerNames(p), "|")
	for i := 0; i < 5; i++ {
		if got := strings.Join(exampleHandlerNames(p), "|"); got != first {
			t.Fatalf("examples changed between runs: %q then %q", first, got)
		}
	}
}
