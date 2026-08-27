// Package ssax holds small shared helpers for inspecting SSA values and call
// sites: callee identification, best-effort string-constant resolution, and
// variadic-argument extraction. Used by entrypoint discovery and effect
// detection.
package ssax

import (
	"fmt"
	"go/constant"
	"go/types"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// CalleeInfo identifies what a call site calls, uniformly for static calls
// and interface (invoke-mode) calls.
type CalleeInfo struct {
	Pkg    string        // package path of the callee (or its interface)
	Type   string        // receiver or interface type name, "" for package funcs
	Name   string        // function or method name
	Invoke bool          // true for interface dispatch
	Static *ssa.Function // non-nil for statically resolved callees
	// ArgOffset is the index of the first non-receiver argument in
	// site.Common().Args.
	ArgOffset int
}

var versionSuffix = regexp.MustCompile(`/v\d+$`)

// NormalizePkg strips a trailing /vN major-version suffix so matchers cover
// all major versions of a library with one entry.
func NormalizePkg(path string) string {
	return versionSuffix.ReplaceAllString(path, "")
}

// Callee extracts CalleeInfo from a call site. Returns nil for calls to
// builtins and for dynamic calls of function values.
func Callee(site ssa.CallInstruction) *CalleeInfo {
	common := site.Common()
	if common.IsInvoke() {
		m := common.Method
		info := &CalleeInfo{Name: m.Name(), Invoke: true}
		if m.Pkg() != nil {
			info.Pkg = m.Pkg().Path()
		}
		if named, ok := types.Unalias(common.Value.Type()).(*types.Named); ok {
			info.Type = named.Obj().Name()
			if named.Obj().Pkg() != nil {
				info.Pkg = named.Obj().Pkg().Path()
			}
		}
		return info
	}
	fn := common.StaticCallee()
	if fn == nil {
		return nil
	}
	info := &CalleeInfo{Name: fn.Name(), Static: fn}
	sig := fn.Signature
	// Prefer the declared object: synthetic wrappers for promoted methods
	// carry the original method's package there.
	if obj := fn.Object(); obj != nil {
		if tf, ok := obj.(*types.Func); ok {
			if tf.Pkg() != nil {
				info.Pkg = tf.Pkg().Path()
			}
			sig = tf.Type().(*types.Signature)
		}
	} else if fn.Pkg != nil {
		info.Pkg = fn.Pkg.Pkg.Path()
	}
	if recv := fn.Signature.Recv(); recv != nil {
		info.ArgOffset = 1
	}
	if recv := sig.Recv(); recv != nil {
		info.Type = typeName(recv.Type())
	}
	return info
}

func typeName(t types.Type) string {
	t = types.Unalias(t)
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	if named, ok := t.(*types.Named); ok {
		return named.Obj().Name()
	}
	return ""
}

const maxChase = 5

// ConstString resolves v to a string constant if it is one, a conversion of
// one, or a package-level variable stored exactly once (in init) with one.
// Returns "", false when the value is dynamic.
func ConstString(v ssa.Value) (string, bool) {
	return constString(v, maxChase)
}

func constString(v ssa.Value, depth int) (string, bool) {
	if depth == 0 || v == nil {
		return "", false
	}
	switch v := v.(type) {
	case *ssa.Const:
		if v.Value != nil && v.Value.Kind() == constant.String {
			return constant.StringVal(v.Value), true
		}
	case *ssa.ChangeType:
		return constString(v.X, depth-1)
	case *ssa.Convert:
		return constString(v.X, depth-1)
	case *ssa.MakeInterface:
		return constString(v.X, depth-1)
	case *ssa.UnOp:
		// Load through a pointer: a package-level var or a local alloc.
		if stored := SingleStore(v.X); stored != nil {
			return constString(stored, depth-1)
		}
		// A struct field (n.Endpoint pattern): chase to the field's single
		// package-wide store, typically in a constructor.
		if fa, ok := v.X.(*ssa.FieldAddr); ok {
			if stored := packageFieldStore(fa); stored != nil {
				return constString(stored, depth-1)
			}
		}
	case *ssa.Global:
		if stored := SingleStore(v); stored != nil {
			return constString(stored, depth-1)
		}
	case *ssa.Phi:
		// All edges agreeing on one constant still counts.
		var got string
		for i, e := range v.Edges {
			s, ok := constString(e, depth-1)
			if !ok || (i > 0 && s != got) {
				return "", false
			}
			got = s
		}
		return got, len(v.Edges) > 0
	}
	return "", false
}

// SingleStore returns the value stored to addr if exactly one store to it
// exists in the enclosing function (for locals/field addresses) or in the
// package init (for globals). Returns nil otherwise.
func SingleStore(addr ssa.Value) ssa.Value {
	var stores []ssa.Value
	switch a := addr.(type) {
	case *ssa.Global:
		pkg := a.Package()
		if pkg == nil {
			return nil
		}
		init := pkg.Func("init")
		if init == nil {
			return nil
		}
		for _, b := range init.Blocks {
			for _, instr := range b.Instrs {
				if st, ok := instr.(*ssa.Store); ok && st.Addr == a {
					stores = append(stores, st.Val)
				}
			}
		}
	default:
		refs := referrers(addr)
		for _, instr := range refs {
			if st, ok := instr.(*ssa.Store); ok && st.Addr == addr {
				stores = append(stores, st.Val)
			}
		}
	}
	if len(stores) == 1 {
		return stores[0]
	}
	return nil
}

func referrers(v ssa.Value) []ssa.Instruction {
	if refs := v.Referrers(); refs != nil {
		return *refs
	}
	return nil
}

// VarargValues unpacks the values passed through a final variadic argument
// (an *ssa.Slice over an *ssa.Alloc), in index order. Returns nil when the
// slice is not a literal vararg pack (e.g. a forwarded []T).
func VarargValues(v ssa.Value) []ssa.Value {
	slice, ok := v.(*ssa.Slice)
	if !ok {
		return nil
	}
	alloc, ok := slice.X.(*ssa.Alloc)
	if !ok {
		return nil
	}
	byIndex := map[int64]ssa.Value{}
	var max int64 = -1
	for _, instr := range referrers(alloc) {
		ia, ok := instr.(*ssa.IndexAddr)
		if !ok {
			continue
		}
		idx, ok := ia.Index.(*ssa.Const)
		if !ok || idx.Value == nil {
			continue
		}
		i, ok := constant.Int64Val(idx.Value)
		if !ok {
			continue
		}
		if stored := SingleStore(ia); stored != nil {
			byIndex[i] = stored
			if i > max {
				max = i
			}
		}
	}
	if max < 0 {
		return nil
	}
	out := make([]ssa.Value, 0, max+1)
	for i := int64(0); i <= max; i++ {
		if val, ok := byIndex[i]; ok {
			out = append(out, val)
		}
	}
	return out
}

// SliceStrings resolves a []string-typed value built from constants (either
// a vararg pack or a literal) to its elements. ok is false if any element is
// dynamic.
func SliceStrings(v ssa.Value) ([]string, bool) {
	vals := VarargValues(v)
	if vals == nil {
		return nil, false
	}
	out := make([]string, 0, len(vals))
	for _, val := range vals {
		s, ok := ConstString(val)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// StructFieldString resolves the named field of a struct-typed value to a
// string constant. It handles the common composite-literal shape: a load of
// an *ssa.Alloc whose FieldAddrs were stored exactly once. This is how Kafka
// writer/reader topics configured via config structs resolve.
func StructFieldString(v ssa.Value, field string) (string, bool) {
	for depth := 0; depth < maxChase; depth++ {
		switch vv := v.(type) {
		case *ssa.UnOp:
			v = vv.X
			continue
		case *ssa.MakeInterface:
			v = vv.X
			continue
		case *ssa.Alloc:
			return allocFieldString(vv, field)
		case *ssa.FieldAddr:
			// The struct itself lives in a field (the p.w pattern): the
			// store usually happens in a constructor, so chase one
			// interprocedural level to the field's single package-wide store.
			if stored := packageFieldStore(vv); stored != nil {
				v = stored
				continue
			}
			return "", false
		default:
			return "", false
		}
	}
	return "", false
}

// fieldKey identifies a (named struct type, field index) pair.
type fieldKey struct {
	obj   *types.TypeName
	field int
}

// fieldStoreCache memoizes, per program, every store into a named struct
// field. One pass over all functions; entries with more than one store are
// marked ambiguous (nil).
var fieldStoreCache sync.Map // *ssa.Program -> map[fieldKey]ssa.Value

func fieldStoreIndex(prog *ssa.Program) map[fieldKey]ssa.Value {
	if v, ok := fieldStoreCache.Load(prog); ok {
		return v.(map[fieldKey]ssa.Value)
	}
	idx := map[fieldKey]ssa.Value{}
	for fn := range ssautil.AllFunctions(prog) {
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				st, ok := instr.(*ssa.Store)
				if !ok {
					continue
				}
				dst, ok := st.Addr.(*ssa.FieldAddr)
				if !ok {
					continue
				}
				named := fieldOwner(dst)
				if named == nil {
					continue
				}
				key := fieldKey{named.Obj(), dst.Field}
				if _, seen := idx[key]; seen {
					idx[key] = nil // more than one store: ambiguous
				} else {
					idx[key] = st.Val
				}
			}
		}
	}
	fieldStoreCache.Store(prog, idx)
	return idx
}

// FieldStore is packageFieldStore for callers outside this package: the value
// stored into the struct field fa addresses, when exactly one such store
// exists program-wide. It resolves the field a constructor assigned once, for
// values that reach a call site through a struct rather than a local.
func FieldStore(fa *ssa.FieldAddr) ssa.Value { return packageFieldStore(fa) }

// packageFieldStore finds the value stored into the struct field that fa
// addresses, provided exactly one such store exists anywhere in the program
// (the constructor / composite-literal pattern).
func packageFieldStore(fa *ssa.FieldAddr) ssa.Value {
	named := fieldOwner(fa)
	if named == nil || fa.Parent() == nil {
		return nil
	}
	return fieldStoreIndex(fa.Parent().Prog)[fieldKey{named.Obj(), fa.Field}]
}

// fieldOwner is the named struct type a FieldAddr addresses into, or nil when
// the operand is not a pointer to one. The pointer has to be unwrapped
// defensively: an alias declared as `type P = *S` reaches here as *types.Alias,
// and asserting straight to *types.Pointer would panic on valid Go.
func fieldOwner(fa *ssa.FieldAddr) *types.Named {
	ptr, ok := types.Unalias(fa.X.Type()).(*types.Pointer)
	if !ok {
		return nil
	}
	named, _ := types.Unalias(ptr.Elem()).(*types.Named)
	return named
}

func allocFieldString(alloc *ssa.Alloc, field string) (string, bool) {
	st, ok := types.Unalias(alloc.Type()).(*types.Pointer)
	if !ok {
		return "", false
	}
	strct, ok := st.Elem().Underlying().(*types.Struct)
	if !ok {
		return "", false
	}
	idx := -1
	for i := 0; i < strct.NumFields(); i++ {
		if strct.Field(i).Name() == field {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", false
	}
	for _, instr := range referrers(alloc) {
		fa, ok := instr.(*ssa.FieldAddr)
		if !ok || fa.Field != idx {
			continue
		}
		if stored := SingleStore(fa); stored != nil {
			return ConstString(stored)
		}
	}
	return "", false
}

// ResolveFuncValue chases a function-typed value to its *ssa.Function:
// direct references, closures, and conversions such as http.HandlerFunc(f).
func ResolveFuncValue(v ssa.Value) *ssa.Function {
	return resolveFuncValue(v, maxChase)
}

func resolveFuncValue(v ssa.Value, depth int) *ssa.Function {
	if depth == 0 {
		return nil
	}
	switch v := v.(type) {
	case *ssa.Function:
		return v
	case *ssa.MakeClosure:
		return resolveFuncValue(v.Fn, depth-1)
	case *ssa.ChangeType:
		return resolveFuncValue(v.X, depth-1)
	case *ssa.Convert:
		return resolveFuncValue(v.X, depth-1)
	case *ssa.MakeInterface:
		return resolveFuncValue(v.X, depth-1)
	case *ssa.UnOp:
		if stored := SingleStore(v.X); stored != nil {
			return resolveFuncValue(stored, depth-1)
		}
	}
	return nil
}

// ConcreteMethod finds the named method on the concrete type behind an
// interface-typed value (the operand of its MakeInterface), e.g. the
// ServeHTTP of a handler passed to http.Handle.
func ConcreteMethod(prog *ssa.Program, v ssa.Value, name string) *ssa.Function {
	for depth := 0; depth < maxChase; depth++ {
		switch vv := v.(type) {
		case *ssa.MakeInterface:
			return lookupMethod(prog, vv.X.Type(), name)
		case *ssa.ChangeInterface:
			v = vv.X
		case *ssa.UnOp:
			stored := SingleStore(vv.X)
			if stored == nil {
				return nil
			}
			v = stored
		default:
			return nil
		}
	}
	return nil
}

func lookupMethod(prog *ssa.Program, t types.Type, name string) *ssa.Function {
	sel := prog.MethodSets.MethodSet(t).Lookup(nil, name)
	if sel == nil {
		return nil
	}
	// A value-receiver method reached through a pointer type resolves to a
	// synthetic indirection wrapper with no package; callers want the
	// declared method.
	return Declared(prog, prog.MethodValue(sel))
}

// Declared maps synthetic wrappers (indirection/promotion/bound wrappers)
// to the declared function they forward to.
func Declared(prog *ssa.Program, fn *ssa.Function) *ssa.Function {
	if fn == nil || fn.Synthetic == "" {
		return fn
	}
	if tf, ok := fn.Object().(*types.Func); ok {
		if real := prog.FuncValue(tf); real != nil {
			return real
		}
	}
	return fn
}

// SentinelErrors reports the named error outcomes fn can return: sentinel
// globals (return nil, ErrOrderNotFound), methods chained on sentinel
// globals (errs.OrderNotFound.WithDetails(...)), and constant
// errors.New / fmt.Errorf messages. Propagated errors from callees are
// deliberately ignored — they surface on the callee's own node. These are
// the business branch points a flow reader cares about.
func SentinelErrors(fn *ssa.Function) []string {
	if fn == nil || fn.Blocks == nil {
		return nil
	}
	results := fn.Signature.Results()
	errIdx := -1
	for i := 0; i < results.Len(); i++ {
		if IsErrorType(results.At(i).Type()) {
			errIdx = i
		}
	}
	if errIdx < 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			ret, ok := instr.(*ssa.Return)
			if !ok || len(ret.Results) <= errIdx {
				continue
			}
			chaseErrorValue(ret.Results[errIdx], maxChase, add)
		}
	}
	const capExits = 12
	if len(out) > capExits {
		out = append(out[:capExits], fmt.Sprintf("(+%d more)", len(out)-capExits))
	}
	return out
}

