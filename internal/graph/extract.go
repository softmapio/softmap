package graph

import (
	"fmt"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/ssa"

	"github.com/softmapio/softmap/internal/effects"
	"github.com/softmapio/softmap/internal/loader"
	"github.com/softmapio/softmap/internal/ssax"
)

// Flow is the mutable intermediate representation between the raw call graph
// and JSON output: a DAG (with back-edges for recursion) of meaningful-call
// candidates. The filter pipeline marks and prunes it in place.
type Flow struct {
	Root      *Node
	Nodes     map[string]*Node // by Node.Name
	Order     []*Node          // deterministic BFS insertion order (excludes Root)
	Truncated bool
	Warnings  []string

	// asyncEdge marks individual caller->callee hops that happen via a `go`
	// statement. Async is an edge property: one function may be called
	// synchronously on one path and inside a goroutine on another, and only
	// the latter edge should read as async.
	asyncEdge map[edgeKey]bool
	// edgeKind/edgeLabel carry flowchart semantics added by the guards
	// pass: "pass" and "fail" edges (default is a plain call edge).
	edgeKind  map[edgeKey]string
	edgeLabel map[edgeKey]string
	// edgeSite records the basic block of the call site that created each
	// edge, so guards can attach post-guard calls to the right decision.
	edgeSite map[edgeKey]*ssa.BasicBlock
	// edgeSeq is the source position (token.Pos as int) of the earliest
	// call site behind the edge: children render in code order.
	edgeSeq map[edgeKey]int
}

type edgeKey struct{ from, to *Node }

// DecisionInfo describes a semantic guard rendered as a decision node.
type DecisionInfo struct {
	Condition string
	Uses      []string
	FailWhen  bool // which boolean value of the condition takes the exit
	// Gate: no branch exits - the condition decides whether a block of
	// work runs at all ("if !whiteListClient { check debts }"). FailWhen
	// holds the value that SKIPS the work.
	Gate bool
	// Branch: both sides of the gate hold work - an exclusive either/or
	// ("email lookup or phone lookup"). Child edges carry yes/no labels.
	Branch bool
	// Checks: what a validation guard enforces, human-rendered
	// ("phone - required, length(11, 11), digit").
	Checks string
}

// DTOInfo is a data contract (request or response body) of an entrypoint.
type DTOInfo struct {
	Type      string // "api.CreateOrderRequest"
	Format    string // "json" | "xml"
	Fields    []DTOField
	Truncated bool
}

type DTOField struct {
	Name  string // Go field name
	Tag   string // wire name from the json/xml/form tag
	Type  string
	Rules string // validation rules (ozzo calls + validate/binding tags)
}

// ExitInfo describes the error outcome of a decision's fail branch.
type ExitInfo struct {
	Kind    string // "sentinel" | "message" | "unknown"
	Name    string
	Message string
	Pos     string
}

// EdgeAsync reports whether the from->to hop happens via a goroutine.
func (f *Flow) EdgeAsync(from, to *Node) bool { return f.asyncEdge[edgeKey{from, to}] }

// EdgeKind returns "pass"/"fail" for flowchart edges, "" for plain calls.
func (f *Flow) EdgeKind(from, to *Node) string { return f.edgeKind[edgeKey{from, to}] }

// EdgeLabel returns the short display label of an edge, if any.
func (f *Flow) EdgeLabel(from, to *Node) string { return f.edgeLabel[edgeKey{from, to}] }

// EdgeSiteBlock returns the basic block of the call site behind the edge.
func (f *Flow) EdgeSiteBlock(from, to *Node) *ssa.BasicBlock {
	return f.edgeSite[edgeKey{from, to}]
}

// EdgeSeq is the source-order key of an edge (0 = unknown, sorts last).
func (f *Flow) EdgeSeq(from, to *Node) int { return f.edgeSeq[edgeKey{from, to}] }

// SetEdgeSeq records the source-order key, keeping the earliest.
func (f *Flow) SetEdgeSeq(from, to *Node, seq int) {
	if seq <= 0 {
		return
	}
	if f.edgeSeq == nil {
		f.edgeSeq = map[edgeKey]int{}
	}
	key := edgeKey{from, to}
	if cur, ok := f.edgeSeq[key]; !ok || seq < cur {
		f.edgeSeq[key] = seq
	}
}

