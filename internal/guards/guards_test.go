package guards

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/softmapio/softmap/internal/loader"
	"github.com/softmapio/softmap/internal/ssax"
)

var fixture *loader.Program

func loadFixture(t *testing.T) *loader.Program {
	t.Helper()
	if fixture != nil {
		return fixture
	}
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "toyshop"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := loader.Load(dir)
	if err != nil {
		t.Fatalf("Load(toyshop): %v", err)
	}
	fixture = p
	return p
}

func fnByName(t *testing.T, p *loader.Program, suffix string) *ssa.Function {
	t.Helper()
	for fn := range ssautil.AllFunctions(p.Prog) {
		if p.InModule(fn) && fn.Blocks != nil && strings.HasSuffix(ssax.FuncDisplayName(fn), suffix) {
			return fn
		}
	}
	t.Fatalf("function %q not found", suffix)
	return nil
}

// TestAnalyzeApproveOrder pins the milestone's core contract on the fixture:
// exactly the semantic guards become decisions, every mechanical propagation
// is classified mechanical, the non-ASCII message survives byte-exact, and
// provenance points at the fetch call.
func TestAnalyzeApproveOrder(t *testing.T) {
	p := loadFixture(t)
	fn := fnByName(t, p, "Service).ApproveOrder")
	gs := Analyze(p, fn)

	var semantic, mechanical, gates []Guard
	for _, g := range gs {
		switch {
		case g.Mechanical:
			mechanical = append(mechanical, g)
		case g.Gate:
			gates = append(gates, g)
		default:
			semantic = append(semantic, g)
		}
	}
	if len(semantic) != 4 {
		t.Fatalf("semantic guards = %d, want 4: %+v", len(semantic), gs)
	}
	if len(gates) != 1 || gates[0].CondText != "o.Qty > 10" {
		t.Fatalf("gates = %+v, want one candidate with condition \"o.Qty > 10\"", gates)
	}
	if len(mechanical) != 3 {
		t.Fatalf("mechanical guards = %d, want 3 (bare, %%v wrap, %%w wrap): %+v", len(mechanical), gs)
	}

	byCond := map[string]Guard{}
	for _, g := range semantic {
		byCond[g.CondText] = g
	}

	gw, ok := byCond["err != nil && !errors.Is(err, ErrNoResellersAttached)"]
	if !ok {
		t.Fatalf("wordless-wrap guard missing; got %v", keys(byCond))
	}
	if gw.Exit.Kind != "sentinel" || gw.Exit.Name != "ErrApprovalStopped" {
		t.Errorf("wordless %%w wrap exit = %+v, want sentinel ErrApprovalStopped", gw.Exit)
	}

	g1, ok := byCond["len(resellers) == 0"]
	if !ok {
		t.Fatalf("sentinel guard condition missing; got %v", keys(byCond))
	}
	if g1.Exit.Kind != "sentinel" || g1.Exit.Name != "ErrNoResellersAttached" {
		t.Errorf("guard1 exit = %+v, want sentinel ErrNoResellersAttached", g1.Exit)
	}
	if len(g1.Uses) == 0 || g1.Uses[0] != "fetchResellers" {
		t.Errorf("guard1 uses = %v, want [fetchResellers]", g1.Uses)
	}

	g2, ok := byCond[`resellers[orderID] == ""`]
	if !ok {
		t.Fatalf("message guard condition missing; got %v", keys(byCond))
	}
	if g2.Exit.Kind != "message" || g2.Exit.Message != "нет прав на заказ %s" {
		t.Errorf("guard2 exit = %+v, want byte-exact non-ASCII message", g2.Exit)
	}
	if len(g2.Uses) == 0 || g2.Uses[0] != "fetchResellers" {
		t.Errorf("guard2 uses = %v, want [fetchResellers]", g2.Uses)
	}

	g3, ok := byCond["o.Qty <= 0"]
	if !ok {
		t.Fatalf("gating guard condition missing; got %v", keys(byCond))
	}
	if g3.Exit.Kind != "message" || g3.Exit.Message != "approve of empty order %s" {
		t.Errorf("guard3 exit = %+v", g3.Exit)
	}

	// Mechanical guards must name the failing calls for the fallible badge.
	var failed []string
	for _, m := range mechanical {
		if m.FailedCall != nil {
			failed = append(failed, m.FailedCall.Name())
		}
	}
	for _, want := range []string{"FindOrder", "CacheOrder", "OrderCreated"} {
		found := false
		for _, f := range failed {
			if f == want {
				found = true
			}
		}
		if !found {
			t.Errorf("mechanical FailedCall %s missing; got %v", want, failed)
		}
	}
}

// TestAnalyzeNoGuardsInErrorlessFunc: functions that cannot return an error
// have no guards by definition (fetchResellers returns a map).
func TestAnalyzeNoGuardsInErrorlessFunc(t *testing.T) {
	p := loadFixture(t)
	if gs := Analyze(p, fnByName(t, p, "Service).fetchResellers")); len(gs) != 0 {
		t.Errorf("fetchResellers guards = %+v, want none", gs)
	}
}

func keys(m map[string]Guard) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
