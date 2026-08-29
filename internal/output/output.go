// Package output serializes a graph.Flow to the stable JSON schema and
// renders the --debug-tree text view. Output is fully deterministic: node
// ids follow BFS order over sorted edges, and every list is emitted in a
// fixed order.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/softmapio/softmap/internal/entrypoints"
	"github.com/softmapio/softmap/internal/graph"
	"github.com/softmapio/softmap/internal/ssax"
)

const SchemaVersion = "2"

type Document struct {
	SchemaVersion string        `json:"schema_version"`
	Module        string        `json:"module"`
	Entrypoint    EntrypointDoc `json:"entrypoint"`
	Nodes         []NodeDoc     `json:"nodes"`
	Edges         []EdgeDoc     `json:"edges"`
	Stats         Stats         `json:"stats"`
}

type EntrypointDoc struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	Topic  string `json:"topic,omitempty"`
	Func   string `json:"func"`
	Pos    string `json:"pos"`
	// ErrorExits of the entrypoint handler itself (it is not in nodes).
	ErrorExits []string `json:"error_exits,omitempty"`
	// Effects the handler performs directly.
	Effects []EffectDoc `json:"effects,omitempty"`
	// Returns: the handler's result types (feeds the success terminal).
	Returns string `json:"returns,omitempty"`
	// Success: the statically known success response ("HTTP 200").
	Success string `json:"success,omitempty"`
	// Request/Response: the entrypoint's data contracts (bound request
	// body, success payload / gRPC message types) with wire field names.
	Request  *DTODoc `json:"request,omitempty"`
	Response *DTODoc `json:"response,omitempty"`
}

// DTODoc is a data contract: type, encoding, and fields as they appear on
// the wire.
type DTODoc struct {
	Type      string        `json:"type"`
	Format    string        `json:"format,omitempty"`
	Fields    []DTOFieldDoc `json:"fields,omitempty"`
	Truncated bool          `json:"truncated,omitempty"`
}

type DTOFieldDoc struct {
	Name  string `json:"name"`
	Tag   string `json:"tag,omitempty"`
	Type  string `json:"type"`
	Rules string `json:"rules,omitempty"`
}

type NodeDoc struct {
	ID      string `json:"id"`
	Func    string `json:"func"`
	Package string `json:"package"`
	Pos     string `json:"pos,omitempty"`
	Kind    string `json:"kind"`
	// Effects: boundary calls this step performs, in call-site order.
	// Driver calls are not nodes; the module method carries them.
	Effects    []EffectDoc `json:"effects,omitempty"`
	Async      bool        `json:"async"`
	Resolution string      `json:"resolution"`
	Collapsed  []string    `json:"collapsed,omitempty"`
	// ErrorExits lists named error outcomes NOT rendered as decisions
	// (overflow and non-guard returns), so nothing displays twice.
	ErrorExits []string `json:"error_exits,omitempty"`

	// Guards layer (schema v2).
	// Label: manual override from .softmap.yaml, applied in Business mode.
	Label          string        `json:"label,omitempty"`
	Condition      string        `json:"condition,omitempty"` // kind == "decision"
	Gate           bool          `json:"gate,omitempty"`      // decision gates work instead of exiting
	Branch         bool          `json:"branch,omitempty"`    // gate with work on BOTH sides (either/or)
	Checks         string        `json:"checks,omitempty"`    // validation rules a decision enforces
	Uses           []string      `json:"uses,omitempty"`      // data feeding the condition
	Error          *ExitErrorDoc `json:"error,omitempty"`     // kind == "exit"
	Fallible       bool          `json:"fallible,omitempty"`
	ChecksOverflow int           `json:"checks_overflow,omitempty"`
	// Returns: the step's result types, compactly rendered
	// ("*model.Order, error") - what the reader gets out of this step.
	Returns string `json:"returns,omitempty"`
}

