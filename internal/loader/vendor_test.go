package loader

import (
	"strings"
	"testing"
)

// TestInconsistentVendorFallsBackToModuleCache: a vendor/modules.txt that
// disagrees with go.mod makes the go tool refuse to load. softmap retries
// with -mod=mod and surfaces a warning instead of failing.
func TestInconsistentVendorFallsBackToModuleCache(t *testing.T) {
	dir := writeModule(t, map[string]string{
		"go.mod":  "module example.com/stale\n\ngo 1.22\n",
		"main.go": "package main\n\nfunc main() {}\n",
		// Claims an explicit dep that go.mod does not require.
		"vendor/modules.txt": "# example.com/ghost v1.0.0\n## explicit; go 1.22\nexample.com/ghost\n",
		"vendor/example.com/ghost/ghost.go": "package ghost\n",
	})
	p, err := Load(dir)
	if err != nil {
		t.Fatalf("Load should fall back to -mod=mod, got: %v", err)
	}
	found := false
	for _, w := range p.Warnings {
		if strings.Contains(w, "vendor/ is out of sync") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a vendor-out-of-sync warning, got %v", p.Warnings)
	}
}
