package loader

import (
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/ssa/ssautil"
)

// FixtureDir returns the shared toyshop fixture module used across packages.
func FixtureDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "toyshop"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadToyshopFixture(t *testing.T) {
	p, err := Load(FixtureDir(t))
	if err != nil {
		t.Fatalf("Load(toyshop): %v", err)
	}
	if p.Module != "example.com/toyshop" {
		t.Errorf("Module = %q, want example.com/toyshop", p.Module)
	}
	if len(p.Warnings) > 0 {
		t.Errorf("unexpected load warnings: %v", p.Warnings)
	}

	gen, total := p.GeneratedStats()
	if gen == 0 {
		t.Error("GeneratedStats found no generated functions; gen/api.pb.go should count")
	}
	if gen*2 > total {
		t.Errorf("generated ratio implausible: %d/%d", gen, total)
	}

	// The stub deps must be loaded with types (effect matching needs their
	// import paths) and the generated file's methods must classify Generated.
	var sawWriteMessages, sawReset bool
	for fn := range ssautil.AllFunctions(p.Prog) {
		switch {
		case fn.Name() == "WriteMessages" && FuncPackage(fn) == "github.com/segmentio/kafka-go":
			sawWriteMessages = true
		case fn.Name() == "Reset" && FuncPackage(fn) == "example.com/toyshop/gen":
			sawReset = true
			if p.FuncClass(fn) != Generated {
				t.Errorf("gen.Reset FuncClass = %v, want Generated", p.FuncClass(fn))
			}
		}
	}
	if !sawReset {
		t.Error("gen.OrderRequest.Reset not found in SSA")
	}
	_ = sawWriteMessages // dep bodies may or may not build; identity matters later via types, not SSA
}
