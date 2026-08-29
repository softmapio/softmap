package guards

import (
	"sort"

	"golang.org/x/tools/go/ssa"

	"github.com/softmapio/softmap/internal/graph"
	"github.com/softmapio/softmap/internal/loader"
	"github.com/softmapio/softmap/internal/ssax"
)

const (
	maxDecisionsPerFunc = 8
	// maxElements is the soft readability budget for total rendered
	// elements (steps + effects + decisions + exits). Once exceeded,
	// lower-priority guards keep only a checks_overflow count.
	maxElements = 80
)

// Annotate runs guard analysis over the surviving steps of a marked flow
// (call between filter.Mark and filter.Prune) and rewrites the graph into
// flowchart form:
//
//	step ─call→ decision ─fail→ exit
//	                └─pass→ decision/next calls
//
// Calls whose site is dominated by a guard's pass branch move onto that
// decision, so the happy path reads left to right with rejections branching
// off. Mechanical propagations never become nodes; they mark the failing
// call's node fallible. Error exits claimed by decisions (or belonging to
// mechanical wraps) leave the node's error_exits list, so nothing shows
// twice.
// markAlternativeEffects: on nodes with several effects, sites in mutually
// non-dominating blocks belong to alternative branches - the panel can say
// "these statements never both run" when it is statically known.
func markAlternativeEffects(p *loader.Program, n *graph.Node) {
	if n.Fn == nil || len(n.Effects) < 2 {
		return
	}
	blocks := map[string]*ssa.BasicBlock{}
	for _, b := range n.Fn.Blocks {
		for _, instr := range b.Instrs {
			call, ok := instr.(ssa.CallInstruction)
			if !ok {
				continue
			}
			if pos := call.Common().Pos(); pos.IsValid() {
				blocks[p.Position(pos)] = b
			}
			if pos := instr.Pos(); pos.IsValid() {
				blocks[p.Position(pos)] = b
			}
		}
	}
	for i := range n.Effects {
		bi := blocks[n.Effects[i].Pos]
		if bi == nil {
			continue
		}
		for j := range n.Effects {
			if i == j {
				continue
			}
			bj := blocks[n.Effects[j].Pos]
			if bj == nil || bi == bj {
				continue
			}
			if !bi.Dominates(bj) && !bj.Dominates(bi) {
				n.Effects[i].Alt = true
				break
			}
		}
	}
}

func Annotate(f *graph.Flow, p *loader.Program) {
	if f.Root.Fn != nil {
		f.Root.SuccessResponse = SuccessResponse(f.Root.Fn)
	}
	ExtractDTOs(f)
	byFn := nodesByFunction(f)
	effectBelow := effectBelowMap(f)

	elements := 1
	for _, n := range f.Order {
		if n.DroppedBy == "" {
			elements++
		}
	}

	type cand struct {
		n  *graph.Node
		gs []Guard
	}
	var cands []cand
	steps := append([]*graph.Node{f.Root}, f.Order...)
	for _, n := range steps {
		if n.Kind != "step" || n.Fn == nil || n.DroppedBy != "" {
			continue
		}
		markAlternativeEffects(p, n)
		gs := Analyze(p, n.Fn)
		if len(gs) == 0 {
			continue
		}
		claimed := map[string]bool{}
		for _, g := range gs {
			if !g.Mechanical {
				continue
			}
			// The badge goes on the failing call's node: "this call may
			// fail (and its error propagates)".
			if g.FailedCall != nil {
				targets := byFn[ssax.Declared(p.Prog, g.FailedCall)]
				for _, target := range targets {
					target.Fallible = true
				}
				// Driver calls have no node of their own anymore: the badge
				// lands on the absorbing method itself.
				if len(targets) == 0 {
					n.Fallible = true
				}
			}
			// A mechanical branch can still return a distinct-looking wrap
			// (%v). Its message must not linger in error_exits.
			e := classifyExitOnly(g)
			if e != "" {
				claimed[e] = true
			}
		}
		sem := semanticGuards(f, p, n, gs, effectBelow)
		if len(sem) > 0 {
			cands = append(cands, cand{n, sem})
		}
		pruneErrorExits(n, claimed)
	}

	// Shallow functions first: the entrypoint's own guards are the story;
	// a depth-6 helper's checks are detail.
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].n.Depth < cands[j].n.Depth })

	for _, c := range cands {
		chosen := c.gs
		if len(chosen) > maxDecisionsPerFunc {
			c.n.ChecksOverflow += len(chosen) - maxDecisionsPerFunc
			chosen = chosen[:maxDecisionsPerFunc]
		}
		// Fit the global budget guard by guard, best outcomes first - a
		// big flow keeps its sentinels and gates instead of losing the
		// whole function's story at once.
		sorted := append([]Guard(nil), chosen...)
		sort.SliceStable(sorted, func(i, j int) bool { return guardScore(sorted[i]) > guardScore(sorted[j]) })
		var fit []Guard
		for _, g := range sorted {
			if elements+guardCost(g) > maxElements {
				c.n.ChecksOverflow++
				continue
			}
			elements += guardCost(g)
			fit = append(fit, g)
		}
		if len(fit) == 0 {
			continue
		}
		sort.SliceStable(fit, func(i, j int) bool { return fit[i].OrderPos < fit[j].OrderPos })
		// Guard evidence is immune: a call whose result feeds a rendered
		// decision's condition must never be filtered away - the reader
		// has to see WHAT was checked.
		for _, g := range fit {
			for _, fn := range g.UsesFns {
				for _, target := range byFn[ssax.Declared(p.Prog, fn)] {
					if target.DroppedBy != "" {
						target.DroppedBy = ""
						target.Collapse = false
					}
				}
			}
		}
		attach(f, p, c.n, fit)
	}
}