// SetEdgeKind records flowchart semantics on an existing edge.
func (f *Flow) SetEdgeKind(from, to *Node, kind, label string) {
	if f.edgeKind == nil {
		f.edgeKind = map[edgeKey]string{}
		f.edgeLabel = map[edgeKey]string{}
	}
	f.edgeKind[edgeKey{from, to}] = kind
	if label != "" {
		f.edgeLabel[edgeKey{from, to}] = label
	}
}

// AddNode registers an externally built node (decisions, exits) under a
// unique key and appends it to the deterministic order.
func (f *Flow) AddNode(key string, n *Node) {
	f.Nodes[key] = n
	f.Order = append(f.Order, n)
}

// AddEdge adds an edge with flowchart semantics at a source position.
func (f *Flow) AddEdge(from, to *Node, kind, label string, seq int) {
	f.edge(from, to, false, nil, seq)
	if kind != "" {
		f.SetEdgeKind(from, to, kind, label)
	}
}

// RewireEdge moves the from->to edge to newFrom->to, preserving async and
// site metadata and applying the given kind.
func (f *Flow) RewireEdge(from, to, newFrom *Node, kind string) {
	key := edgeKey{from, to}
	async := f.asyncEdge[key]
	site := f.edgeSite[key]
	seq := f.edgeSeq[key]
	from.Out = removeFromList(from.Out, to)
	to.In = removeFromList(to.In, from)
	delete(f.asyncEdge, key)
	delete(f.edgeSite, key)
	delete(f.edgeSeq, key)
	f.edge(newFrom, to, async, site, seq)
	if kind != "" {
		f.SetEdgeKind(newFrom, to, kind, "")
	}
}

func removeFromList(list []*Node, n *Node) []*Node {
	out := list[:0]
	for _, x := range list {
		if x != n {
			out = append(out, x)
		}
	}
	return out
}

// MarkEdgeAsync records an async hop (also used when the filter splices
// collapse chains).
func (f *Flow) MarkEdgeAsync(from, to *Node) {
	if f.asyncEdge == nil {
		f.asyncEdge = map[edgeKey]bool{}
	}
	f.asyncEdge[edgeKey{from, to}] = true
	to.Async = true // node keeps the "reached async on some path" OR
}

// EffectUse is one boundary call absorbed by a module-code node: the effect
// identity plus where in the module it happens.
type EffectUse struct {
	*effects.Effect
	Pos   string // call-site position in module code
	Async bool   // the call happens inside a goroutine
	// Alt: this effect's site is in a branch mutually exclusive with
	// another effect on the same node - they never both run.
	Alt bool
}

type Node struct {
	Name string // qualified display name; the node's identity
	Fn   *ssa.Function
	Pkg  string
	Pos  string
	Kind string // "step" | "terminal" | "decision" | "exit"
	// Effects are the boundary calls this node performs, in call-site
	// order. Driver calls (database/sql, go-redis, kafka writers, ...) are
	// not nodes: the module method that makes them carries them.
	Effects    []EffectUse
	Async      bool   // reached via a `go` statement on some edge
	Resolution string // "static" | "static-multi" | "dynamic" | "truncated"
	Depth      int

	Out []*Node
	In  []*Node

	// Returns renders the function's result types compactly
	// ("*model.Order, error") - what the reader gets out of this step.
	Returns string

	// ErrorExits are the named error outcomes this function can return -
	// business branch points (guards, permission checks) a reader cares
	// about alongside effects.
	ErrorExits []string

	// SuccessResponse: how the entrypoint completes successfully over HTTP
	// ("HTTP 200"), when statically known.
	SuccessResponse string
	// RequestDTO/ResponseDTO: the entrypoint's data contracts.
	RequestDTO  *DTOInfo
	ResponseDTO *DTOInfo

	// Guards layer (set by internal/guards after filtering).
	Fallible       bool          // some caller propagates this call's error
	ChecksOverflow int           // semantic guards beyond the render budget
	Decision       *DecisionInfo // kind == "decision"
	ExitErr        *ExitInfo     // kind == "exit"

	// Filter bookkeeping.
	Collapsed []string // names of wrappers inlined into this node
	DroppedBy string   // rule id that dropped/collapsed this node, "" = kept
	Collapse  bool     // marked for collapse rather than drop
	Kept      bool     // protected: effects and ancestors of effects
}

