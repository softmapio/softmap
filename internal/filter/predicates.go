package filter

import (
	"go/types"
	"path"
	"strings"

	"golang.org/x/tools/go/ssa"

	"github.com/softmapio/softmap/internal/graph"
	"github.com/softmapio/softmap/internal/loader"
)

// Predicate is a named heuristic referenced from rule files. Every predicate
// is individually unit-tested; add new ones here and they become available
// to user rules automatically.
type Predicate func(n *graph.Node, p *loader.Program) bool

var predicates = map[string]Predicate{
	"logger-method":       IsLoggerMethod,
	"logger-package":      IsLoggerPackage,
	"logger-factory":      IsLoggerFactory,
	"metrics-tracing":     IsMetricsTracing,
	"config-reader":       IsConfigReader,
	"validation-helper":   IsValidationHelper,
	"trivial-wrapper":     IsTrivialWrapper,
	"respond-helper":      IsRespondHelper,
	"dto-mapper":          IsDTOMapper,
	"inline-closure":      IsInlineClosure,
	"trivial-constructor": IsTrivialConstructor,
	"getter":              IsGetter,
	"error-wrapper":       IsErrorWrapper,
}

var logLevelNames = map[string]bool{
	"Debug": true, "Debugf": true, "Debugw": true, "Debugln": true,
	"Info": true, "Infof": true, "Infow": true, "Infoln": true,
	"Warn": true, "Warnf": true, "Warnw": true, "Warnln": true, "Warning": true, "Warningf": true,
	"Error": true, "Errorf": true, "Errorw": true, "Errorln": true,
	"Trace": true, "Tracef": true,
	"Fatal": true, "Fatalf": true, "Fatalln": true,
	"Panic": true, "Panicf": true, "Panicln": true,
	"Print": true, "Printf": true, "Println": true, "Log": true, "Logf": true,
}

var loggerPkgBases = map[string]bool{
	"log": true, "slog": true, "zap": true, "logrus": true, "zerolog": true,
	"logger": true, "logging": true, "logx": true,
}

// IsLoggerMethod: a level-named method on a logger-shaped receiver or in a
// logging package. Both signals are required so that e.g. Store.Delete or a
// domain type's Error() survive.
func IsLoggerMethod(n *graph.Node, _ *loader.Program) bool {
	if n.Fn == nil || !logLevelNames[n.Fn.Name()] {
		return false
	}
	if base := path.Base(n.Pkg); loggerPkgBases[base] {
		return true
	}
	recv := receiverTypeName(n.Fn)
	return strings.Contains(strings.ToLower(recv), "log")
}

// IsLoggerPackage: everything in a logging-named package - constructors and
// helpers included, not just level methods. Anything effectful a custom
// logger does is immune anyway.
func IsLoggerPackage(n *graph.Node, _ *loader.Program) bool {
	return loggerPkgBases[path.Base(n.Pkg)]
}

// IsLoggerFactory: a function that returns a logger (scopedLogger,
// withTraceID, ...) is logging plumbing no matter what it is named or where
// it lives - the return type is the reliable signal.
func IsLoggerFactory(n *graph.Node, _ *loader.Program) bool {
	if n.Fn == nil {
		return false
	}
	results := n.Fn.Signature.Results()
	for i := 0; i < results.Len(); i++ {
		t := types.Unalias(results.At(i).Type())
		if ptr, ok := t.(*types.Pointer); ok {
			t = types.Unalias(ptr.Elem())
		}
		named, ok := t.(*types.Named)
		if !ok || named.Obj().Pkg() == nil {
			continue
		}
		if loggerPkgBases[path.Base(named.Obj().Pkg().Path())] {
			return true
		}
	}
	return false
}

var metricsPkgBases = map[string]bool{
	"metrics": true, "metric": true, "telemetry": true, "tracing": true,
	"trace": true, "stats": true, "monitoring": true, "prometheus": true,
	"instrumentation": true, "otel": true,
}

var metricsPkgPrefixes = []string{
	"github.com/prometheus/",
	"go.opentelemetry.io/",
	"go.opencensus.io/",
	"contrib.go.opencensus.io/",
}

var telemetryRecvParts = []string{"timing", "metric", "telemetr", "tracer", "stopwatch", "profil"}

