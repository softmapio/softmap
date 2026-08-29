package entrypoints

import (
	"go/types"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/softmapio/softmap/internal/loader"
	"github.com/softmapio/softmap/internal/ssax"
)

var httpVerbs = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "DELETE": true, "PATCH": true,
	"HEAD": true, "OPTIONS": true, "TRACE": true, "CONNECT": true,
}

// handlerFunc resolves a handler-typed argument to its declared function,
// unwrapping conversions, closures, and synthetic bound-method wrappers
// (h.CreateOrder passed as a value).
func handlerFunc(p *loader.Program, v ssa.Value) *ssa.Function {
	return ssax.Declared(p.Prog, ssax.ResolveFuncValue(v))
}

func httpEntrypoint(method, path string, fn *ssa.Function) []Entrypoint {
	if fn == nil {
		return nil
	}
	return []Entrypoint{{Kind: "http", Method: method, Path: path, Fn: fn, Pos: fn.Pos()}}
}

// normalizeRoutePath rewrites the colon-and-star parameter syntax into the
// brace form, so the same endpoint gets the same ID whoever registered it:
// gin, echo and fiber write ":id" where chi, gorilla and net/http already
// write "{id}". Wildcard markers stay inside the braces ("*" -> "{*}", gin's
// "*path" -> "{*path}") because they mean something different from a
// single-segment parameter.
//
// Only the matchers of colon-syntax routers may call this. To net/http, chi
// and gorilla a segment starting with ":" or "*" is an ordinary LITERAL, and
// rewriting it there would rename a real route - and could collide it with
// the router's own "{id}" route.
func normalizeRoutePath(path string) string {
	if !strings.ContainsAny(path, ":*+") {
		return path
	}
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		if seg == "" || seg[0] == '{' {
			continue
		}
		switch seg[0] {
		case ':':
			segs[i] = "{" + seg[1:] + "}"
		case '*', '+':
			segs[i] = "{" + seg + "}"
		}
	}
	return strings.Join(segs, "/")
}

// matchNetHTTP handles http.Handle(Func) and (*http.ServeMux).Handle(Func),
// including Go 1.22 "METHOD /path" patterns.
func matchNetHTTP(p *loader.Program, _ *ssa.Function, site ssa.CallInstruction, info *ssax.CalleeInfo) []Entrypoint {
	if info.Pkg != "net/http" || (info.Name != "Handle" && info.Name != "HandleFunc") {
		return nil
	}
	if info.Type != "" && info.Type != "ServeMux" {
		return nil
	}
	args := site.Common().Args[info.ArgOffset:]
	if len(args) < 2 {
		return nil
	}
	pattern, _ := ssax.ConstString(args[0])
	method, path := "", pattern
	if verb, rest, ok := strings.Cut(pattern, " "); ok && httpVerbs[verb] {
		method, path = verb, strings.TrimSpace(rest)
	}
	fn := handlerFunc(p, args[1])
	if fn == nil {
		fn = ssax.ConcreteMethod(p.Prog, args[1], "ServeHTTP")
	}
	return httpEntrypoint(method, path, fn)
}

// matchGin handles (*gin.Engine)/(*gin.RouterGroup) verb methods and Handle.
func matchGin(p *loader.Program, _ *ssa.Function, site ssa.CallInstruction, info *ssax.CalleeInfo) []Entrypoint {
	if info.Pkg != "github.com/gin-gonic/gin" || (info.Type != "Engine" && info.Type != "RouterGroup") {
		return nil
	}
	args := site.Common().Args[info.ArgOffset:]
	method := info.Name
	switch {
	case httpVerbs[method] || method == "Any":
		if method == "Any" {
			method = ""
		}
	case method == "Handle":
		if len(args) < 1 {
			return nil
		}
		method, _ = ssax.ConstString(args[0])
		args = args[1:]
	default:
		return nil
	}
	if len(args) < 2 {
		return nil
	}
	path, _ := ssax.ConstString(args[0])
	handlers := ssax.VarargValues(args[1])
	if len(handlers) == 0 {
		return nil
	}
	// The final handler is the endpoint; earlier ones are middleware.
	return httpEntrypoint(method, normalizeRoutePath(path), handlerFunc(p, handlers[len(handlers)-1]))
}