// semanticGuards selects and orders the guards worth rendering: priority
// picks WHICH survive the per-function cap (sentinel > message > gates an
// effect), source order decides the chain layout.
func semanticGuards(f *graph.Flow, p *loader.Program, n *graph.Node, gs []Guard, effectBelow map[*graph.Node]bool) []Guard {
	var sem []Guard
	for _, g := range gs {
		if g.Mechanical {
			continue
		}
		if g.Gate {
			// One working side -> gate ("runs or skipped"); both sides
			// working -> branch (exclusive either/or, yes/no children);
			// neither -> silence.
			wPass := gateSideWork(f, p, n, g.PassBlock, effectBelow)
			wSkip := gateSideWork(f, p, n, g.SkipBlock, effectBelow)
			switch {
			case wPass && wSkip:
				g.Branch = true
			case wPass:
				g.FailWhen = false
			case wSkip:
				g.PassBlock, g.SkipBlock = g.SkipBlock, g.PassBlock
				g.FailWhen = true
			default:
				continue
			}
			sem = append(sem, g)
			continue
		}
		// A decision earns canvas space only when its outcome has a NAME -
		// a sentinel or a message. "? err != nil → ✗ error" tells the
		// reader nothing, and unnamed exits used to merge into one shared
		// hub that collected fail edges from the whole map. Unnamed guards
		// surface as the +N checks badge instead.
		if g.Exit.Kind == "unknown" {
			n.ChecksOverflow++
			continue
		}
		// No source `if` behind the condition (a loop header's exhaustion
		// exit): "t7 < t5" is unreadable - the outcome stays visible in
		// error_exits, but it earns no decision card.
		if !g.CondFromAST {
			n.ChecksOverflow++
			continue
		}
		sem = append(sem, g)
	}
	if len(sem) <= maxDecisionsPerFunc {
		sort.SliceStable(sem, func(i, j int) bool { return sem[i].OrderPos < sem[j].OrderPos })
		return sem
	}
	gates := func(g Guard) bool {
		if g.PassBlock == nil {
			return false
		}
		for _, c := range n.Out {
			sb := f.EdgeSiteBlock(n, c)
			if sb != nil && g.PassBlock.Dominates(sb) && effectBelow[c] {
				return true
			}
		}
		return false
	}
	score := func(g Guard) int {
		s := guardScore(g)
		if !g.Gate && gates(g) {
			s++
		}
		return s
	}
	sort.SliceStable(sem, func(i, j int) bool { return score(sem[i]) > score(sem[j]) })
	sem = sem[:maxDecisionsPerFunc]
	sort.SliceStable(sem, func(i, j int) bool { return sem[i].OrderPos < sem[j].OrderPos })
	return sem
}

