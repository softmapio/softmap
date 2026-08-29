package filter

import (
	"path"
	"strings"

	"github.com/softmapio/softmap/internal/graph"
	"github.com/softmapio/softmap/internal/loader"
	"github.com/softmapio/softmap/internal/ssax"
)

// Mark runs the rule pass over a raw flow, writing decisions onto the nodes
// without removing anything (so --debug-tree can show every decision), then
// applies keep-protection. Pass order:
//
//  1. Effect immunity - effect nodes and dynamic terminals were born with
//     Kept=true at extraction; rules never touch them. This engine-level
//     guarantee is what makes broad rules ("drop all stdlib") safe.
//  2. Rules in file order; first match marks the node dropped or collapsed.
//  3. The narrative pass: nodes deeper than narrativeDepth whose subtree
//     contains no anchor (effect or terminal) are implementation detail, not
//     flow - marked with the engine:no-effect-subtree pseudo-rule. This is
//     the single biggest lever on real codebases, where an entrypoint can
//     transitively reach a thousand pure-computation helpers.
//  4. Keep-protection, bottom-up: a drop-marked node with a surviving
//     descendant is un-dropped - paths to effects never break. Collapse
//     marks stay: collapsing preserves connectivity by construction.
func Mark(f *graph.Flow, rules []Rule, p *loader.Program) {
	f.Root.Kept = true
	for _, n := range f.Order {
		if n.Kept || n.DroppedBy != "" { // immune, or pre-marked excluded:*
			continue
		}
		for _, r := range rules {
			if ruleMatches(&r, n, p) {
				n.DroppedBy = r.ID
				n.Collapse = r.Action == "collapse"
				break
			}
		}
	}
	markEffectFreeSubtrees(f)
	protect(f)
	fitToBudget(f)
}

// narrativeDepth is how many levels below the entrypoint stay in the map
// even without effects beneath them: the top-level story of the flow.
const narrativeDepth = 2

// EffectFreeRuleID is the pseudo-rule id recorded on nodes dropped for
// having no effect (or unresolved dynamic call) anywhere beneath them.
const EffectFreeRuleID = "engine:no-effect-subtree"

// Only effects anchor a subtree. Dynamic terminals stay when their parent
// stays (uncertainty on a real path is worth showing), but they must not
// rescue ancestors: a logging subsystem ending in an unresolved writer
// interface is still noise.
func markEffectFreeSubtrees(f *graph.Flow) {
	anchored := anchoredNodes(f)
	for _, n := range f.Order {
		if n.Kept || n.DroppedBy != "" || anchored[n] || n.Depth <= narrativeDepth {
			continue
		}
		if n.Kind == "terminal" {
			continue // fate follows the parent; orphans vanish at prune
		}
		n.DroppedBy = EffectFreeRuleID
	}
}

// anchoredNodes reports which nodes have an effect somewhere strictly below
// them (cycle-safe post-order).
func anchoredNodes(f *graph.Flow) map[*graph.Node]bool {
	anchored := map[*graph.Node]bool{}
	state := map[*graph.Node]int{} // 0 unvisited, 1 in progress, 2 done
	var visit func(n *graph.Node) bool
	visit = func(n *graph.Node) bool {
		switch state[n] {
		case 1:
			return false // back-edge
		case 2:
			return len(n.Effects) > 0 || anchored[n]
		}
		state[n] = 1
		below := false
		for _, child := range n.Out {
			if visit(child) {
				below = true
			}
		}
		state[n] = 2
		anchored[n] = below
		return len(n.Effects) > 0 || below
	}
	visit(f.Root)
	return anchored
}

// BeyondNarrativeRuleID marks nodes collapsed by the fit-to-budget pass.
const BeyondNarrativeRuleID = "engine:beyond-narrative"

// maxNarrativeNodes is the readability budget: the point of a flow map is
// to be read top to bottom by a human, which caps out around this size.
const maxNarrativeNodes = 40