// Limits guards raw extraction; hitting one marks the flow truncated rather
// than failing.
type Limits struct {
	MaxDepth int
	MaxNodes int
}

var DefaultLimits = Limits{MaxDepth: 40, MaxNodes: 5000}

// maxFallbackImpls bounds the per-site CHA fallback: an interface with this
// many implementations is genuinely ambiguous, and expanding it would
// re-create the spaghetti CHA is known for. The call stays a dynamic
// terminal instead.
const maxFallbackImpls = 5

// Extract walks the call graph breadth-first from root, producing the raw
// flow: every reachable module function, effect leaves at library
// boundaries, terminal nodes for unresolvable dynamic calls, async tags for
// goroutine spawns.
//
// fallback (may be nil, typically the CHA graph) is consulted per call site
// when cg resolves an interface call to nothing - the signature pattern of
// reflection-based dependency injection, where VTA never observes the
// allocation that flows into an interface field. A unique implementation is
// then taken as resolved; up to maxFallbackImpls become static-multi edges;
// more stays an honest dynamic terminal.
func Extract(p *loader.Program, cg *callgraph.Graph, fallback *callgraph.Graph, root *ssa.Function, limits Limits) (*Flow, error) {
	if limits.MaxDepth == 0 {
		limits.MaxDepth = DefaultLimits.MaxDepth
	}
	if limits.MaxNodes == 0 {
		limits.MaxNodes = DefaultLimits.MaxNodes
	}
	rootNode := cg.Nodes[root]
	if rootNode == nil {
		return nil, fmt.Errorf("entrypoint %s is not in the call graph (dead code, or excluded from analysis)", ssax.FuncDisplayName(root))
	}

	f := &Flow{Nodes: map[string]*Node{}}
	f.Root = f.node(p, root)
	f.Root.Kind = "step"

	type item struct {
		n     *Node
		cgn   *callgraph.Node
		depth int
	}
	queue := []item{{f.Root, rootNode, 0}}
	visited := map[*Node]bool{f.Root: true}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if cur.depth >= limits.MaxDepth {
			cur.n.Kind, cur.n.Resolution = "terminal", "truncated"
			f.Truncated = true
			continue
		}

		for _, site := range callSites(cur.cgn.Func) {
			common := site.Common()
			if _, isBuiltin := common.Value.(*ssa.Builtin); isBuiltin && !common.IsInvoke() {
				continue // len, append, panic, ... are not calls in a flow map
			}
			info := ssax.Callee(site)
			if info != nil && info.Invoke && info.Pkg == "" && info.Type == "error" {
				continue // err.Error() is not meaningful dynamic dispatch
			}
			_, isGo := site.(*ssa.Go)

			// A driver call is not a step: the module method making it
			// absorbs the effect (and never descends into the library).
			if e := effects.Detect(info, site); e != nil {
				cur.n.absorbEffect(e, p.Position(site.Pos()), isGo)
				continue
			}

			callees := calleesAt(cg, cur.cgn, site)
			dynamic := info == nil || (info.Invoke && info.Static == nil)
			if len(callees) == 0 && dynamic && info != nil && fallback != nil {
				// DI-wired interface: ask the sound over-approximation.
				if fb := calleesAt(fallback, fallback.Nodes[cur.cgn.Func], site); len(fb) > 0 && len(fb) <= maxFallbackImpls {
					callees = fb
				}
			}
			if len(callees) == 0 {
				// Uncertainty worth showing: unresolved calls through
				// module-local interfaces or function values. Unresolved
				// invokes on dependency interfaces (rows.Scan) and values
				// of dependency-declared func types (context.CancelFunc)
				// are boundary plumbing, skipped like body-less dep calls.
				if dynamic && (info == nil || p.PkgInModule(info.Pkg)) && !depFuncType(p, common.Value.Type()) {
					child := f.dynamicNode(p, site, info, cur.depth+1)
					f.edge(cur.n, child, isGo, site.Block(), int(site.Pos()))
				}
				continue
			}

			multi := len(callees) > 1
			for _, callee := range callees {
				callee = canonical(p.Prog, callee)
				if callee == nil || !worthVisiting(p, callee) {
					continue
				}
				child := f.node(p, callee)
				if multi && child.Resolution == "static" {
					child.Resolution = "static-multi"
				}
				f.edge(cur.n, child, isGo, site.Block(), int(site.Pos()))
				if visited[child] {
					continue
				}
				visited[child] = true
				if child.Depth == 0 {
					child.Depth = cur.depth + 1
				}
				if len(f.Nodes) >= limits.MaxNodes {
					child.Kind, child.Resolution = "terminal", "truncated"
					f.Truncated = true
					continue
				}
				// A callee can miss a graph node (e.g. outside the VTA
				// cone); it stays a leaf rather than crashing the walk.
				if next := cg.Nodes[callee]; next != nil {
					queue = append(queue, item{child, next, cur.depth + 1})
				}
			}
		}
	}
	if f.Truncated {
		f.Warnings = append(f.Warnings, fmt.Sprintf("flow truncated at limits (max-depth=%d, max-nodes=%d); the graph is incomplete", limits.MaxDepth, limits.MaxNodes))
	}
	return f, nil
}