// matchEcho handles (*echo.Echo)/(*echo.Group) verb methods.
func matchEcho(p *loader.Program, _ *ssa.Function, site ssa.CallInstruction, info *ssax.CalleeInfo) []Entrypoint {
	if ssax.NormalizePkg(info.Pkg) != "github.com/labstack/echo" || (info.Type != "Echo" && info.Type != "Group") {
		return nil
	}
	method := info.Name
	if method == "Any" {
		method = ""
	} else if !httpVerbs[method] {
		return nil
	}
	args := site.Common().Args[info.ArgOffset:]
	if len(args) < 2 {
		return nil
	}
	path, _ := ssax.ConstString(args[0])
	return httpEntrypoint(method, normalizeRoutePath(path), handlerFunc(p, args[1]))
}

// matchChi handles chi.Mux / chi.Router registrations (both the concrete
// struct and interface-dispatched calls).
func matchChi(p *loader.Program, encl *ssa.Function, site ssa.CallInstruction, info *ssax.CalleeInfo) []Entrypoint {
	if ssax.NormalizePkg(info.Pkg) != "github.com/go-chi/chi" || (info.Type != "Mux" && info.Type != "Router") {
		return nil
	}
	args := site.Common().Args[info.ArgOffset:]
	method := ""
	switch name := info.Name; {
	case httpVerbs[strings.ToUpper(name)] && name != strings.ToUpper(name):
		method = strings.ToUpper(name) // Get, Post, ... (chi capitalization)
	case name == "Handle" || name == "HandleFunc":
	case name == "Method" || name == "MethodFunc":
		if len(args) < 1 {
			return nil
		}
		method, _ = ssax.ConstString(args[0])
		args = args[1:]
	default:
		return nil
	}
	if len(args) < 2 {
		return nil
	}
	path, _ := ssax.ConstString(args[0])
	// Registrations inside r.Route("/auth", func(r chi.Router){...}) carry
	// the group prefix: the endpoint is /auth/login, not /login.
	path = chiGroupPrefix(encl) + path
	fn := handlerFunc(p, args[1])
	if fn == nil {
		fn = ssax.ConcreteMethod(p.Prog, args[1], "ServeHTTP")
	}
	return httpEntrypoint(method, path, fn)
}

var chiSubrouters = map[string]bool{"Route": true, "Group": true}

// chiGroupPrefix walks outward from a route-registration closure: if the
// enclosing anonymous function is the callback of a chi Route(pattern, fn)
// (directly or through a MakeClosure), the pattern prepends - recursively,
// so nested groups compose ("/api" + "/auth").
func chiGroupPrefix(fn *ssa.Function) string {
	prefix := ""
	cur := fn
	for hops := 0; hops < 8 && cur != nil && cur.Parent() != nil; hops++ {
		call, pat := callbackRegistrar(cur, "github.com/go-chi/chi", chiSubrouters)
		if call == nil {
			break
		}
		prefix = pat + prefix
		cur = call.Parent()
	}
	return prefix
}

// callbackRegistrar finds the subrouter call in pkg (one of the names in
// want) that receives closure as its callback, and returns it with its
// constant prefix argument - "" when the call takes no prefix, as in chi's
// Group(fn). Both chi's Route/Group and fiber's Route register a subtree this
// way, either passing the function directly or through a MakeClosure.
func callbackRegistrar(closure *ssa.Function, pkg string, want map[string]bool) (ssa.CallInstruction, string) {
	var found ssa.CallInstruction
	var prefix string
	for _, instr := range referrersOf(closure) {
		var call ssa.CallInstruction
		var pat string
		if mc, ok := instr.(*ssa.MakeClosure); ok {
			call, pat = registrarTaking(referrersOf(mc), mc, pkg, want)
		} else if _, ok := instr.(ssa.CallInstruction); ok {
			call, pat = registrarTaking([]ssa.Instruction{instr}, closure, pkg, want)
		}
		if call == nil || call == found {
			continue
		}
		if found != nil {
			// Mounted under two prefixes: claim neither, the way the declared
			// callback index does.
			return nil, ""
		}
		found, prefix = call, pat
	}
	return found, prefix
}