// fitToBudget zooms the flow out until it fits the readability budget: it
// finds the largest depth D whose surviving steps (plus all effect nodes,
// which splice upward through collapses and deduplicate by identity) fit,
// and collapses every surviving step deeper than D. The full detail remains
// visible in --debug-tree; the JSON keeps the narrative plus everything the
// flow ultimately does.
func fitToBudget(f *graph.Flow) {
	maxDepth := 0
	stepsAt := map[int]int{}
	for _, n := range f.Order {
		if n.DroppedBy != "" {
			continue
		}
		stepsAt[n.Depth]++
		if n.Depth > maxDepth {
			maxDepth = n.Depth
		}
	}
	total := 0
	for _, c := range stepsAt {
		total += c
	}
	if total <= maxNarrativeNodes {
		return
	}

	depth := 1
	cum := stepsAt[1]
	for d := 2; d <= maxDepth; d++ {
		if cum+stepsAt[d] > maxNarrativeNodes {
			break
		}
		cum += stepsAt[d]
		depth = d
	}

	for _, n := range f.Order {
		if n.DroppedBy != "" || n.Depth <= depth {
			continue
		}
		n.DroppedBy = BeyondNarrativeRuleID
		n.Collapse = true
	}
}

func ruleMatches(r *Rule, n *graph.Node, p *loader.Program) bool {
	if len(r.Match.Pkg) > 0 && !anyMatch(r.Match.Pkg, func(pat string) bool {
		return matchPkg(pat, n.Pkg, p.Module)
	}) {
		return false
	}
	if len(r.Match.Func) > 0 && !anyMatch(r.Match.Func, func(pat string) bool {
		return matchGlob(pat, funcBaseName(n))
	}) {
		return false
	}
	if h := r.Match.Heuristic; h != "" {
		if !predicates[h](n, p) {
			return false
		}
	}
	return true
}

func anyMatch(patterns []string, match func(string) bool) bool {
	for _, pat := range patterns {
		if match(pat) {
			return true
		}
	}
	return false
}

func funcBaseName(n *graph.Node) string {
	if n.Fn != nil {
		return n.Fn.Name()
	}
	if i := strings.LastIndex(n.Name, "."); i >= 0 {
		return n.Name[i+1:]
	}
	return n.Name
}

// protect un-drops any drop-marked node that still leads to surviving work.
// A node "survives at base" when it is immune (effects, terminals, root) or
// no rule marked it. One bottom-up pass suffices: protection looks for any
// base-surviving descendant, which is transitive by construction.
func protect(f *graph.Flow) {
	const (
		white = iota // unvisited
		grey         // on stack (cycle guard)
		black        // done
	)
	color := map[*graph.Node]int{}
	memo := map[*graph.Node]bool{} // has a base-surviving strict descendant

	// Terminals never protect ancestors (Kind check): an unresolved dynamic
	// call is shown when its context survives, but proves nothing alive.
	baseKept := func(n *graph.Node) bool {
		return len(n.Effects) > 0 || (n.DroppedBy == "" && n.Kind != "terminal")
	}

	var visit func(n *graph.Node) bool // returns baseKept(n) || memo[n]
	visit = func(n *graph.Node) bool {
		switch color[n] {
		case grey:
			return false // back-edge: a cycle cannot prove liveness by itself
		case black:
			return baseKept(n) || memo[n]
		}
		color[n] = grey
		below := false
		for _, child := range n.Out {
			if visit(child) {
				below = true
			}
		}
		color[n] = black
		memo[n] = below
		return baseKept(n) || below
	}
	visit(f.Root)

	for _, n := range f.Order {
		if n.DroppedBy != "" && !n.Collapse && memo[n] {
			n.DroppedBy = "" // protected: this node is on a path to kept work
		}
	}
}