// IsMetricsTracing: functions in metrics/tracing packages (by import path
// for known ecosystems, by package base name for module-local ones), and
// methods of telemetry-shaped receivers wherever they live
// (scoringTimings.flush in a service package is still telemetry).
func IsMetricsTracing(n *graph.Node, _ *loader.Program) bool {
	for _, prefix := range metricsPkgPrefixes {
		if strings.HasPrefix(n.Pkg, prefix) {
			return true
		}
	}
	if metricsPkgBases[path.Base(n.Pkg)] {
		return true
	}
	if n.Fn != nil {
		// Closures inherit their parent's receiver (track$1 inside
		// scoringTimings.track is still telemetry).
		for fn := n.Fn; fn != nil; fn = fn.Parent() {
			recv := strings.ToLower(receiverTypeName(fn))
			for _, part := range telemetryRecvParts {
				if strings.Contains(recv, part) {
					return true
				}
			}
		}
	}
	return false
}

var configPkgBases = map[string]bool{
	"config": true, "configs": true, "conf": true, "cfg": true,
	"settings": true, "env": true, "flags": true,
}

// IsConfigReader: functions living in configuration packages.
func IsConfigReader(n *graph.Node, _ *loader.Program) bool {
	return configPkgBases[path.Base(n.Pkg)]
}

var validationPrefixes = []string{"validate", "check", "verify", "ensure", "assert", "isvalid", "iserr"}