// registrarTaking returns the first call among refs that belongs to pkg, is
// named in want, and passes v as one of its arguments.
func registrarTaking(refs []ssa.Instruction, v ssa.Value, pkg string, want map[string]bool) (ssa.CallInstruction, string) {
	for _, instr := range refs {
		call, ok := instr.(ssa.CallInstruction)
		if !ok {
			continue
		}
		info := ssax.Callee(call)
		if info == nil || ssax.NormalizePkg(info.Pkg) != pkg || !want[info.Name] {
			continue
		}
		args := call.Common().Args[info.ArgOffset:]
		takes := false
		for _, a := range args {
			if a == v {
				takes = true
				break
			}
		}
		if !takes {
			continue
		}
		if len(args) > 0 && args[0] != v {
			pat, _ := ssax.ConstString(args[0])
			return call, pat
		}
		return call, ""
	}
	return nil, ""
}

func referrersOf(v ssa.Value) []ssa.Instruction {
	if refs := v.Referrers(); refs != nil {
		return *refs
	}
	return nil
}

// matchGorilla handles (*mux.Router).Handle(Func); the HTTP method is pulled
// from a chained .Methods(...) call on the returned *Route when present.
func matchGorilla(p *loader.Program, _ *ssa.Function, site ssa.CallInstruction, info *ssax.CalleeInfo) []Entrypoint {
	if info.Pkg != "github.com/gorilla/mux" || info.Type != "Router" {
		return nil
	}
	if info.Name != "Handle" && info.Name != "HandleFunc" {
		return nil
	}
	args := site.Common().Args[info.ArgOffset:]
	if len(args) < 2 {
		return nil
	}
	path, _ := ssax.ConstString(args[0])
	fn := handlerFunc(p, args[1])
	if fn == nil {
		fn = ssax.ConcreteMethod(p.Prog, args[1], "ServeHTTP")
	}
	method := ""
	if call, ok := site.(*ssa.Call); ok {
		method = gorillaChainedMethods(call)
	}
	return httpEntrypoint(method, path, fn)
}

func gorillaChainedMethods(route ssa.Value) string {
	refs := route.Referrers()
	if refs == nil {
		return ""
	}
	for _, instr := range *refs {
		call, ok := instr.(*ssa.Call)
		if !ok {
			continue
		}
		ci := ssax.Callee(call)
		if ci == nil || ci.Pkg != "github.com/gorilla/mux" || ci.Name != "Methods" {
			continue
		}
		if methods, ok := ssax.SliceStrings(call.Common().Args[ci.ArgOffset]); ok {
			return strings.Join(methods, ",")
		}
	}
	return ""
}

// fiberPkg is the gofiber import path with its major-version suffix stripped,
// so one matcher covers every major.
const fiberPkg = "github.com/gofiber/fiber"

// fiberSubrouters are the fiber calls that register a subtree through a
// callback; Group hands back a value instead and is followed by fiberPrefix.
var fiberSubrouters = map[string]bool{"Route": true}

// matchFiber handles gofiber registrations on *App, *Group and the Router
// interface. The supported majors differ in argument shape: v2 takes a
// variadic handler list whose LAST element is the endpoint (earlier ones are
// middleware), while v3 names the endpoint FIRST and takes middleware after
// it, and its Add takes a list of methods rather than one.
func matchFiber(p *loader.Program, _ *ssa.Function, site ssa.CallInstruction, info *ssax.CalleeInfo) []Entrypoint {
	if ssax.NormalizePkg(info.Pkg) != fiberPkg {
		return nil
	}
	switch info.Type {
	case "App", "Group", "Router":
	default:
		return nil
	}
	args := site.Common().Args[info.ArgOffset:]
	method := ""
	switch name := info.Name; {
	case name == "Add":
		if len(args) < 1 {
			return nil
		}
		if m, ok := ssax.ConstString(args[0]); ok {
			method = m // v2: one method
		} else if ms, ok := ssax.SliceStrings(args[0]); ok {
			method = strings.Join(ms, ",") // v3: several at once
		}
		// A method that does not resolve to a constant degrades to ANY, the
		// way an unknown method does everywhere else - the route is still
		// real, only the verb is unknown.
		args = args[1:]
	case name == "All":
		// Every method; the ID says ANY.
	case httpVerbs[strings.ToUpper(name)] && name != strings.ToUpper(name):
		method = strings.ToUpper(name) // Get, Post, ... (fiber capitalization)
	default:
		return nil // Group, Route, Use, Static, Listen, ...
	}
	if len(args) < 2 {
		return nil
	}
	// A path that does not resolve to a constant leaves Path empty so the ID
	// falls back to func:<name>. Prepending the group prefix to nothing would
	// report the group itself as the route, which is confidently wrong.
	path, ok := ssax.ConstString(args[0])
	if ok {
		path = normalizeRoutePath(fiberPrefix(receiverOf(site, info), 0) + path)
	}
	return httpEntrypoint(method, path, fiberHandler(p, info.Pkg, args[1:]))
}