// depFuncType reports a named function type declared outside the module,
// e.g. context.CancelFunc: calling such a value is plumbing, not a step.
func depFuncType(p *loader.Program, t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	return !p.PkgInModule(named.Obj().Pkg().Path())
}

// formatResults renders result types with bare package names:
// "(*model.Order, error)" reads; full import paths do not.
func formatResults(fn *ssa.Function) string {
	results := fn.Signature.Results()
	if results.Len() == 0 {
		return ""
	}
	qual := func(p *types.Package) string { return p.Name() }
	parts := make([]string, results.Len())
	for i := 0; i < results.Len(); i++ {
		parts[i] = types.TypeString(results.At(i).Type(), qual)
	}
	return strings.Join(parts, ", ")
}

// callSites returns fn's call instructions in deterministic source order.
func callSites(fn *ssa.Function) []ssa.CallInstruction {
	if fn == nil {
		return nil
	}
	var out []ssa.CallInstruction
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if site, ok := instr.(ssa.CallInstruction); ok {
				out = append(out, site)
			}
		}
	}
	return out
}

// calleesAt collects the call-graph edges of one call site, sorted for
// determinism.
func calleesAt(cg *callgraph.Graph, n *callgraph.Node, site ssa.CallInstruction) []*ssa.Function {
	if n == nil {
		return nil
	}
	var out []*ssa.Function
	for _, e := range n.Out {
		if e.Site == site && e.Callee.Func != nil {
			out = append(out, e.Callee.Func)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return ssax.FuncDisplayName(out[i]) < ssax.FuncDisplayName(out[j])
	})
	return out
}

// canonical maps synthetic wrappers (promoted methods, bound methods,
// thunks) and generic instantiations to the declared function they stand for.
func canonical(prog *ssa.Program, fn *ssa.Function) *ssa.Function {
	if fn == nil {
		return nil
	}
	if orig := fn.Origin(); orig != nil && orig != fn {
		fn = orig
	}
	if fn.Synthetic != "" {
		if tf, ok := fn.Object().(*types.Func); ok {
			if real := prog.FuncValue(tf); real != nil {
				return real
			}
		}
	}
	return fn
}

// worthVisiting keeps the raw flow to code we can say something about:
// module functions (any file class; exclusion is the filter's job so paths
// stay connected) and anonymous functions inside them.
func worthVisiting(p *loader.Program, fn *ssa.Function) bool {
	if fn.Blocks == nil {
		return false
	}
	return p.InModule(fn)
}