// IsValidationHelper: validation-named functions returning only error or
// bool. Validators that reach real effects are rescued by keep-protection.
func IsValidationHelper(n *graph.Node, _ *loader.Program) bool {
	if n.Fn == nil {
		return false
	}
	name := strings.ToLower(n.Fn.Name())
	matched := false
	for _, prefix := range validationPrefixes {
		if strings.HasPrefix(name, prefix) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	results := n.Fn.Signature.Results()
	if results.Len() != 1 {
		return false
	}
	t := results.At(0).Type()
	return isErrorType(t) || isBoolType(t)
}

// IsTrivialWrapper: a single-block body whose only call is the one it
// forwards to - glue with no logic of its own. Collapsing it keeps the
// callee, so nothing meaningful is lost.
func IsTrivialWrapper(n *graph.Node, _ *loader.Program) bool {
	if n.Fn == nil || len(n.Fn.Blocks) != 1 {
		return false
	}
	calls := 0
	for _, instr := range n.Fn.Blocks[0].Instrs {
		switch instr.(type) {
		case *ssa.Call, *ssa.Go, *ssa.Defer:
			calls++
		case *ssa.If, *ssa.Jump, *ssa.Panic:
			return false
		}
	}
	return calls == 1
}

// IsDTOMapper: a pure shape-shifter between layers - ToX/FromX/MapX and
// the *FromModel/*ToResponse family. Collapsing keeps any callee; the
// caller shows "+N mapping" instead of a step that does no business work.
func IsDTOMapper(n *graph.Node, _ *loader.Program) bool {
	if n.Fn == nil {
		return false
	}
	name := n.Fn.Name()
	if !mapperName(name) {
		return false
	}
	results := n.Fn.Signature.Results()
	if results.Len() == 0 {
		return false
	}
	for i := 0; i < results.Len(); i++ {
		if !isErrorType(results.At(i).Type()) {
			return true // returns a value: a transform, not a procedure
		}
	}
	return false
}

func mapperName(name string) bool {
	for _, prefix := range []string{"To", "From", "Map", "to", "from"} {
		if strings.HasPrefix(name, prefix) && len(name) > len(prefix) &&
			name[len(prefix)] >= 'A' && name[len(prefix)] <= 'Z' {
			return true
		}
	}
	for _, suffix := range []string{"FromModel", "ToModel", "FromDTO", "ToDTO", "ToResponse", "FromRequest", "ToProto", "FromProto", "ToDoc", "ToEntity"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// IsRespondHelper: a hand-rolled HTTP respond helper -
// RespondWithJSON(w, code, payload) and friends. Once guards turn its call
// sites into exits and the success terminal, the helper itself is
// transport plumbing.
func IsRespondHelper(n *graph.Node, _ *loader.Program) bool {
	if n.Fn == nil {
		return false
	}
	lower := strings.ToLower(n.Fn.Name())
	if !strings.Contains(lower, "respond") && !strings.Contains(lower, "json") && !strings.Contains(lower, "write") {
		return false
	}
	params := n.Fn.Signature.Params()
	for i := 0; i < params.Len(); i++ {
		if named, ok := types.Unalias(params.At(i).Type()).(*types.Named); ok {
			if pkg := named.Obj().Pkg(); pkg != nil && pkg.Path() == "net/http" && named.Obj().Name() == "ResponseWriter" {
				return true
			}
		}
	}
	return false
}

// IsInlineClosure: an anonymous function (a `go func(){...}` body or an
// inline literal) that forwards to at most one module function - logging
// aside, the closure is syntax, the call inside is the story. Collapsing it
// keeps the callee, and when the closure was launched via `go` the spliced
// edge stays async, so `go func(){ s.GeneratePdf(...) }` reads as an async
// call of GeneratePdf instead of a nameless twin of the enclosing method.
func IsInlineClosure(n *graph.Node, p *loader.Program) bool {
	if n.Fn == nil || n.Fn.Parent() == nil {
		return false
	}
	moduleCalls := 0
	for _, b := range n.Fn.Blocks {
		for _, instr := range b.Instrs {
			site, ok := instr.(ssa.CallInstruction)
			if !ok {
				continue
			}
			common := site.Common()
			if common.IsInvoke() {
				// A dynamic call could be anything: keep the closure.
				return false
			}
			callee := common.StaticCallee()
			if callee == nil || callee.Pkg == nil || !p.InModule(callee) {
				continue
			}
			probe := &graph.Node{Fn: callee, Pkg: callee.Pkg.Pkg.Path()}
			if IsLoggerMethod(probe, p) || IsLoggerPackage(probe, p) || IsMetricsTracing(probe, p) {
				continue
			}
			moduleCalls++
		}
	}
	return moduleCalls <= 1
}

// IsTrivialConstructor: newX/NewX with a single block, zero calls and a
// tiny body - a struct literal dressed as a function (error-response DTOs
// and the like). Collapsing it keeps nothing hidden: it has no callees.
func IsTrivialConstructor(n *graph.Node, _ *loader.Program) bool {
	if n.Fn == nil || len(n.Fn.Blocks) != 1 {
		return false
	}
	name := n.Fn.Name()
	if !strings.HasPrefix(name, "new") && !strings.HasPrefix(name, "New") {
		return false
	}
	results := n.Fn.Signature.Results()
	if results.Len() == 0 || isErrorType(results.At(0).Type()) {
		return false
	}
	instrs := n.Fn.Blocks[0].Instrs
	if len(instrs) > 10 {
		return false
	}
	for _, instr := range instrs {
		switch instr.(type) {
		case *ssa.Call, *ssa.Go, *ssa.Defer:
			return false
		}
	}
	return true
}

var getterPrefixes = []string{"Get", "Is", "Has", "Len", "Name", "String", "ID"}

// IsGetter: a short, single-block, call-free method - field access dressed
// as a function. Collapsed (it has no callees, so this equals dropping it
// into the caller's collapsed list).
func IsGetter(n *graph.Node, _ *loader.Program) bool {
	if n.Fn == nil || n.Fn.Signature.Recv() == nil || len(n.Fn.Blocks) != 1 {
		return false
	}
	instrs := n.Fn.Blocks[0].Instrs
	if len(instrs) > 8 {
		return false
	}
	for _, instr := range instrs {
		switch instr.(type) {
		case *ssa.Call, *ssa.Go, *ssa.Defer:
			return false
		}
	}
	name := n.Fn.Name()
	for _, prefix := range getterPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

var errorHelperPkgs = map[string]bool{
	"fmt": true, "errors": true, "github.com/pkg/errors": true,
}

// IsErrorWrapper: returns exactly one error and only calls error-formatting
// helpers (fmt.Errorf, errors.*): message plumbing, not a step.
func IsErrorWrapper(n *graph.Node, _ *loader.Program) bool {
	if n.Fn == nil || len(n.Fn.Blocks) > 3 {
		return false
	}
	results := n.Fn.Signature.Results()
	if results.Len() != 1 || !isErrorType(results.At(0).Type()) {
		return false
	}
	calls := 0
	for _, b := range n.Fn.Blocks {
		for _, instr := range b.Instrs {
			site, ok := instr.(ssa.CallInstruction)
			if !ok {
				continue
			}
			callee := site.Common().StaticCallee()
			if callee == nil {
				return false
			}
			if !errorHelperPkgs[loader.FuncPackage(callee)] {
				return false
			}
			calls++
		}
	}
	return calls > 0
}

func receiverTypeName(fn *ssa.Function) string {
	recv := fn.Signature.Recv()
	if recv == nil {
		return ""
	}
	t := types.Unalias(recv.Type())
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	if named, ok := t.(*types.Named); ok {
		return named.Obj().Name()
	}
	return ""
}

func isErrorType(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	return ok && named.Obj().Pkg() == nil && named.Obj().Name() == "error"
}

func isBoolType(t types.Type) bool {
	basic, ok := t.Underlying().(*types.Basic)
	return ok && basic.Info()&types.IsBoolean != 0
}