// receiverOf returns the value a method call was made on: the interface value
// for dynamic dispatch, the first argument for a static method call.
func receiverOf(site ssa.CallInstruction, info *ssax.CalleeInfo) ssa.Value {
	common := site.Common()
	if common.IsInvoke() {
		return common.Value
	}
	if info.ArgOffset > 0 && len(common.Args) > 0 {
		return common.Args[0]
	}
	return nil
}

// fiberPrefix composes the path prefix a registration inherits. Fiber groups
// are values (api := app.Group("/api")), so the router a route was registered
// on is chased back through the Group calls that produced it - recursively,
// so nested groups compose. Route(prefix, func(r fiber.Router){...}) hands its
// router in as a callback parameter instead, which is followed outward to the
// Route call and then chased the same way.
func fiberPrefix(v ssa.Value, depth int) string {
	for depth < 8 && v != nil {
		switch r := v.(type) {
		case *ssa.MakeInterface:
			v = r.X
		case *ssa.ChangeInterface:
			v = r.X
		case *ssa.UnOp:
			// A router held in a local, or in a struct field a constructor
			// assigned once (s.api = app.Group("/api")).
			if stored := ssax.SingleStore(r.X); stored != nil {
				v = stored
				break
			}
			if fa, ok := r.X.(*ssa.FieldAddr); ok {
				v = ssax.FieldStore(fa)
				break
			}
			return ""
		case *ssa.Call:
			info := ssax.Callee(r)
			if info == nil || ssax.NormalizePkg(info.Pkg) != fiberPkg {
				return ""
			}
			if info.Name != "Group" && info.Name != "Route" {
				// Verbs, Add, All and Use hand back the router they were
				// called on, so a chained registration keeps its prefix.
				v = receiverOf(r, info)
				break
			}
			args := r.Common().Args[info.ArgOffset:]
			if len(args) == 0 {
				return ""
			}
			prefix, _ := ssax.ConstString(args[0])
			return fiberPrefix(receiverOf(r, info), depth+1) + prefix
		case *ssa.Parameter:
			owner := r.Parent()
			if call, prefix := fiberRegistrarOf(owner); call != nil {
				info := ssax.Callee(call)
				if info == nil {
					return prefix
				}
				return fiberPrefix(receiverOf(call, info), depth+1) + prefix
			}
			// Not a Route callback: the router was handed in as an ordinary
			// argument (setupRoutes(app.Group("/api")), or a constructor
			// storing it in a field). Follow the argument, but only from a
			// single call site - two callers could pass different groups,
			// and picking one would invent a prefix.
			callers := callersOf(owner)
			if len(callers) != 1 {
				return ""
			}
			i, args := paramIndex(owner, r), callers[0].Common().Args
			if i < 0 || i >= len(args) {
				return ""
			}
			return fiberPrefix(args[i], depth+1)
		default:
			return ""
		}
		depth++
	}
	return ""
}

// fiberRegistrarOf finds the Route call that passed fn in as a subrouter
// callback. An anonymous function records its own referrers; a declared
// function or method does not (ssa.Function.Referrers is nil unless the
// function is anonymous), so app.Route("/admin", adminRoutes) has to be found
// by scanning the program instead - memoized, since a repo can register many
// subtrees that way.
func fiberRegistrarOf(fn *ssa.Function) (ssa.CallInstruction, string) {
	if fn == nil {
		return nil, ""
	}
	if call, prefix := callbackRegistrar(fn, fiberPkg, fiberSubrouters); call != nil {
		return call, prefix
	}
	if fn.Prog == nil {
		return nil, ""
	}
	if site, ok := fiberRouteIndex(fn.Prog)[fn]; ok && site.call != nil {
		return site.call, site.prefix
	}
	return nil, ""
}

// registrarSite is the call that registered one subrouter callback.
type registrarSite struct {
	call   ssa.CallInstruction
	prefix string
}

// paramIndex is the position of p in its function's parameter list, matching
// the argument positions of a static call to it.
func paramIndex(fn *ssa.Function, p *ssa.Parameter) int {
	for i, param := range fn.Params {
		if param == p {
			return i
		}
	}
	return -1
}

var callerCache sync.Map // *ssa.Program -> map[*ssa.Function][]ssa.CallInstruction

