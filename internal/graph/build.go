// Package graph builds the whole-program call graph and extracts
// per-entrypoint flows into the mutable Flow IR shared by the filter
// pipeline and output layers.
package graph

import (
	"fmt"
	"time"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/softmapio/softmap/internal/loader"
)

// Algorithm trade-off: CHA is fast but over-approximates interface calls
// (every implementation of the method's signature becomes an edge). VTA
// propagates concrete types through a whole-program flow graph, giving far
// more precise interface resolution, at a cost that grows steeply with the
// size of the function set. Strategy: build CHA once (cheap, also serves as
// VTA's initial graph), restrict VTA to the CHA-reachable cone from the
// requested entrypoints, and fall back to plain CHA when the cone is huge or
// VTA exceeds its wall-clock budget. The Builder interface keeps algorithms
// swappable.
type Builder interface {
	Name() string
	// Build constructs a call graph for the functions in cone; initial is a
	// sound over-approximation some algorithms refine (may be nil).
	Build(prog *ssa.Program, cone map[*ssa.Function]bool, initial *callgraph.Graph) *callgraph.Graph
}

type chaBuilder struct{}

func (chaBuilder) Name() string { return "cha" }
func (chaBuilder) Build(prog *ssa.Program, _ map[*ssa.Function]bool, initial *callgraph.Graph) *callgraph.Graph {
	if initial != nil {
		return initial
	}
	return cha.CallGraph(prog)
}

type vtaBuilder struct{}

func (vtaBuilder) Name() string { return "vta" }
func (vtaBuilder) Build(_ *ssa.Program, cone map[*ssa.Function]bool, initial *callgraph.Graph) *callgraph.Graph {
	return vta.CallGraph(cone, initial)
}

// Options controls call-graph construction.
type Options struct {
	Algo       string // "auto" | "vta" | "cha"
	VTATimeout time.Duration
	// MaxVTAFuncs is the reachable-cone size beyond which auto mode falls
	// back to CHA.
	MaxVTAFuncs int
}

// Result carries the built graph plus honesty metadata for the CLI.
type Result struct {
	Graph *callgraph.Graph
	// CHA is the sound over-approximation, kept alongside a VTA Graph so
	// extraction can fall back per call site when VTA resolves an interface
	// call to nothing (typical with reflection-based dependency injection,
	// where VTA never sees the allocation flow into an interface field).
	CHA   *callgraph.Graph
	Algo  string // algorithm actually used
	Funcs int    // size of the function set given to the algorithm
	Warn  string // non-empty when a fallback happened
}

const defaultMaxVTAFuncs = 20000

// Build constructs the call graph once for the whole program. roots are the
// entrypoints whose cones matter; with an empty roots slice the whole module
// is the cone.
func Build(p *loader.Program, roots []*ssa.Function, opts Options) (*Result, error) {
	if opts.MaxVTAFuncs == 0 {
		opts.MaxVTAFuncs = defaultMaxVTAFuncs
	}
	chaGraph := chaBuilder{}.Build(p.Prog, nil, nil)
	if opts.Algo == "cha" {
		return &Result{Graph: chaGraph, CHA: chaGraph, Algo: "cha", Funcs: len(chaGraph.Nodes)}, nil
	}

	// The cone must include the module's wiring code (main/init and
	// everything they reach), not just the entrypoints' callees: VTA can
	// only resolve an interface call if it sees the allocation sites whose
	// types flow into it, and those live in constructors called from main.
	coneRoots := append(append([]*ssa.Function(nil), roots...), moduleWiring(p)...)
	cone := reachable(p.Prog, chaGraph, coneRoots)
	res := &Result{Funcs: len(cone), CHA: chaGraph}
	if opts.Algo == "auto" && len(cone) > opts.MaxVTAFuncs {
		res.Graph, res.Algo = chaGraph, "cha"
		res.Warn = fmt.Sprintf("reachable set has %d functions (> %d); using CHA, interface edges may over-approximate", len(cone), opts.MaxVTAFuncs)
		return res, nil
	}
	if opts.Algo != "auto" && opts.Algo != "vta" {
		return nil, fmt.Errorf("unknown call-graph algorithm %q (want auto, vta, or cha)", opts.Algo)
	}

	done := make(chan *callgraph.Graph, 1)
	go func() {
		// VTA cannot be cancelled; on timeout this goroutine is abandoned
		// and the process exits with the CHA result soon after.
		done <- vtaBuilder{}.Build(p.Prog, cone, chaGraph)
	}()
	timeout := opts.VTATimeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	select {
	case g := <-done:
		res.Graph, res.Algo = g, "vta"
	case <-time.After(timeout):
		res.Graph, res.Algo = chaGraph, "cha"
		res.Warn = fmt.Sprintf("VTA exceeded %s; using CHA, interface edges may over-approximate", timeout)
	}
	return res, nil
}

// moduleWiring returns the module's main and package init functions.
func moduleWiring(p *loader.Program) []*ssa.Function {
	var out []*ssa.Function
	for _, pkg := range p.Pkgs {
		ssaPkg := p.Prog.Package(pkg.Types)
		if ssaPkg == nil {
			continue
		}
		for _, name := range []string{"init", "main"} {
			if fn := ssaPkg.Func(name); fn != nil {
				out = append(out, fn)
			}
		}
	}
	return out
}

// reachable computes the set of body-bearing functions reachable in g from
// roots (or all functions when roots is empty).
func reachable(prog *ssa.Program, g *callgraph.Graph, roots []*ssa.Function) map[*ssa.Function]bool {
	if len(roots) == 0 {
		all := map[*ssa.Function]bool{}
		for fn := range ssautil.AllFunctions(prog) {
			if fn.Blocks != nil {
				all[fn] = true
			}
		}
		return all
	}
	seen := map[*ssa.Function]bool{}
	var queue []*callgraph.Node
	for _, root := range roots {
		if n := g.Nodes[root]; n != nil && !seen[root] {
			seen[root] = true
			queue = append(queue, n)
		}
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, e := range n.Out {
			callee := e.Callee
			if callee.Func == nil || seen[callee.Func] {
				continue
			}
			seen[callee.Func] = true
			if callee.Func.Blocks != nil {
				queue = append(queue, callee)
			}
		}
	}
	// Only body-bearing functions matter to VTA.
	for fn := range seen {
		if fn.Blocks == nil {
			delete(seen, fn)
		}
	}
	return seen
}
