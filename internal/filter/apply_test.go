package filter

import (
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/softmapio/softmap/internal/effects"
	"github.com/softmapio/softmap/internal/graph"
	"github.com/softmapio/softmap/internal/loader"
)

// flowBuilder assembles synthetic flows for pipeline tests; rules here match
// by package so no SSA is needed.
type flowBuilder struct {
	f *graph.Flow
}

func newFlow() *flowBuilder {
	root := &graph.Node{Name: "root", Pkg: "m/handlers", Kind: "step"}
	return &flowBuilder{f: &graph.Flow{Root: root, Nodes: map[string]*graph.Node{"root": root}}}
}

func (b *flowBuilder) add(name, pkg string) *graph.Node {
	n := &graph.Node{Name: name, Pkg: pkg, Kind: "step", Resolution: "static"}
	b.f.Nodes[name] = n
	b.f.Order = append(b.f.Order, n)
	return n
}

func (b *flowBuilder) effect(name string) *graph.Node {
	n := b.add(name, "m/repo")
	n.Effects = []graph.EffectUse{{Effect: &effects.Effect{Type: "sql", Detail: "SELECT 1"}}}
	n.Kept = true
	return n
}

func edge(from, to *graph.Node) {
	from.Out = append(from.Out, to)
	to.In = append(to.In, from)
}

func dropRule(id, pkg string) Rule {
	return Rule{ID: id, Action: "drop", Match: Match{Pkg: StringList{pkg}}}
}

func collapseRule(id, pkg string) Rule {
	return Rule{ID: id, Action: "collapse", Match: Match{Pkg: StringList{pkg}}}
}

func surviving(f *graph.Flow) []string {
	var out []string
	for _, n := range f.Order {
		out = append(out, n.Name)
	}
	sort.Strings(out)
	return out
}

// p is a minimal loader.Program: rule matching only needs Module.
var testProg = &loader.Program{Module: "m"}

func TestDropRemovesSubtree(t *testing.T) {
	b := newFlow()
	logger := b.add("log.Info", "m/pkg/log")
	helper := b.add("log.format", "m/pkg/log")
	step := b.add("svc.Do", "m/svc")
	edge(b.f.Root, logger)
	edge(logger, helper)
	edge(b.f.Root, step)

	Mark(b.f, []Rule{dropRule("drop-log", "m/pkg/log")}, testProg)
	counts := Prune(b.f, "m")

	if got := surviving(b.f); !reflect.DeepEqual(got, []string{"svc.Do"}) {
		t.Errorf("surviving = %v, want [svc.Do]", got)
	}
	if counts["drop-log"] != 2 {
		t.Errorf("dropped count = %d, want 2", counts["drop-log"])
	}
}

// TestKeepProtection: a drop-marked node with an effect beneath must stay so
// the path to the effect is never broken.
func TestKeepProtection(t *testing.T) {
	b := newFlow()
	mid := b.add("helper.Do", "m/helper")
	eff := b.effect("db.Query")
	edge(b.f.Root, mid)
	edge(mid, eff)

	Mark(b.f, []Rule{dropRule("drop-helper", "m/helper")}, testProg)
	counts := Prune(b.f, "m")

	want := []string{"db.Query", "helper.Do"}
	if got := surviving(b.f); !reflect.DeepEqual(got, want) {
		t.Errorf("surviving = %v, want %v", got, want)
	}
	if counts["drop-helper"] != 0 {
		t.Errorf("protected node counted as dropped: %v", counts)
	}
}

// TestEffectImmunity: rules can never drop an effect node, even one whose
// package a drop rule matches.
func TestEffectImmunity(t *testing.T) {
	b := newFlow()
	eff := b.effect("db.Query") // pkg lib/db
	edge(b.f.Root, eff)

	Mark(b.f, []Rule{dropRule("drop-all", "*")}, testProg)
	Prune(b.f, "m")

	if got := surviving(b.f); !reflect.DeepEqual(got, []string{"db.Query"}) {
		t.Errorf("surviving = %v, want [db.Query]", got)
	}
}