// callersOf returns the statically resolved calls to fn. Like the route
// index it is built once per program, and only ever reached from fiber's
// prefix chase, so repos without fiber never pay for it.
func callersOf(fn *ssa.Function) []ssa.CallInstruction {
	if fn == nil || fn.Prog == nil {
		return nil
	}
	idx, ok := callerCache.Load(fn.Prog)
	if !ok {
		built := map[*ssa.Function][]ssa.CallInstruction{}
		for f := range ssautil.AllFunctions(fn.Prog) {
			for _, b := range f.Blocks {
				for _, instr := range b.Instrs {
					call, ok := instr.(ssa.CallInstruction)
					if !ok {
						continue
					}
					if callee := call.Common().StaticCallee(); callee != nil {
						built[callee] = append(built[callee], call)
					}
				}
			}
		}
		idx, _ = callerCache.LoadOrStore(fn.Prog, built)
	}
	return idx.(map[*ssa.Function][]ssa.CallInstruction)[fn]
}

var fiberRouteCache sync.Map // *ssa.Program -> map[*ssa.Function]registrarSite

// fiberRouteIndex maps every function used as a fiber Route callback to the
// call that registered it. Built once per program: the alternative is one
// full scan per lookup.
func fiberRouteIndex(prog *ssa.Program) map[*ssa.Function]registrarSite {
	if v, ok := fiberRouteCache.Load(prog); ok {
		return v.(map[*ssa.Function]registrarSite)
	}
	idx := map[*ssa.Function]registrarSite{}
	for fn := range ssautil.AllFunctions(prog) {
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				call, ok := instr.(ssa.CallInstruction)
				if !ok {
					continue
				}
				info := ssax.Callee(call)
				if info == nil || ssax.NormalizePkg(info.Pkg) != fiberPkg || !fiberSubrouters[info.Name] {
					continue
				}
				args := call.Common().Args[info.ArgOffset:]
				if len(args) < 2 {
					continue
				}
				// Declared resolves a method value (s.routes) through its
				// synthetic bound wrapper back to the declared method.
				cb := ssax.Declared(prog, ssax.ResolveFuncValue(args[1]))
				if cb == nil {
					continue
				}
				if _, seen := idx[cb]; seen {
					// Mounted under two prefixes: there is no single prefix to
					// claim, and picking one would depend on iteration order.
					idx[cb] = registrarSite{}
					continue
				}
				prefix, _ := ssax.ConstString(args[0])
				idx[cb] = registrarSite{call: call, prefix: prefix}
			}
		}
	}
	fiberRouteCache.Store(prog, idx)
	return idx
}

// fiberHandler picks the endpoint out of a registration's handler arguments,
// expanding the variadic ones: v2 lists middleware first and the endpoint
// last, v3 names the endpoint first and takes middleware after it.
func fiberHandler(p *loader.Program, pkg string, args []ssa.Value) *ssa.Function {
	var candidates []ssa.Value
	for _, a := range args {
		if vals := ssax.VarargValues(a); len(vals) > 0 {
			candidates = append(candidates, vals...)
			continue
		}
		candidates = append(candidates, a)
	}
	if len(candidates) == 0 {
		return nil
	}
	// The pick is positional, not "first that resolves": scanning past an
	// endpoint softmap cannot resolve would silently report its middleware as
	// the entrypoint. An unresolvable endpoint is a miss, which discovery
	// reports honestly.
	if fiberMajor(pkg) >= 3 {
		return handlerFunc(p, candidates[0])
	}
	return handlerFunc(p, candidates[len(candidates)-1])
}

// fiberMajor reads the major version out of a gofiber import path; the
// original unversioned path has none.
func fiberMajor(pkg string) int {
	i := strings.LastIndex(pkg, "/v")
	if i < 0 {
		return 1
	}
	n, err := strconv.Atoi(pkg[i+2:])
	if err != nil {
		return 1
	}
	return n
}

