package graph

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/softmapio/softmap/internal/loader"
	"github.com/softmapio/softmap/internal/ssax"
)

// TestCHAFallbackForDIWiredInterfaces: worker.Run calls Active.Sync where
// Active is only populated by a (simulated) DI container. VTA resolves the
// call to nothing; the per-site CHA fallback must find the unique
// implementation and keep the flow — including its SQL effect — alive.
func TestCHAFallbackForDIWiredInterfaces(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "toyshop"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := loader.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var run *ssa.Function
	for fn := range ssautil.AllFunctions(p.Prog) {
		if p.InModule(fn) && ssax.FuncDisplayName(fn) == "example.com/toyshop/worker.Run" {
			run = fn
			break
		}
	}
	if run == nil {
		t.Fatal("worker.Run not found")
	}

	res, err := Build(p, []*ssa.Function{run}, Options{Algo: "auto"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if res.Algo != "vta" {
		t.Fatalf("expected VTA, got %s (%s)", res.Algo, res.Warn)
	}

	f, err := Extract(p, res.Graph, res.CHA, run, Limits{})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	var syncNode *Node
	for _, n := range f.Order {
		if strings.Contains(n.Name, "DBSyncer).Sync") {
			syncNode = n
		}
	}
	if syncNode == nil {
		t.Fatalf("DBSyncer.Sync missing: CHA fallback did not resolve the DI-wired interface; nodes: %v", nodeNames(f))
	}
	if syncNode.Resolution != "static" {
		t.Errorf("Sync resolution = %q, want static (unique implementation)", syncNode.Resolution)
	}
	// The driver call is absorbed: Sync itself must carry the SQL effect.
	foundSQL := false
	for _, use := range syncNode.Effects {
		if use.Type == "sql" && strings.Contains(use.Detail, "REFRESH MATERIALIZED VIEW") {
			foundSQL = true
		}
	}
	if !foundSQL {
		t.Errorf("Sync did not absorb its SQL effect; effects: %+v", syncNode.Effects)
	}

	// Without the fallback the flow must degrade to a dynamic terminal —
	// pin that so the fallback is provably doing the work.
	bare, err := Extract(p, res.Graph, nil, run, Limits{})
	if err != nil {
		t.Fatalf("Extract(no fallback): %v", err)
	}
	foundDynamic := false
	for _, n := range bare.Order {
		if n.Resolution == "dynamic" && strings.Contains(n.Name, "Syncer.Sync") {
			foundDynamic = true
		}
	}
	if !foundDynamic {
		t.Errorf("expected a dynamic terminal without fallback; nodes: %v", nodeNames(bare))
	}
}

func nodeNames(f *Flow) []string {
	var out []string
	for _, n := range f.Order {
		out = append(out, n.Name)
	}
	return out
}
