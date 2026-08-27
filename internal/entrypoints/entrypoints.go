// Package entrypoints discovers flow entrypoints: HTTP handler registrations
// (net/http, gin, echo, chi, gorilla/mux, fiber), gRPC service registration
// and Kafka consumer handlers (segmentio/kafka-go, sarama, confluent).
// Discovery is best effort by design; Resolve implements the --entrypoint
// escape hatch for anything discovery misses.
package entrypoints

import (
	"fmt"
	"go/token"
	"sort"
	"strings"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/softmapio/softmap/internal/loader"
	"github.com/softmapio/softmap/internal/ssax"
)

type Entrypoint struct {
	ID     string
	Kind   string // "http" | "kafka" | "func"
	Method string // HTTP method, "" when unknown
	Path   string // HTTP route path, "" when unknown
	Topic  string // Kafka topic, "" when unknown
	Fn     *ssa.Function
	Pos    token.Pos
}

// FuncName returns the qualified function name of the handler.
func (e *Entrypoint) FuncName() string { return ssax.FuncDisplayName(e.Fn) }

// matcher inspects one call site inside enclosing and returns any
// entrypoints it registers.
type matcher func(p *loader.Program, enclosing *ssa.Function, site ssa.CallInstruction, info *ssax.CalleeInfo) []Entrypoint

var matchers = []matcher{
	matchNetHTTP,
	matchGin,
	matchEcho,
	matchChi,
	matchGorilla,
	matchFiber,
	matchGRPCRegister,
	matchSaramaConsumer,
	matchKafkaGoConsumer,
	matchConfluentConsumer,
}

// Frameworks lists, for user-facing messages, what the matchers above
// recognize. Keeping it next to the table is what stops the CLI from
// advertising a framework discovery does not actually cover.
const Frameworks = "net/http (incl. Go 1.22 method patterns), chi, gin, echo, " +
	"gorilla/mux, fiber (v2, v3), protoc-generated gRPC registration, and Kafka " +
	"consumers (segmentio/kafka-go, sarama, confluent)"

// Discover scans all module-local, non-generated functions for registration
// call sites. Results are deterministic: sorted by ID, duplicate handler
// functions deduplicated, ID collisions suffixed by position order.
func Discover(p *loader.Program) []Entrypoint {
	var found []Entrypoint
	seen := map[string]bool{} // Kind + handler identity, to dedupe e.g. a ReadMessage loop
	for fn := range ssautil.AllFunctions(p.Prog) {
		if !p.InModule(fn) || p.FuncClass(fn) != loader.Normal || fn.Blocks == nil {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				site, ok := instr.(ssa.CallInstruction)
				if !ok {
					continue
				}
				info := ssax.Callee(site)
				if info == nil {
					continue
				}
				for _, m := range matchers {
					for _, ep := range m(p, fn, site, info) {
						// An entrypoint must be handler code of the module
						// itself; resolution falling through to a stdlib or
						// dependency method (e.g. HandlerFunc.ServeHTTP)
						// means we failed to find the real handler.
						if ep.Fn == nil || !p.InModule(ep.Fn) || p.FuncClass(ep.Fn) != loader.Normal {
							continue
						}
						key := ep.Kind + "\x00" + ssax.FuncDisplayName(ep.Fn) + "\x00" + ep.Method + ep.Path + ep.Topic
						if seen[key] {
							continue
						}
						seen[key] = true
						found = append(found, ep)
					}
				}
			}
		}
	}
	assignIDs(p, found)
	sort.Slice(found, func(i, j int) bool { return found[i].ID < found[j].ID })
	return found
}

// assignIDs builds stable IDs: http:GET:/path when route info resolved,
// kafka:<topic>:<func> for consumers, func:<qualified name> otherwise.
// Colliding IDs get #2, #3... suffixes in source-position order.
func assignIDs(p *loader.Program, eps []Entrypoint) {
	sort.Slice(eps, func(i, j int) bool { return eps[i].Pos < eps[j].Pos })
	used := map[string]int{}
	for i := range eps {
		ep := &eps[i]
		var id string
		switch {
		case ep.Kind == "http" && ep.Path != "":
			method := ep.Method
			if method == "" {
				method = "ANY"
			}
			id = fmt.Sprintf("http:%s:%s", method, ep.Path)
		case ep.Kind == "grpc" && ep.Path != "":
			id = "grpc:" + ep.Path
		case ep.Kind == "kafka":
			topic := ep.Topic
			if topic == "" {
				topic = "?"
			}
			id = fmt.Sprintf("kafka:%s:%s", topic, ssax.TrimModule(ep.FuncName(), p.Module))
		default:
			id = "func:" + ep.FuncName()
		}
		used[id]++
		if n := used[id]; n > 1 {
			id = fmt.Sprintf("%s#%d", id, n)
		}
		ep.ID = id
	}
}

// normalizeID rewrites the path of an http:METHOD:/path id into the parameter
// syntax discovery emits, so an id written the way its own router spells
// route parameters ("/orders/:id") still resolves.
func normalizeID(id string) string {
	rest, ok := strings.CutPrefix(id, "http:")
	if !ok {
		return id
	}
	method, path, ok := strings.Cut(rest, ":")
	if !ok {
		return id
	}
	// assignIDs appends "#2", "#3"... to colliding ids; that suffix is not
	// part of the route and must not be normalized with it.
	suffix := ""
	if i := strings.LastIndex(path, "#"); i >= 0 {
		path, suffix = path[:i], path[i:]
	}
	return "http:" + method + ":" + normalizeRoutePath(path) + suffix
}

// Resolve finds the entrypoint for a user-supplied --entrypoint value:
// either a discovered ID or func:<qualified-name> (suffix match allowed) for
// functions discovery missed.
func Resolve(p *loader.Program, discovered []Entrypoint, id string) (*Entrypoint, error) {
	normalized := normalizeID(id)
	for i := range discovered {
		if discovered[i].ID == id || discovered[i].ID == normalized {
			return &discovered[i], nil
		}
	}
	name := strings.TrimPrefix(id, "func:")
	var candidates []*ssa.Function
	for fn := range ssautil.AllFunctions(p.Prog) {
		if !p.InModule(fn) || fn.Blocks == nil {
			continue
		}
		// A generic function appears once per instantiation, and every
		// instance displays as its origin — counting them all would report a
		// generic handler as ambiguous with itself.
		if orig := fn.Origin(); orig != nil && orig != fn {
			continue
		}
		full := ssax.FuncDisplayName(fn)
		if full == name || strings.HasSuffix(full, name) || ssax.TrimModule(full, p.Module) == name {
			candidates = append(candidates, fn)
		}
	}
	switch len(candidates) {
	case 0:
		return nil, fmt.Errorf("no entrypoint or function matches %q (try func:<pkg>.<Name>, see 'scan' listing for discovered ids)", id)
	case 1:
		fn := candidates[0]
		return &Entrypoint{ID: "func:" + ssax.FuncDisplayName(fn), Kind: "func", Fn: fn, Pos: fn.Pos()}, nil
	default:
		names := make([]string, 0, len(candidates))
		for _, fn := range candidates {
			names = append(names, "func:"+ssax.FuncDisplayName(fn))
		}
		sort.Strings(names)
		return nil, fmt.Errorf("%q is ambiguous; candidates:\n  %s", id, strings.Join(names, "\n  "))
	}
}