// matchGRPCRegister keys on the protoc-generated registration call every
// gRPC codebase makes: pb.Register<Service>Server(registrar, impl). The
// generated Register function's second parameter is the service interface;
// each of its exported methods resolved on the concrete impl becomes an
// entrypoint. (The Register function itself lives in a generated file -
// that's fine, the call site is handwritten wiring code.)
func matchGRPCRegister(p *loader.Program, _ *ssa.Function, site ssa.CallInstruction, info *ssax.CalleeInfo) []Entrypoint {
	if info.Static == nil || info.Type != "" ||
		!strings.HasPrefix(info.Name, "Register") || !strings.HasSuffix(info.Name, "Server") {
		return nil
	}
	sig := info.Static.Signature
	args := site.Common().Args[info.ArgOffset:]
	if sig.Params().Len() != 2 || len(args) != 2 {
		return nil
	}
	iface, ok := sig.Params().At(1).Type().Underlying().(*types.Interface)
	if !ok {
		return nil
	}
	service := strings.TrimSuffix(strings.TrimPrefix(info.Name, "Register"), "Server")
	var eps []Entrypoint
	for i := 0; i < iface.NumMethods(); i++ {
		m := iface.Method(i)
		if !m.Exported() {
			continue // mustEmbedUnimplemented* and friends
		}
		fn := ssax.ConcreteMethod(p.Prog, args[1], m.Name())
		if fn == nil {
			continue
		}
		eps = append(eps, Entrypoint{Kind: "grpc", Method: m.Name(), Path: service + "/" + m.Name(), Fn: fn, Pos: fn.Pos()})
	}
	return eps
}

// matchSaramaConsumer: group.Consume(ctx, topics, handler) makes the
// handler's ConsumeClaim method the entrypoint.
func matchSaramaConsumer(p *loader.Program, _ *ssa.Function, site ssa.CallInstruction, info *ssax.CalleeInfo) []Entrypoint {
	if info.Name != "Consume" || info.Type != "ConsumerGroup" {
		return nil
	}
	if info.Pkg != "github.com/IBM/sarama" && info.Pkg != "github.com/Shopify/sarama" {
		return nil
	}
	args := site.Common().Args[info.ArgOffset:]
	if len(args) < 3 {
		return nil
	}
	topic := ""
	if topics, ok := ssax.SliceStrings(args[1]); ok {
		topic = strings.Join(topics, ",")
	}
	fn := ssax.ConcreteMethod(p.Prog, args[2], "ConsumeClaim")
	if fn == nil {
		return nil
	}
	return []Entrypoint{{Kind: "kafka", Topic: topic, Fn: fn, Pos: fn.Pos()}}
}

// matchKafkaGoConsumer: a function calling (*kafka.Reader).ReadMessage or
// FetchMessage is itself the consumer entrypoint.
func matchKafkaGoConsumer(p *loader.Program, enclosing *ssa.Function, site ssa.CallInstruction, info *ssax.CalleeInfo) []Entrypoint {
	if info.Pkg != "github.com/segmentio/kafka-go" || info.Type != "Reader" {
		return nil
	}
	if info.Name != "ReadMessage" && info.Name != "FetchMessage" {
		return nil
	}
	topic := kafkaGoReaderTopic(site.Common().Args[0])
	return []Entrypoint{{Kind: "kafka", Topic: topic, Fn: enclosing, Pos: enclosing.Pos()}}
}

// kafkaGoReaderTopic chases a *kafka.Reader back to its NewReader(config)
// call and resolves config.Topic when constant.
func kafkaGoReaderTopic(reader ssa.Value) string {
	for depth := 0; depth < 5; depth++ {
		switch v := reader.(type) {
		case *ssa.Call:
			ci := ssax.Callee(v)
			if ci == nil || ci.Pkg != "github.com/segmentio/kafka-go" || ci.Name != "NewReader" {
				return ""
			}
			if topic, ok := ssax.StructFieldString(v.Common().Args[ci.ArgOffset], "Topic"); ok {
				return topic
			}
			return ""
		case *ssa.UnOp:
			stored := ssax.SingleStore(v.X)
			if stored == nil {
				return ""
			}
			reader = stored
		default:
			return ""
		}
	}
	return ""
}

// matchConfluentConsumer: a function calling (*kafka.Consumer).ReadMessage
// or Poll is itself the consumer entrypoint.
func matchConfluentConsumer(p *loader.Program, enclosing *ssa.Function, site ssa.CallInstruction, info *ssax.CalleeInfo) []Entrypoint {
	if !strings.HasPrefix(info.Pkg, "github.com/confluentinc/confluent-kafka-go") || info.Type != "Consumer" {
		return nil
	}
	if info.Name != "ReadMessage" && info.Name != "Poll" {
		return nil
	}
	return []Entrypoint{{Kind: "kafka", Fn: enclosing, Pos: enclosing.Pos()}}
}