// ExitErrorDoc is the error identity of an exit node.
type ExitErrorDoc struct {
	Kind    string `json:"kind"` // "sentinel" | "message" | "unknown"
	Name    string `json:"name,omitempty"`
	Message string `json:"message,omitempty"`
	Pos     string `json:"pos,omitempty"`
}

// EffectDoc emits "topic" only for kafka effects - as null when the topic
// could not be resolved statically, with topic_expr as the source hint.
type EffectDoc struct {
	Type      string  `json:"type"`
	// Label: manual override from .softmap.yaml, applied in Business mode.
	Label     string  `json:"label,omitempty"`
	Detail    string  `json:"detail"`
	Topic     *string `json:"topic,omitempty"`
	TopicExpr string  `json:"topic_expr,omitempty"`
	Pos       string  `json:"pos,omitempty"`
	Async     bool    `json:"async,omitempty"`
	// Alt: sits in a branch mutually exclusive with another effect of the
	// same node - statically known to never both run.
	Alt bool `json:"alt,omitempty"`

	isKafka bool
}

func (e EffectDoc) MarshalJSON() ([]byte, error) {
	type plain struct {
		Type   string `json:"type"`
		Detail string `json:"detail"`
		Pos    string `json:"pos,omitempty"`
		Async  bool   `json:"async,omitempty"`
	}
	if !e.isKafka {
		return json.Marshal(plain{e.Type, e.Detail, e.Pos, e.Async})
	}
	type kafka struct {
		Type      string  `json:"type"`
		Detail    string  `json:"detail"`
		Topic     *string `json:"topic"`
		TopicExpr string  `json:"topic_expr,omitempty"`
		Pos       string  `json:"pos,omitempty"`
		Async     bool    `json:"async,omitempty"`
	}
	return json.Marshal(kafka{e.Type, e.Detail, e.Topic, e.TopicExpr, e.Pos, e.Async})
}

type EdgeDoc struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Async marks this specific hop as happening via a goroutine. It lives
	// on the edge, not only the node: the same function can be called
	// synchronously on one path and via `go` on another.
	Async bool `json:"async,omitempty"`
	// Kind: "pass"/"fail" for flowchart edges around decisions; omitted for
	// plain call edges (back-compat default).
	Kind  string `json:"kind,omitempty"`
	Label string `json:"label,omitempty"`
}

type Stats struct {
	RawNodes      int            `json:"raw_nodes"`
	KeptNodes     int            `json:"kept_nodes"`
	DroppedByRule map[string]int `json:"dropped_by_rule"`
}

// FromFlow converts a (filtered or raw) flow into the output document.
// rawNodes is the pre-filter node count; droppedByRule the per-rule counts.
func FromFlow(f *graph.Flow, ep *entrypoints.Entrypoint, module, epPos string, rawNodes int, droppedByRule map[string]int) *Document {
	doc := &Document{
		SchemaVersion: SchemaVersion,
		Module:        module,
		Entrypoint: EntrypointDoc{
			ID:         ep.ID,
			Kind:       ep.Kind,
			Method:     ep.Method,
			Path:       ep.Path,
			Topic:      ep.Topic,
			Func:       ssax.TrimModule(ep.FuncName(), module),
			Pos:        epPos,
			ErrorExits: f.Root.ErrorExits,
			Returns:    f.Root.Returns,
			Success:    f.Root.SuccessResponse,
			Request:    dtoDoc(f.Root.RequestDTO),
			Response:   dtoDoc(f.Root.ResponseDTO),
		},
		Stats: Stats{RawNodes: rawNodes, DroppedByRule: droppedByRule},
	}
	for _, use := range f.Root.Effects {
		doc.Entrypoint.Effects = append(doc.Entrypoint.Effects, effectDoc(use))
	}
	if doc.Stats.DroppedByRule == nil {
		doc.Stats.DroppedByRule = map[string]int{}
	}

	// Deterministic ids: BFS over the surviving graph from the root, with
	// each node's children in SOURCE order (edge seq = call-site position),
	// so the document - and the viewer reading it - follow the code.
	childrenOf := func(n *graph.Node) []*graph.Node { return sourceOrdered(f, n) }
	ids := map[*graph.Node]string{}
	var order []*graph.Node
	queue := []*graph.Node{f.Root}
	seen := map[*graph.Node]bool{f.Root: true}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if n != f.Root {
			ids[n] = fmt.Sprintf("n%d", len(ids)+1)
			order = append(order, n)
		}
		for _, child := range childrenOf(n) {
			if !seen[child] {
				seen[child] = true
				queue = append(queue, child)
			}
		}
	}

	for _, n := range order {
		doc.Nodes = append(doc.Nodes, nodeDoc(n, ids[n], module))
	}
	rootID := "entrypoint"
	emit := func(from *graph.Node) {
		fromID := rootID
		if from != f.Root {
			fromID = ids[from]
		}
		for _, to := range childrenOf(from) {
			doc.Edges = append(doc.Edges, EdgeDoc{
				From: fromID, To: ids[to],
				Async: f.EdgeAsync(from, to),
				Kind:  f.EdgeKind(from, to),
				Label: f.EdgeLabel(from, to),
			})
		}
	}
	emit(f.Root)
	for _, n := range order {
		emit(n)
	}
	doc.Stats.KeptNodes = len(doc.Nodes)
	return doc
}