func (f *Flow) node(p *loader.Program, fn *ssa.Function) *Node {
	name := ssax.FuncDisplayName(fn)
	if n, ok := f.Nodes[name]; ok {
		return n
	}
	n := &Node{
		Name:       name,
		Fn:         fn,
		Pkg:        loader.FuncPackage(fn),
		Pos:        p.Position(fn.Pos()),
		Kind:       "step",
		Resolution: "static",
		ErrorExits: ssax.SentinelErrors(fn),
		Returns:    formatResults(fn),
	}
	f.Nodes[name] = n
	if f.Root != nil {
		f.Order = append(f.Order, n)
	}
	// Pre-mark functions from generated/vendored files: the pre-analysis
	// exclusion pass could not delete them without breaking type soundness,
	// so they carry a pseudo-rule id and are pruned with the noise unless
	// keep-protection saves the path through them.
	switch p.FuncClass(fn) {
	case loader.Generated:
		n.DroppedBy = "excluded:generated-file"
	case loader.Vendor:
		n.DroppedBy = "excluded:vendor"
	}
	return n
}

// absorbEffect attaches a boundary call to the node, deduplicating exact
// repeats (same type+detail+topic) while preserving call-site order. A node
// with effects is meaningful by definition: immune to rule drops (it may
// still be budget-collapsed, in which case its effects bubble upward).
func (n *Node) absorbEffect(e *effects.Effect, pos string, async bool) {
	n.mergeUse(EffectUse{Effect: e, Pos: pos, Async: async})
	n.Kept = true
}

func (n *Node) mergeUse(use EffectUse) {
	for i := range n.Effects {
		ex := &n.Effects[i]
		if ex.Type == use.Type && ex.Detail == use.Detail && ex.Topic == use.Topic {
			ex.Async = ex.Async || use.Async
			return
		}
	}
	n.Effects = append(n.Effects, use)
}

// MergeEffectsFrom bubbles a collapsing node's effects into its survivor,
// deduplicating: what the subtree ultimately does stays visible.
func (n *Node) MergeEffectsFrom(c *Node) {
	for _, use := range c.Effects {
		n.mergeUse(use)
	}
}

// dynamicNode synthesizes a terminal for an unresolvable dynamic call. It is
// never dropped: uncertainty must stay visible.
func (f *Flow) dynamicNode(p *loader.Program, site ssa.CallInstruction, info *ssax.CalleeInfo, depth int) *Node {
	name := "dynamic:"
	switch {
	case info != nil: // interface call with no resolved implementations
		name += fmt.Sprintf("%s.%s.%s", info.Pkg, info.Type, info.Name)
	default: // function-value call
		name += describeFuncValue(site.Common().Value)
	}
	if n, ok := f.Nodes[name]; ok {
		return n
	}
	n := &Node{
		Name:       name,
		Kind:       "terminal",
		Resolution: "dynamic",
		Pos:        p.Position(site.Pos()),
		Kept:       true,
		Depth:      depth,
	}
	f.Nodes[name] = n
	f.Order = append(f.Order, n)
	return n
}

func describeFuncValue(v ssa.Value) string {
	for depth := 0; depth < 5; depth++ {
		switch vv := v.(type) {
		case *ssa.UnOp:
			v = vv.X
		case *ssa.Global:
			return vv.String()
		default:
			return v.Type().String()
		}
	}
	return v.Type().String()
}

func (f *Flow) edge(from, to *Node, async bool, site *ssa.BasicBlock, seq int) {
	if from == to {
		return // self-recursion adds nothing to a flow map
	}
	f.SetEdgeSeq(from, to, seq)
	if async {
		f.MarkEdgeAsync(from, to)
	}
	if site != nil {
		if f.edgeSite == nil {
			f.edgeSite = map[edgeKey]*ssa.BasicBlock{}
		}
		if _, exists := f.edgeSite[edgeKey{from, to}]; !exists {
			f.edgeSite[edgeKey{from, to}] = site
		}
	}
	for _, ex := range from.Out {
		if ex == to {
			return
		}
	}
	from.Out = append(from.Out, to)
	to.In = append(to.In, from)
}