// attach builds the decision chain under n and moves post-guard call edges
// onto the deepest decision whose pass branch dominates their site.
func attach(f *graph.Flow, p *loader.Program, n *graph.Node, chosen []Guard) {
	existing := append([]*graph.Node(nil), n.Out...)

	claimed := map[string]bool{}
	prev := n
	kind := ""    // first hop is a plain call edge
	passLbl := "" // continue-value label of the previous decision
	decisions := make([]*graph.Node, 0, len(chosen))
	for _, g := range chosen {
		posStr := p.Position(g.StmtPos)
		d := &graph.Node{
			Name:       "decision:" + n.Name + ":" + posStr,
			Fn:         n.Fn,
			Pkg:        n.Pkg,
			Pos:        posStr,
			Kind:       "decision",
			Resolution: "static",
			Depth:      n.Depth + 1,
			Kept:       true,
			Decision:   &graph.DecisionInfo{Condition: g.CondText, Uses: g.Uses, FailWhen: g.FailWhen, Checks: g.Checks},
		}
		f.AddNode(d.Name, d)
		f.AddEdge(prev, d, kind, passLbl, int(g.OrderPos))

		if g.Gate {
			// No exit: the decision gates work instead of ending the flow.
			// Absorbed driver effects made inside the gated region move onto
			// the decision, so "calls accounting API" hangs under the
			// condition that triggers it.
			d.Decision.Gate = true
			d.Decision.Branch = g.Branch
			moveGatedEffects(p, n, d, g.PassBlock)
			// A gate is a leaf of the chain: the NEXT decision runs whether
			// or not the gated block did, so it hangs off the gate's parent,
			// not the gate.
			decisions = append(decisions, d)
			continue
		}
		passLbl = failLabel(!g.FailWhen)

		e := &graph.Node{
			Name:       "exit:" + g.Exit.Display(),
			Pkg:        n.Pkg,
			Pos:        p.Position(g.Exit.Pos),
			Kind:       "exit",
			Resolution: "static",
			Depth:      n.Depth + 2,
			Kept:       true,
			ExitErr: &graph.ExitInfo{
				Kind: g.Exit.Kind, Name: g.Exit.Name, Message: g.Exit.Message,
				Pos: p.Position(g.Exit.Pos),
			},
		}
		// Exits deduplicate by identity within a flow (the same sentinel
		// rejected from two decisions is one outcome).
		if ex, ok := f.Nodes[e.Name]; ok && ex.Kind == "exit" {
			e = ex
		} else {
			f.AddNode(e.Name, e)
		}
		f.AddEdge(d, e, "fail", failLabel(g.FailWhen), int(g.Exit.Pos))
		claimed[g.Exit.Key()] = true

		decisions = append(decisions, d)
		prev = d
		kind = "pass"
	}

	// Post-guard calls hang off the deepest decision that gates them.
	guardBlocks := make([]*ssa.BasicBlock, len(chosen))
	for i, g := range chosen {
		guardBlocks[i] = g.PassBlock
	}
	moved := map[*graph.Node]bool{}
	for _, c := range existing {
		sb := f.EdgeSiteBlock(n, c)
		if sb == nil {
			continue
		}
		target := -1
		for i, pb := range guardBlocks {
			if pb != nil && pb.Dominates(sb) {
				target = i
			}
		}
		if target >= 0 {
			f.RewireEdge(n, c, decisions[target], "pass")
			moved[c] = true
			if !chosen[target].Gate {
				// The continue side of an exit decision is a branch too -
				// label it, never leave the else side implicit.
				f.SetEdgeKind(decisions[target], c, "pass", failLabel(!chosen[target].FailWhen))
			}
			if chosen[target].Branch {
				// yes = the condition holds; the pass side runs when the
				// condition equals !FailWhen.
				lbl := "no"
				if !chosen[target].FailWhen {
					lbl = "yes"
				}
				f.SetEdgeKind(decisions[target], c, "pass", lbl)
			}
		}
	}
	// A branch's OTHER side: children in the skip-side region hang off the
	// same decision, labeled "no" - the exclusive alternative.
	for _, c := range existing {
		if moved[c] {
			continue
		}
		sb := f.EdgeSiteBlock(n, c)
		if sb == nil {
			continue
		}
		for i := len(chosen) - 1; i >= 0; i-- {
			g := chosen[i]
			if g.Branch && g.SkipBlock != nil && g.SkipBlock.Dominates(sb) {
				lbl := "yes"
				if !g.FailWhen {
					lbl = "no"
				}
				f.RewireEdge(n, c, decisions[i], "pass")
				f.SetEdgeKind(decisions[i], c, "pass", lbl)
				break
			}
		}
	}
	// A gate whose gated work was claimed by a deeper decision in the same
	// chain ends up an empty card - a condition pointing at nothing. Remove
	// it rather than render a dangling "yes → steps below run".
	for _, d := range decisions {
		if d.Decision != nil && d.Decision.Gate && len(d.Out) == 0 && len(d.Effects) == 0 {
			for _, parent := range d.In {
				out := parent.Out[:0]
				for _, x := range parent.Out {
					if x != d {
						out = append(out, x)
					}
				}
				parent.Out = out
			}
			d.In = nil
			delete(f.Nodes, d.Name)
		}
	}
	pruneErrorExits(n, claimed)
}