func chaseErrorValue(v ssa.Value, depth int, add func(string)) {
	if depth == 0 || v == nil {
		return
	}
	switch v := v.(type) {
	case *ssa.Global:
		add(v.Name())
	case *ssa.UnOp:
		if g, ok := v.X.(*ssa.Global); ok {
			add(g.Name())
			return
		}
		// Functions with defer keep named results in stack allocs: every
		// `return X` becomes store-then-load. Chase the store that reaches
		// this load along the straight-line predecessor chain.
		if _, ok := v.X.(*ssa.Alloc); ok {
			if val := ReachingStore(v, v.X); val != nil {
				chaseErrorValue(val, depth-1, add)
			}
		}
	case *ssa.Phi:
		for _, e := range v.Edges {
			chaseErrorValue(e, depth-1, add)
		}
	case *ssa.MakeInterface:
		chaseErrorValue(v.X, depth-1, add)
	case *ssa.ChangeInterface:
		chaseErrorValue(v.X, depth-1, add)
	case *ssa.Call:
		cc := v.Common()
		if callee := cc.StaticCallee(); callee != nil {
			pkg := funcPkgPath(callee)
			if (pkg == "errors" && callee.Name() == "New") || (pkg == "fmt" && callee.Name() == "Errorf") {
				if msg, ok := ConstString(cc.Args[0]); ok && !strings.Contains(msg, "%w") {
					add(`"` + msg + `"`)
				}
				return
			}
			// A method chained on a sentinel keeps the sentinel's name:
			// errs.OrderNotFound.WithDetails(...).
			if callee.Signature.Recv() != nil && len(cc.Args) > 0 {
				chaseErrorValue(cc.Args[0], depth-1, add)
			}
		} else if cc.IsInvoke() {
			chaseErrorValue(cc.Value, depth-1, add)
		}
	}
}