// Prune executes the marks: collapse-marked nodes are inlined into their
// callers (children re-parented, name recorded in Collapsed), drop-marked
// nodes are removed with their subtrees. Returns removed-node counts per
// rule id - the honesty stats for dropped_by_rule.
func Prune(f *graph.Flow, module string) map[string]int {
	counts := map[string]int{}

	// Splice collapses first, in BFS order so chains of wrappers accumulate
	// into the nearest surviving ancestor (A→w1→w2→B becomes A→B with
	// A.collapsed=[w1,w2]).
	for _, n := range f.Order {
		if n.DroppedBy == "" || !n.Collapse {
			continue
		}
		// Beyond-narrative collapses can remove hundreds of nodes; recording
		// every name would bloat the surviving nodes. The debug tree still
		// shows each one, and dropped_by_rule carries the count.
		recordName := n.DroppedBy != BeyondNarrativeRuleID
		short := collapsedName(n, module)
		for _, parent := range append([]*graph.Node(nil), n.In...) {
			if parent.DroppedBy != "" && !parent.Collapse {
				continue // parent is being dropped anyway
			}
			if recordName {
				parent.Collapsed = append(parent.Collapsed, short)
				// Inherit names already inlined into n, so no record is
				// lost when collapse chains are processed out of order.
				parent.Collapsed = append(parent.Collapsed, n.Collapsed...)
			}
			// Effects always bubble to the survivor, whatever the reason
			// for the collapse.
			parent.MergeEffectsFrom(n)
			for _, child := range n.Out {
				if child != parent {
					// The spliced hop is async if any hop it replaces was;
					// it keeps the collapsed call's source position.
					async := f.EdgeAsync(parent, n) || f.EdgeAsync(n, child) || n.Async
					spliceEdge(f, parent, child, async, f.EdgeSeq(parent, n))
					// A decision's pass/fail/yes-no metadata survives the
					// splice: the collapsed hop was plumbing, the edge's
					// meaning was not.
					if k := f.EdgeKind(parent, n); k != "" {
						f.SetEdgeKind(parent, child, k, f.EdgeLabel(parent, n))
					}
				}
			}
		}
		counts[n.DroppedBy]++
		detach(n)
	}

	for _, n := range f.Order {
		if n.DroppedBy != "" && !n.Collapse {
			counts[n.DroppedBy]++
			detach(n)
		}
	}

	// Rebuild Order (and the node index) from what is still reachable.
	var order []*graph.Node
	seen := map[*graph.Node]bool{f.Root: true}
	queue := []*graph.Node{f.Root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, child := range cur.Out {
			if !seen[child] {
				seen[child] = true
				order = append(order, child)
				queue = append(queue, child)
			}
		}
	}
	f.Order = order
	for key, n := range f.Nodes {
		if !seen[n] {
			delete(f.Nodes, key)
		}
	}
	// Splicing can orphan a gate decision: its gated call collapsed into
	// nothing, leaving a condition card that points at no work. Drop those
	// rather than render "yes → steps below run" above an empty column.
	orphaned := false
	for _, n := range f.Order {
		if n.Kind == "decision" && n.Decision != nil && n.Decision.Gate &&
			len(n.Out) == 0 && len(n.Effects) == 0 {
			detach(n)
			delete(f.Nodes, n.Name)
			counts["engine:empty-gate"]++
			orphaned = true
		}
	}
	if orphaned {
		kept := f.Order[:0]
		for _, n := range f.Order {
			if _, ok := f.Nodes[n.Name]; ok {
				kept = append(kept, n)
			}
		}
		f.Order = kept
	}
	return counts
}

// collapsedName renders a compact name for the collapsed list:
// "repo.Save" rather than "(*example.com/toyshop/repo.Repo).Save".
func collapsedName(n *graph.Node, module string) string {
	if n.Fn != nil {
		return path.Base(n.Pkg) + "." + n.Fn.Name()
	}
	return ssax.TrimModule(n.Name, module)
}

func spliceEdge(f *graph.Flow, from, to *graph.Node, async bool, seq int) {
	if from == to {
		return
	}
	if async {
		f.MarkEdgeAsync(from, to)
	}
	f.SetEdgeSeq(from, to, seq)
	for _, ex := range from.Out {
		if ex == to {
			return
		}
	}
	from.Out = append(from.Out, to)
	to.In = append(to.In, from)
}

func detach(n *graph.Node) {
	for _, parent := range n.In {
		parent.Out = removeNode(parent.Out, n)
	}
	for _, child := range n.Out {
		child.In = removeNode(child.In, n)
	}
	n.In, n.Out = nil, nil
}

func removeNode(list []*graph.Node, n *graph.Node) []*graph.Node {
	out := list[:0]
	for _, x := range list {
		if x != n {
			out = append(out, x)
		}
	}
	return out
}