// gateSideWork: does this side of a gate hold visible work - kept child
// calls or absorbed effects in its exclusive region?
func gateSideWork(f *graph.Flow, p *loader.Program, n *graph.Node, blk *ssa.BasicBlock, effectBelow map[*graph.Node]bool) bool {
	if blk == nil {
		return false
	}
	// A multi-predecessor successor is the JOIN - the flow's continuation,
	// not a branch's exclusive region. It holds no work of its own.
	if len(blk.Preds) > 1 {
		return false
	}
	for _, c := range n.Out {
		sb := f.EdgeSiteBlock(n, c)
		if sb == nil || !blk.Dominates(sb) {
			continue
		}
		if c.DroppedBy == "" || len(c.Effects) > 0 || effectBelow[c] {
			return true
		}
	}
	return len(effectsInRegion(p, n, blk)) > 0
}

// effectsInRegion: indices of n's absorbed effects whose call site lives in
// blocks dominated by blk.
func effectsInRegion(p *loader.Program, n *graph.Node, blk *ssa.BasicBlock) []int {
	if n.Fn == nil || len(n.Effects) == 0 {
		return nil
	}
	sites := map[string]bool{}
	for _, b := range n.Fn.Blocks {
		if !blk.Dominates(b) {
			continue
		}
		for _, instr := range b.Instrs {
			call, ok := instr.(ssa.CallInstruction)
			if !ok {
				continue
			}
			if pos := call.Common().Pos(); pos.IsValid() {
				sites[p.Position(pos)] = true
			}
			if pos := instr.Pos(); pos.IsValid() {
				sites[p.Position(pos)] = true
			}
		}
	}
	var idx []int
	for i, use := range n.Effects {
		if sites[use.Pos] {
			idx = append(idx, i)
		}
	}
	return idx
}

func moveGatedEffects(p *loader.Program, n, d *graph.Node, blk *ssa.BasicBlock) {
	idx := effectsInRegion(p, n, blk)
	if len(idx) == 0 {
		return
	}
	move := map[int]bool{}
	for _, i := range idx {
		move[i] = true
	}
	kept := n.Effects[:0]
	for i, use := range n.Effects {
		if move[i] {
			d.Effects = append(d.Effects, use)
		} else {
			kept = append(kept, use)
		}
	}
	n.Effects = kept
}

// guardScore ranks what survives budget pressure: named rejections beat
// gates, gates beat nothing. guardCost is the canvas price: a gate is one
// card, a guard is a decision plus its exit.
func guardScore(g Guard) int {
	switch {
	case g.Gate:
		return 1
	case g.Exit.Kind == "sentinel":
		return 4
	default:
		return 2
	}
}

func guardCost(g Guard) int {
	if g.Gate {
		return 1
	}
	return 2
}

func failLabel(failWhen bool) string {
	if failWhen {
		return "true"
	}
	return "false"
}

// classifyExitOnly renders a mechanical guard's returned wrap for
// error_exits dedupe (it never becomes a node).
func classifyExitOnly(g Guard) string {
	if g.Exit.Kind != "" && g.Exit.Kind != "unknown" {
		return g.Exit.Key()
	}
	return ""
}

func pruneErrorExits(n *graph.Node, claimed map[string]bool) {
	if len(claimed) == 0 || len(n.ErrorExits) == 0 {
		return
	}
	kept := n.ErrorExits[:0]
	for _, e := range n.ErrorExits {
		if !claimed[e] {
			kept = append(kept, e)
		}
	}
	n.ErrorExits = kept
}

func nodesByFunction(f *graph.Flow) map[*ssa.Function][]*graph.Node {
	idx := map[*ssa.Function][]*graph.Node{}
	for _, n := range f.Order {
		if n.Fn != nil {
			idx[n.Fn] = append(idx[n.Fn], n)
		}
	}
	return idx
}

// effectBelowMap: which nodes have an effect in their subtree (cycle-safe).
func effectBelowMap(f *graph.Flow) map[*graph.Node]bool {
	memo := map[*graph.Node]bool{}
	state := map[*graph.Node]int{}
	var visit func(n *graph.Node) bool
	visit = func(n *graph.Node) bool {
		switch state[n] {
		case 1:
			return false
		case 2:
			return len(n.Effects) > 0 || memo[n]
		}
		state[n] = 1
		below := false
		for _, c := range n.Out {
			if visit(c) {
				below = true
			}
		}
		state[n] = 2
		memo[n] = below
		return len(n.Effects) > 0 || below
	}
	visit(f.Root)
	return memo
}
