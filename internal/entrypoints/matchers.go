package entrypoints

import (
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"

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
	return httpEntrypoint(method, path, handlerFunc(p, handlers[len(handlers)-1]))
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
	return httpEntrypoint(method, path, handlerFunc(p, args[1]))
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

// chiGroupPrefix walks outward from a route-registration closure: if the
// enclosing anonymous function is the callback of a chi Route(pattern, fn)
// (directly or through a MakeClosure), the pattern prepends — recursively,
// so nested groups compose ("/api" + "/auth").
func chiGroupPrefix(fn *ssa.Function) string {
	prefix := ""
	cur := fn
	for hops := 0; hops < 8 && cur != nil && cur.Parent() != nil; hops++ {
		call, pat := chiRouteCallOf(cur)
		if call == nil {
			break
		}
		prefix = pat + prefix
		cur = call.Parent()
	}
	return prefix
}

// chiRouteCallOf finds the chi Route/Group call that receives closure as its
// callback and returns it with the Route pattern ("" for Group).
func chiRouteCallOf(closure *ssa.Function) (ssa.CallInstruction, string) {
	refs := closure.Referrers()
	if refs == nil {
		return nil, ""
	}
	for _, instr := range *refs {
		if mc, ok := instr.(*ssa.MakeClosure); ok {
			if call, pat := chiCallTaking(mc.Referrers(), mc); call != nil {
				return call, pat
			}
			continue
		}
		if call, ok := instr.(ssa.CallInstruction); ok {
			if c, pat := chiCallTaking(&[]ssa.Instruction{call.(ssa.Instruction)}, closure); c != nil {
				return c, pat
			}
		}
	}
	return nil, ""
}

func chiCallTaking(refs *[]ssa.Instruction, v ssa.Value) (ssa.CallInstruction, string) {
	if refs == nil {
		return nil, ""
	}
	for _, instr := range *refs {
		call, ok := instr.(ssa.CallInstruction)
		if !ok {
			continue
		}
		info := ssax.Callee(call)
		if info == nil || ssax.NormalizePkg(info.Pkg) != "github.com/go-chi/chi" {
			continue
		}
		args := call.Common().Args[info.ArgOffset:]
		switch info.Name {
		case "Route":
			if len(args) == 2 && args[1] == v {
				pat, _ := ssax.ConstString(args[0])
				return call, pat
			}
		case "Group":
			if len(args) == 1 && args[0] == v {
				return call, ""
			}
		}
	}
	return nil, ""
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

// matchGRPCRegister keys on the protoc-generated registration call every
// gRPC codebase makes: pb.Register<Service>Server(registrar, impl). The
// generated Register function's second parameter is the service interface;
// each of its exported methods resolved on the concrete impl becomes an
// entrypoint. (The Register function itself lives in a generated file —
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