// ReachingStore finds the store to addr nearest before the load, walking
// backwards through the load's block and then through single-predecessor
// blocks. Sound for the return-site pattern (store result; maybe rundefers;
// load result; return); gives up on merges rather than guessing.
func ReachingStore(load *ssa.UnOp, addr ssa.Value) ssa.Value {
	block := load.Block()
	if block == nil {
		return nil
	}
	start := -1
	for i, instr := range block.Instrs {
		if instr == ssa.Instruction(load) {
			start = i - 1
			break
		}
	}
	if start < 0 {
		start = len(block.Instrs) - 1
	}
	for hops := 0; hops < 4 && block != nil; hops++ {
		for i := start; i >= 0; i-- {
			if st, ok := block.Instrs[i].(*ssa.Store); ok && st.Addr == addr {
				return st.Val
			}
		}
		if len(block.Preds) != 1 {
			return nil
		}
		block = block.Preds[0]
		start = len(block.Instrs) - 1
	}
	return nil
}

func funcPkgPath(fn *ssa.Function) string {
	for f := fn; f != nil; {
		if f.Pkg != nil {
			return f.Pkg.Pkg.Path()
		}
		if orig := f.Origin(); orig != nil && orig != f {
			f = orig
			continue
		}
		f = f.Parent()
	}
	return ""
}

func IsErrorType(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	return ok && named.Obj().Pkg() == nil && named.Obj().Name() == "error"
}

// FuncDisplayName returns the stable qualified name of fn, using the generic
// origin so instantiations do not multiply identities.
func FuncDisplayName(fn *ssa.Function) string {
	if orig := fn.Origin(); orig != nil {
		fn = orig
	}
	return fn.String()
}

// TrimModule shortens a qualified function name relative to a module path:
// "(*example.com/m/svc.Service).Create" -> "(*svc.Service).Create".
func TrimModule(name, module string) string {
	if module == "" {
		return name
	}
	return strings.ReplaceAll(name, module+"/", "")
}