// TestCollapseChain: root→w1→w2→leaf with both wrappers collapsed becomes
// root→leaf with both names recorded on root.
func TestCollapseChain(t *testing.T) {
	b := newFlow()
	w1 := b.add("wrap.One", "m/wrap")
	w2 := b.add("wrap.Two", "m/wrap")
	leaf := b.effect("db.Exec")
	edge(b.f.Root, w1)
	edge(w1, w2)
	edge(w2, leaf)

	Mark(b.f, []Rule{collapseRule("collapse-wrap", "m/wrap")}, testProg)
	counts := Prune(b.f, "m")

	if got := surviving(b.f); !reflect.DeepEqual(got, []string{"db.Exec"}) {
		t.Errorf("surviving = %v, want [db.Exec]", got)
	}
	wantCollapsed := []string{"wrap.One", "wrap.Two"}
	got := append([]string(nil), b.f.Root.Collapsed...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, wantCollapsed) {
		t.Errorf("root.Collapsed = %v, want %v", got, wantCollapsed)
	}
	if len(b.f.Root.Out) != 1 || b.f.Root.Out[0] != leaf {
		t.Errorf("root edges not spliced to leaf: %v", b.f.Root.Out)
	}
	if counts["collapse-wrap"] != 2 {
		t.Errorf("collapse count = %d, want 2", counts["collapse-wrap"])
	}
}

// TestCollapseDiamond: a collapsed node with two surviving parents records
// its name on both and re-parents its child to both.
func TestCollapseDiamond(t *testing.T) {
	b := newFlow()
	left := b.add("svc.Left", "m/svc")
	right := b.add("svc.Right", "m/svc")
	wrap := b.add("wrap.Mid", "m/wrap")
	leaf := b.effect("db.Exec")
	edge(b.f.Root, left)
	edge(b.f.Root, right)
	edge(left, wrap)
	edge(right, wrap)
	edge(wrap, leaf)

	Mark(b.f, []Rule{collapseRule("collapse-wrap", "m/wrap")}, testProg)
	Prune(b.f, "m")

	for _, parent := range []*graph.Node{left, right} {
		if !reflect.DeepEqual(parent.Collapsed, []string{"wrap.Mid"}) {
			t.Errorf("%s.Collapsed = %v, want [wrap.Mid]", parent.Name, parent.Collapsed)
		}
		if len(parent.Out) != 1 || parent.Out[0] != leaf {
			t.Errorf("%s not re-parented to leaf", parent.Name)
		}
	}
}

// TestAsyncPropagatesThroughCollapse: go wrapper() calling an effect keeps
// the async tag on the surviving effect node.
func TestAsyncPropagatesThroughCollapse(t *testing.T) {
	b := newFlow()
	wrap := b.add("wrap.Async", "m/wrap")
	wrap.Async = true
	leaf := b.effect("db.Exec")
	edge(b.f.Root, wrap)
	edge(wrap, leaf)

	Mark(b.f, []Rule{collapseRule("collapse-wrap", "m/wrap")}, testProg)
	Prune(b.f, "m")

	if !leaf.Async {
		t.Error("async tag lost when the async wrapper collapsed")
	}
}

// TestCycleSafety: recursion between two dropped nodes must not hang or
// resurrect anything.
func TestCycleSafety(t *testing.T) {
	b := newFlow()
	a := b.add("noise.A", "m/noise")
	c := b.add("noise.B", "m/noise")
	edge(b.f.Root, a)
	edge(a, c)
	edge(c, a) // cycle

	Mark(b.f, []Rule{dropRule("drop-noise", "m/noise")}, testProg)
	Prune(b.f, "m")

	if got := surviving(b.f); len(got) != 0 {
		t.Errorf("surviving = %v, want none", got)
	}
}