func nodeDoc(n *graph.Node, id, module string) NodeDoc {
	doc := NodeDoc{
		ID:             id,
		Func:           ssax.TrimModule(n.Name, module),
		Package:        n.Pkg,
		Pos:            n.Pos,
		Kind:           n.Kind,
		Async:          n.Async,
		Resolution:     n.Resolution,
		Collapsed:      collapsedList(n.Collapsed),
		ErrorExits:     n.ErrorExits,
		Fallible:       n.Fallible,
		ChecksOverflow: n.ChecksOverflow,
		Returns:        n.Returns,
	}
	if n.Decision != nil {
		doc.Condition = n.Decision.Condition
		doc.Gate = n.Decision.Gate
		doc.Branch = n.Decision.Branch
		doc.Uses = n.Decision.Uses
		doc.Checks = n.Decision.Checks
	}
	if n.ExitErr != nil {
		doc.Error = &ExitErrorDoc{Kind: n.ExitErr.Kind, Name: n.ExitErr.Name, Message: n.ExitErr.Message, Pos: n.ExitErr.Pos}
	}
	for _, use := range n.Effects {
		doc.Effects = append(doc.Effects, effectDoc(use))
	}
	return doc
}

func effectDoc(use graph.EffectUse) EffectDoc {
	doc := EffectDoc{
		Type: use.Type, Detail: use.Detail, TopicExpr: use.TopicExpr,
		Pos: use.Pos, Async: use.Async, Alt: use.Alt, isKafka: use.Type == "kafka",
	}
	if use.Topic != "" {
		t := use.Topic
		doc.Topic = &t
	}
	return doc
}

func sortedCopy(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// maxCollapsedNames caps the collapsed list per node: diamond call paths can
// funnel hundreds of inlined wrappers into one survivor, and past a couple
// dozen the list stops informing anyone.
const maxCollapsedNames = 24

func collapsedList(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	uniq := sortedCopy(s)
	uniq = slices.Compact(uniq)
	if len(uniq) > maxCollapsedNames {
		rest := len(uniq) - maxCollapsedNames
		uniq = append(uniq[:maxCollapsedNames], fmt.Sprintf("... (+%d more)", rest))
	}
	return uniq
}

// WriteJSON emits the document with stable formatting.
func (d *Document) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(d)
}

func dtoDoc(d *graph.DTOInfo) *DTODoc {
	if d == nil {
		return nil
	}
	doc := &DTODoc{Type: d.Type, Format: d.Format, Truncated: d.Truncated}
	for _, f := range d.Fields {
		doc.Fields = append(doc.Fields, DTOFieldDoc{Name: f.Name, Tag: f.Tag, Type: f.Type, Rules: f.Rules})
	}
	return doc
}