// TestEffectFreeSubtreeDrop: deep subtrees with no effect beneath them are
// implementation detail; the top narrativeDepth levels stay regardless.
func TestEffectFreeSubtreeDrop(t *testing.T) {
	b := newFlow()
	var prev *graph.Node = b.f.Root
	var chain []*graph.Node
	for i, name := range []string{"a", "b", "c", "d"} {
		n := b.add("svc."+name, "m/svc")
		n.Depth = i + 1
		edge(prev, n)
		chain = append(chain, n)
		prev = n
	}

	Mark(b.f, nil, testProg)
	Prune(b.f, "m")

	want := []string{"svc.a", "svc.b"} // depth 1 and 2 survive
	if got := surviving(b.f); !reflect.DeepEqual(got, want) {
		t.Errorf("surviving = %v, want %v", got, want)
	}
	if chain[2].DroppedBy != EffectFreeRuleID {
		t.Errorf("svc.c DroppedBy = %q, want %s", chain[2].DroppedBy, EffectFreeRuleID)
	}
}

// TestFitToBudget: a flow larger than the readability budget zooms out -
// steps beyond the fitting depth collapse and their effects bubble into the
// surviving ancestors (deduplicated), so what the subtree does stays
// visible.
func TestFitToBudget(t *testing.T) {
	b := newFlow()
	eff := b.effect("db.Query")
	eff.Depth = 3
	var top []*graph.Node
	for i := 0; i < 5; i++ {
		n := b.add(fmt.Sprintf("svc.Top%02d", i), "m/svc")
		n.Depth = 1
		edge(b.f.Root, n)
		top = append(top, n)
	}
	for i := 0; i < 45; i++ {
		n := b.add(fmt.Sprintf("svc.Deep%02d", i), "m/svc")
		n.Depth = 2
		edge(top[i%5], n)
		edge(n, eff) // anchored: the effect-free pass must not touch them
	}

	Mark(b.f, nil, testProg)
	counts := Prune(b.f, "m")

	if counts[BeyondNarrativeRuleID] != 46 { // 45 deep steps + the effect carrier
		t.Errorf("beyond-narrative collapses = %d, want 46", counts[BeyondNarrativeRuleID])
	}
	got := surviving(b.f)
	if len(got) != 5 { // the 5 top steps, each carrying the bubbled effect
		t.Errorf("surviving = %d nodes (%v), want 5", len(got), got)
	}
	_ = eff
	for _, n := range top {
		sqlSeen := false
		for _, use := range n.Effects {
			if use.Type == "sql" && use.Detail == "SELECT 1" {
				sqlSeen = true
			}
		}
		if !sqlSeen {
			t.Errorf("%s did not inherit the bubbled sql effect: %+v", n.Name, n.Effects)
		}
		if len(n.Collapsed) != 0 {
			t.Errorf("%s.Collapsed = %v; beyond-narrative must not record names", n.Name, n.Collapsed)
		}
	}
}

// TestTerminalDoesNotProtect: a dropped subtree ending in a dynamic terminal
// stays dropped - unresolved calls show uncertainty, not liveness.
func TestTerminalDoesNotProtect(t *testing.T) {
	b := newFlow()
	logNode := b.add("log.Error", "m/pkg/log")
	term := b.add("dynamic:log.Writer.Write", "m/pkg/log")
	term.Kind = "terminal"
	term.Resolution = "dynamic"
	term.Kept = true
	edge(b.f.Root, logNode)
	edge(logNode, term)

	Mark(b.f, []Rule{dropRule("drop-log", "m/pkg/log")}, testProg)
	Prune(b.f, "m")

	if got := surviving(b.f); len(got) != 0 {
		t.Errorf("surviving = %v, want none (terminal must not resurrect the log subtree)", got)
	}
}

func TestFirstMatchingRuleWins(t *testing.T) {
	b := newFlow()
	n := b.add("noise.A", "m/noise")
	edge(b.f.Root, n)

	Mark(b.f, []Rule{dropRule("first", "m/noise"), dropRule("second", "m/*")}, testProg)
	counts := Prune(b.f, "m")

	if counts["first"] != 1 || counts["second"] != 0 {
		t.Errorf("counts = %v, want first=1 second=0", counts)
	}
}