// sourceOrdered returns n's children sorted by the source position of the
// call that creates each edge - the order the code reads.
func sourceOrdered(f *graph.Flow, n *graph.Node) []*graph.Node {
	out := append([]*graph.Node(nil), n.Out...)
	const last = int(^uint(0) >> 1)
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := f.EdgeSeq(n, out[i]), f.EdgeSeq(n, out[j])
		if si == 0 {
			si = last
		}
		if sj == 0 {
			sj = last
		}
		return si < sj
	})
	return out
}

// WriteDebugTree renders the flow as an indented tree with filter decisions,
// for eyeballing readability without any UI. Runs on the marked-but-unpruned
// flow so dropped nodes appear annotated with the rule that removed them.
func WriteDebugTree(w io.Writer, f *graph.Flow, module string) {
	seen := map[*graph.Node]bool{}
	path := map[*graph.Node]bool{}
	var walk func(n *graph.Node, depth int)
	walk = func(n *graph.Node, depth int) {
		line := strings.Repeat("  ", depth) + treeLabel(n, module) + annotations(n)
		if seen[n] && len(n.Out) > 0 {
			fmt.Fprintf(w, "%s  (subtree shown above)\n", line)
			return
		}
		seen[n] = true
		fmt.Fprintln(w, line)
		for _, use := range n.Effects {
			detail := use.Detail
			if use.Type == "kafka" && use.Topic != "" {
				detail = "topic=" + use.Topic
			}
			async := ""
			if use.Async {
				async = "  [async]"
			}
			fmt.Fprintf(w, "%s· %s: %s%s\n", strings.Repeat("  ", depth+1), use.Type, detail, async)
		}
		path[n] = true
		for _, child := range sourceOrdered(f, n) {
			if path[child] {
				fmt.Fprintf(w, "%s%s  (cycle)\n", strings.Repeat("  ", depth+1), ssax.TrimModule(child.Name, module))
				continue
			}
			walk(child, depth+1)
		}
		delete(path, n)
	}
	walk(f.Root, 0)
}

// treeLabel renders decision/exit nodes as flowchart story lines.
func treeLabel(n *graph.Node, module string) string {
	switch n.Kind {
	case "decision":
		return "? " + n.Decision.Condition
	case "exit":
		if n.ExitErr.Kind == "sentinel" {
			return "✗ exit: " + n.ExitErr.Name
		}
		if n.ExitErr.Kind == "message" {
			return "✗ exit: \"" + n.ExitErr.Message + "\""
		}
		return "✗ exit: error"
	}
	return ssax.TrimModule(n.Name, module)
}

func annotations(n *graph.Node) string {
	var tags []string
	if n.Async {
		tags = append(tags, "async")
	}
	switch n.Resolution {
	case "static-multi", "dynamic", "truncated":
		tags = append(tags, n.Resolution)
	}
	if n.DroppedBy != "" {
		action := "dropped"
		if n.Collapse {
			action = "collapsed"
		}
		tags = append(tags, action+": "+n.DroppedBy)
	}
	if n.Fallible {
		tags = append(tags, "!")
	}
	if n.ChecksOverflow > 0 {
		tags = append(tags, fmt.Sprintf("+%d more checks", n.ChecksOverflow))
	}
	if n.Kind == "decision" && n.Decision.Branch {
		tags = append(tags, "branch")
	} else if n.Kind == "decision" && n.Decision.Gate {
		tags = append(tags, "gate")
	}
	if n.Kind == "decision" && len(n.Decision.Uses) > 0 {
		tags = append(tags, "uses: "+strings.Join(n.Decision.Uses, ", "))
	}
	if len(n.Collapsed) > 0 {
		tags = append(tags, "inlined: "+strings.Join(sortedCopy(n.Collapsed), ", "))
	}
	if len(tags) == 0 {
		return ""
	}
	return "  [" + strings.Join(tags, "] [") + "]"
}
