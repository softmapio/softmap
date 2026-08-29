// Package guards finds the "why" layer of a flow: If branches that
// terminate the function with an error. Semantic guards (business
// conditions, permission gates, validation) become decision nodes with
// explicit fail exits; mechanical `if err != nil` propagation only marks
// the failing call as fallible. The classifier errs toward mechanical: a
// missed guard is a smaller failure than a noisy map.
package guards

import (
	"fmt"
	"go/constant"
	"go/token"
	"go/types"
	"strings"
	"unicode"

	"golang.org/x/tools/go/ssa"

	"github.com/softmapio/softmap/internal/loader"
	"github.com/softmapio/softmap/internal/ssax"
)

// ExitError identifies the error a fail branch returns.
type ExitError struct {
	Kind    string // "sentinel" | "message" | "unknown"
	Name    string // sentinel var name
	Message string // verbatim format string, incl. non-ASCII and %s/%v verbs
	Pos     token.Pos
}

// Guard is one If whose one branch terminates the function with an error.
type Guard struct {
	CondText string
	StmtPos  token.Pos // enclosing if-statement: dedupe key and display pos
	// OrderPos is the layout anchor: for `if err := call(); err != nil`
	// the guard logically runs AFTER the call it checks, even though the
	// if-keyword precedes it on the line.
	OrderPos  token.Pos
	FailWhen  bool // condition value that takes the exit branch
	PassBlock *ssa.BasicBlock
	Exit      ExitError
	Uses      []string // module calls (same function) feeding the condition
	// UsesFns: the SSA functions behind Uses - annotate exempts them from
	// drop rules (guard evidence must never be filtered away).
	UsesFns    []*ssa.Function
	Mechanical bool
	// CondFromAST: condition text came from source (false = raw SSA form).
	CondFromAST bool
	// FailedCall is set for mechanical guards: the call whose error is
	// being propagated (its node gets the fallible badge).
	FailedCall *ssa.Function
	// Checks: validation rules this guard enforces, when its condition is
	// a validation call.
	Checks string
	// Branch: set by annotate when BOTH sides of a gate hold work - an
	// exclusive either/or path split.
	Branch bool
	// Gate: neither branch exits, but one side holds work the other skips
	// ("if !whiteListClient { call accounting }"). PassBlock is the working
	// side (settled by annotate, which sees the flow); SkipBlock the other.
	// For gates FailWhen is the condition value that SKIPS the work.
	Gate      bool
	SkipBlock *ssa.BasicBlock
}

const (
	maxExitHops = 5 // straight-line blocks tolerated between If and Return
	maxUses     = 3
)

// Analyze finds guards in fn, deduplicated by source if-statement (&&/||
// lower to several SSA branches). Functions that cannot return an error
// have no guards by definition.
func Analyze(p *loader.Program, fn *ssa.Function) []Guard {
	if fn == nil || fn.Blocks == nil {
		return nil
	}
	errIdx := errorResultIndex(fn)
	var out []Guard
	seenStmt := map[token.Pos]bool{}
	for _, b := range fn.Blocks {
		if len(b.Instrs) == 0 {
			continue
		}
		ifInstr, ok := b.Instrs[len(b.Instrs)-1].(*ssa.If)
		if !ok || len(b.Succs) != 2 {
			continue
		}
		var thenErr, elseErr ssa.Value
		var thenRet, elseRet *ssa.Return
		var thenChain, elseChain []*ssa.BasicBlock
		var thenVoid, elseVoid *ExitError
		if errIdx >= 0 {
			thenErr, thenRet, thenChain = exitBranch(b.Succs[0], errIdx)
			elseErr, elseRet, elseChain = exitBranch(b.Succs[1], errIdx)
		} else {
			// Void net/http handlers reject by responding and returning -
			// those branches are exits exactly like `return c.JSON(4xx,...)`.
			if c, r, ch := voidRespondExit(b.Succs[0]); c != nil {
				if e, ok := httpResponseExit(c, r); ok {
					thenRet, thenChain, thenVoid = r, ch, &e
				}
			}
			if c, r, ch := voidRespondExit(b.Succs[1]); c != nil {
				if e, ok := httpResponseExit(c, r); ok {
					elseRet, elseChain, elseVoid = r, ch, &e
				}
			}
		}
		var g Guard
		var errVal ssa.Value
		var ret *ssa.Return
		var chain []*ssa.BasicBlock
		var terminalExit *ExitError
		switch {
		case thenRet == nil && elseRet == nil:
			// Neither branch exits: possibly a gate - a condition that
			// decides whether a block of work runs at all.
			if gc, ok := gateCandidate(p, b, ifInstr, seenStmt); ok {
				out = append(out, gc)
			}
			continue
		case thenRet != nil && elseRet != nil:
			// Both branches end the function - the tail `if err != nil {
			// return c.JSON(500,...) }; return c.JSON(200,...)` idiom. If
			// exactly one side is an HTTP rejection, it is still a guard:
			// one outcome fails, the other completes.
			te, tok := httpResponseExit(errorSourceCall(origin(thenErr)), thenRet)
			ee, eok := httpResponseExit(errorSourceCall(origin(elseErr)), elseRet)
			if tok == eok {
				continue // none or both reject: ambiguous, skip
			}
			if tok {
				g = Guard{FailWhen: true, PassBlock: b.Succs[1]}
				errVal, ret, chain, terminalExit = thenErr, thenRet, thenChain, &te
			} else {
				g = Guard{FailWhen: false, PassBlock: b.Succs[0]}
				errVal, ret, chain, terminalExit = elseErr, elseRet, elseChain, &ee
			}
		case thenRet != nil:
			g = Guard{FailWhen: true, PassBlock: b.Succs[1]}
			errVal, ret, chain = thenErr, thenRet, thenChain
			if thenVoid != nil {
				terminalExit = thenVoid
			}
		default:
			g = Guard{FailWhen: false, PassBlock: b.Succs[0]}
			errVal, ret, chain = elseErr, elseRet, elseChain
			if elseVoid != nil {
				terminalExit = elseVoid
			}
		}

		// The return statement's position is the most reliable anchor for
		// the enclosing source `if` (SSA conditions often carry no
		// position of their own); the condition's own position is the
		// fallback anchor before giving up to raw SSA text.
		text, stmtPos, okAST := p.IfCondition(ret.Pos())
		if !okAST {
			text, stmtPos, okAST = p.IfCondition(ifInstr.Cond.Pos())
		}
		if !okAST {
			text, stmtPos = ifInstr.Cond.String(), ifInstr.Cond.Pos()
		}
		g.CondFromAST = okAST
		if seenStmt[stmtPos] {
			continue
		}
		seenStmt[stmtPos] = true
		g.CondText, g.StmtPos = text, stmtPos
		g.OrderPos = stmtPos
		var condCall *ssa.Call
		if bin, ok := ifInstr.Cond.(*ssa.BinOp); ok {
			for _, side := range []ssa.Value{bin.X, bin.Y} {
				if c := errorSourceCall(origin(side)); c != nil {
					condCall = c
					if c.Pos()+1 > g.OrderPos {
						g.OrderPos = c.Pos() + 1
					}
				}
			}
		}

		// Exit identity is classified for BOTH classes: semantic guards
		// render it; mechanical wraps use it to dedupe error_exits.
		g.Exit = classifyExit(errVal, ret)
		if terminalExit != nil {
			g.Exit = *terminalExit
		}

		if terminalExit == nil && tailContinuation(g.Exit, errVal, chain) {
			// `return c.JSON(4xx, body)` is the framework idiom for a
			// rejection - a real exit with a status and a message, not
			// continuation.
			if e2, ok := httpResponseExit(errorSourceCall(origin(errVal)), ret); ok {
				g.Exit = e2
			} else {
				// `if cond { return doA() }; doB(...)` - not a rejection but
				// an exclusive either/or: offer it as a gate candidate; the
				// annotate pass checks whether both sides carry real work.
				if g.CondFromAST && !errNilCond(ifInstr.Cond) {
					gb := g
					gb.Gate = true
					gb.Exit = ExitError{}
					gb.SkipBlock = chain[0]
					gb.Uses, gb.UsesFns = condUses(p, ifInstr.Cond)
					out = append(out, gb)
				}
				continue // `return doWork()`: continuation, not a rejection
			}
		}
		// A bare "HTTP 400" says nothing when three guards in a row reject
		// with it - name the call whose failure causes the rejection.
		if g.Exit.Kind == "message" && strings.HasPrefix(g.Exit.Message, "HTTP ") &&
			!strings.Contains(g.Exit.Message, "·") && condCall != nil {
			if info := ssax.Callee(condCall); info != nil && info.Name != "" {
				g.Exit.Message += " (" + info.Name + " failed)"
			}
		}
		if condCall != nil {
			g.Checks = ValidationChecks(condCall)
		}
		if e, call := mechanicalPropagation(ifInstr.Cond, g.FailWhen, errVal); e {
			g.Mechanical = true
			g.FailedCall = call
		} else {
			g.Uses, g.UsesFns = condUses(p, ifInstr.Cond)
		}
		out = append(out, g)
	}
	return out
}

// gateCandidate recognizes the structural half of a gate: an If where both
// branches continue. Whether one side actually holds work the other skips
// is settled later by annotate, which sees the flow. Loop headers and
// mechanical `if err != nil { log }` shapes never qualify.
func gateCandidate(p *loader.Program, b *ssa.BasicBlock, ifInstr *ssa.If, seenStmt map[token.Pos]bool) (Guard, bool) {
	// A loop header has a back edge: a predecessor it dominates. Its
	// condition is iteration bookkeeping, not a decision.
	for _, pred := range b.Preds {
		if b.Dominates(pred) {
			return Guard{}, false
		}
	}
	if errNilCond(ifInstr.Cond) {
		return Guard{}, false
	}
	// Both sides must rejoin the flow. A branch that only returns (early
	// success, errorless-function bailout) is an ending, not a gate.
	if !rejoins(b.Succs[0]) || !rejoins(b.Succs[1]) {
		return Guard{}, false
	}
	// Gates insist on a source `if` statement: for/range conditions have
	// none, and raw SSA condition text is unreadable anyway. A bool-var
	// condition ("if !whiteListClient") is a phi with no position inside
	// the statement - anchor through a branch body instead.
	text, stmtPos, okAST := p.IfCondition(ifInstr.Cond.Pos())
	if !okAST {
		for _, succ := range []*ssa.BasicBlock{b.Succs[0], b.Succs[1]} {
			if bp := firstInstrPos(succ); bp.IsValid() {
				if text, stmtPos, okAST = p.IfConditionOfBody(bp); okAST {
					break
				}
			}
		}
	}
	if !okAST || seenStmt[stmtPos] {
		return Guard{}, false
	}
	seenStmt[stmtPos] = true
	g := Guard{
		Gate:        true,
		CondText:    text,
		StmtPos:     stmtPos,
		OrderPos:    stmtPos,
		CondFromAST: true,
		PassBlock:   b.Succs[0],
		SkipBlock:   b.Succs[1],
	}
	g.Uses, g.UsesFns = condUses(p, ifInstr.Cond)
	return g, true
}

// firstInstrPos: the first valid source position in a block.
func firstInstrPos(b *ssa.BasicBlock) token.Pos {
	for _, instr := range b.Instrs {
		if pos := instr.Pos(); pos.IsValid() {
			return pos
		}
	}
	return token.NoPos
}

// rejoins reports whether the branch flows back into the rest of the
// function: either it IS the join point (multiple predecessors) or its
// dominated region has an edge escaping the region.
func rejoins(succ *ssa.BasicBlock) bool {
	if len(succ.Preds) > 1 {
		return true
	}
	for _, b := range succ.Parent().Blocks {
		if !succ.Dominates(b) {
			continue
		}
		for _, out := range b.Succs {
			if !succ.Dominates(out) {
				return true
			}
		}
	}
	return false
}

// errNilCond: the condition is E == nil / E != nil for an error-typed E -
// the mechanical shape, never a business gate.
func errNilCond(cond ssa.Value) bool {
	bin, ok := cond.(*ssa.BinOp)
	if !ok {
		return false
	}
	for _, side := range []ssa.Value{bin.X, bin.Y} {
		if ssax.IsErrorType(side.Type()) {
			return true
		}
	}
	return false
}

func errorResultIndex(fn *ssa.Function) int {
	results := fn.Signature.Results()
	idx := -1
	for i := 0; i < results.Len(); i++ {
		if ssax.IsErrorType(results.At(i).Type()) {
			idx = i
		}
	}
	return idx
}

// exitBranch reports whether bb reaches, through a straight-line chain
// (every block exactly one successor - tolerates logging calls and defer's
// RunDefers), a Return whose error result is not the nil constant.
func exitBranch(bb *ssa.BasicBlock, errIdx int) (ssa.Value, *ssa.Return, []*ssa.BasicBlock) {
	cur := bb
	var chain []*ssa.BasicBlock
	for hops := 0; hops < maxExitHops && cur != nil; hops++ {
		chain = append(chain, cur)
		if len(cur.Instrs) == 0 {
			return nil, nil, nil
		}
		switch last := cur.Instrs[len(cur.Instrs)-1].(type) {
		case *ssa.Return:
			if len(last.Results) <= errIdx {
				return nil, nil, nil
			}
			v := last.Results[errIdx]
			// With defer in the function, `return nil` compiles to a
			// store/load through a stack cell - chase before deciding.
			if isNilConst(origin(v)) {
				return nil, nil, nil
			}
			return v, last, chain
		case *ssa.Jump:
			cur = cur.Succs[0]
		default:
			return nil, nil, nil
		}
	}
	return nil, nil, nil
}

// tailContinuation reports the "return doWork()" shape: the branch's error
// comes from a call made inside the branch itself and has no recognizable
// identity - that is the flow continuing into work, not a rejection.
func tailContinuation(exit ExitError, errVal ssa.Value, chain []*ssa.BasicBlock) bool {
	if exit.Kind != "unknown" {
		return false
	}
	src := errorSourceCall(origin(errVal))
	if src == nil {
		return false
	}
	for _, b := range chain {
		if src.Block() == b {
			return true
		}
	}
	return false
}

func isNilConst(v ssa.Value) bool {
	c, ok := v.(*ssa.Const)
	return ok && c.Value == nil
}

// mechanicalPropagation implements the classifier's exact rules:
//  1. the condition is E != nil / E == nil for an error-typed E,
//  2. E is the error result of a call in this function,
//  3. the branch returns E itself or a wrap of E (fmt.Errorf with E among
//     args, errors.Join, github.com/pkg/errors.Wrap*).
//
// Anything else that ends the flow with an error is a semantic guard.
func mechanicalPropagation(cond ssa.Value, failWhen bool, errVal ssa.Value) (bool, *ssa.Function) {
	bin, ok := cond.(*ssa.BinOp)
	if !ok || (bin.Op != token.EQL && bin.Op != token.NEQ) {
		return false, nil
	}
	var e ssa.Value
	switch {
	case isNilConst(bin.X):
		e = bin.Y
	case isNilConst(bin.Y):
		e = bin.X
	default:
		return false, nil
	}
	if !ssax.IsErrorType(e.Type()) {
		return false, nil
	}
	// Direction sanity: err != nil must fail on true; err == nil on false.
	if (bin.Op == token.NEQ) != failWhen {
		return false, nil
	}
	src := origin(e)
	call := errorSourceCall(src)
	if call == nil {
		return false, nil
	}
	r := origin(errVal)
	if r == src || isWrapOf(r, src) {
		callee := call.Common().StaticCallee()
		return true, callee
	}
	return false, nil
}

// origin unwraps interface conversions and stack store/load pairs.
func origin(v ssa.Value) ssa.Value {
	for depth := 0; depth < 6 && v != nil; depth++ {
		switch t := v.(type) {
		case *ssa.MakeInterface:
			v = t.X
		case *ssa.ChangeInterface:
			v = t.X
		case *ssa.UnOp:
			stored := ssax.ReachingStore(t, t.X)
			if stored == nil {
				return v
			}
			v = stored
		default:
			return v
		}
	}
	return v
}

// errorSourceCall reports the call whose (possibly tuple) result v is.
func errorSourceCall(v ssa.Value) *ssa.Call {
	switch t := v.(type) {
	case *ssa.Call:
		return t
	case *ssa.Extract:
		if c, ok := t.Tuple.(*ssa.Call); ok {
			return c
		}
	}
	return nil
}

var wrapFuncs = map[string]map[string]bool{
	"fmt":                   {"Errorf": true},
	"errors":                {"Join": true},
	"github.com/pkg/errors": {"Wrap": true, "Wrapf": true, "WithMessage": true, "WithMessagef": true, "WithStack": true},
}

// isWrapOf reports whether r wraps e: a known wrapping call with e among
// its (variadic) arguments.
func isWrapOf(r, e ssa.Value) bool {
	call, ok := r.(*ssa.Call)
	if !ok {
		return false
	}
	callee := call.Common().StaticCallee()
	if callee == nil {
		return false
	}
	names := wrapFuncs[loader.FuncPackage(callee)]
	if names == nil || !names[callee.Name()] {
		return false
	}
	for _, arg := range call.Common().Args {
		if origin(arg) == e {
			return true
		}
		for _, v := range ssax.VarargValues(arg) {
			if origin(v) == e {
				return true
			}
		}
	}
	return false
}

// SuccessResponse reports how fn completes successfully over HTTP: a
// return-position response call with a constant status below 400
// ("HTTP 200"). Empty when the function does not respond that way.
func SuccessResponse(fn *ssa.Function) string {
	if fn == nil || fn.Blocks == nil {
		return ""
	}
	errIdx := errorResultIndex(fn)
	if errIdx < 0 {
		// Void handler: any respond site with a 2xx/3xx constant is the
		// success outcome.
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				if call, ok := instr.(*ssa.Call); ok {
					if status, _, ok := respondSiteArgs(call); ok && status < 400 {
						return successText(status)
					}
				}
			}
		}
		return ""
	}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			ret, ok := instr.(*ssa.Return)
			if !ok || len(ret.Results) <= errIdx {
				continue
			}
			call := errorSourceCall(origin(ret.Results[errIdx]))
			if call == nil {
				continue
			}
			if status, _, ok := respondSiteArgs(call); ok && status < 400 {
				return successText(status)
			}
		}
	}
	return ""
}

// successText: the code plus its reason phrase where the phrase carries
// meaning an analyst asks about ("204 - no body comes back").
func successText(status int) string {
	switch status {
	case 201:
		return "HTTP 201 Created"
	case 202:
		return "HTTP 202 Accepted"
	case 204:
		return "HTTP 204 No Content"
	default:
		return fmt.Sprintf("HTTP %d", status)
	}
}

// respondSiteArgs recognizes an HTTP respond call - a framework method
// (echo/gin) or a hand-rolled module helper (RespondWithJSON, respondError,
// WriteJSON: name contains respond/json/write and the callee takes an
// http.ResponseWriter) - and returns the constant status plus the args
// after it (payload / message candidates).
func respondSiteArgs(call *ssa.Call) (int, []ssa.Value, bool) {
	if call == nil {
		return 0, nil, false
	}
	info := ssax.Callee(call)
	if info == nil {
		return 0, nil, false
	}
	args := call.Common().Args[info.ArgOffset:]
	if responseMethods[ssax.NormalizePkg(info.Pkg)][info.Name] {
		if len(args) == 0 {
			return 0, nil, false
		}
		status, ok := respConstInt(args[0], 2)
		if !ok {
			return 0, nil, false
		}
		return status, args[1:], true
	}
	lower := strings.ToLower(info.Name)
	if !strings.Contains(lower, "respond") && !strings.Contains(lower, "json") && !strings.Contains(lower, "write") {
		return 0, nil, false
	}
	callee := call.Common().StaticCallee()
	if callee == nil || !takesResponseWriter(callee) {
		return 0, nil, false
	}
	for i, a := range args {
		if status, ok := respConstInt(a, 2); ok && status >= 100 && status < 600 {
			return status, args[i+1:], true
		}
	}
	return 0, nil, false
}

func takesResponseWriter(fn *ssa.Function) bool {
	params := fn.Signature.Params()
	for i := 0; i < params.Len(); i++ {
		if named, ok := types.Unalias(params.At(i).Type()).(*types.Named); ok {
			if pkg := named.Obj().Pkg(); pkg != nil && pkg.Path() == "net/http" && named.Obj().Name() == "ResponseWriter" {
				return true
			}
		}
	}
	return false
}

// voidRespondExit: for handlers without an error result, an exit branch is
// a straight-line chain that responds with a 4xx/5xx and returns.
func voidRespondExit(bb *ssa.BasicBlock) (*ssa.Call, *ssa.Return, []*ssa.BasicBlock) {
	cur := bb
	var chain []*ssa.BasicBlock
	var respond *ssa.Call
	for hops := 0; hops < maxExitHops && cur != nil; hops++ {
		chain = append(chain, cur)
		if len(cur.Instrs) == 0 {
			return nil, nil, nil
		}
		for _, instr := range cur.Instrs {
			if call, ok := instr.(*ssa.Call); ok {
				if status, _, ok := respondSiteArgs(call); ok && status >= 400 {
					respond = call
				}
			}
		}
		switch cur.Instrs[len(cur.Instrs)-1].(type) {
		case *ssa.Return:
			if respond == nil {
				return nil, nil, nil
			}
			return respond, cur.Instrs[len(cur.Instrs)-1].(*ssa.Return), chain
		case *ssa.Jump:
			cur = cur.Succs[0]
		default:
			return nil, nil, nil
		}
	}
	return nil, nil, nil
}

var responseMethods = map[string]map[string]bool{
	"github.com/labstack/echo": {"JSON": true, "JSONBlob": true, "String": true, "XML": true, "HTML": true, "Blob": true, "NoContent": true},
	"github.com/gin-gonic/gin": {"JSON": true, "IndentedJSON": true, "String": true, "Data": true, "AbortWithStatusJSON": true, "AbortWithStatus": true},
}

// httpResponseExit recognizes `return c.JSON(status, body)` rejections:
// the status must resolve to a constant >= 400 (a 2xx early return is a
// success path, not a guard). The message is the first resolvable string
// among the args, following one hop into the call that built the body
// (newErrHandler(400, "request_id is required")).
func httpResponseExit(call *ssa.Call, ret *ssa.Return) (ExitError, bool) {
	status, rest, ok := respondSiteArgs(call)
	if !ok || status < 400 {
		return ExitError{}, false
	}
	e := ExitError{Kind: "message", Pos: ret.Pos()}
	for _, a := range rest {
		if msg, ok := respConstString(a, 3); ok {
			e.Message = msg
			break
		}
		// Reused DTO pattern: errHandler.Message = ...; return e.JSON(400,
		// errHandler). The message is the nearest field store before the
		// response call in the same block.
		if msg, ok := blockFieldString(call, a); ok {
			e.Message = msg
			break
		}
	}
	// The status is part of the outcome's identity: "HTTP 404 · not found".
	if e.Message == "" {
		e.Message = fmt.Sprintf("HTTP %d", status)
	} else {
		e.Message = fmt.Sprintf("HTTP %d · %s", status, e.Message)
	}
	return e, true
}

// blockFieldString: given a response-body argument that loads a struct
// from a stack alloc, find the nearest preceding store into any field of
// that alloc within the response call's block and resolve it.
func blockFieldString(call *ssa.Call, arg ssa.Value) (string, bool) {
	load, ok := origin(arg).(*ssa.UnOp)
	if !ok {
		return "", false
	}
	base := load.X
	block := call.Block()
	idx := -1
	for i, instr := range block.Instrs {
		if instr == ssa.Instruction(call) {
			idx = i
			break
		}
	}
	for i := idx - 1; i >= 0; i-- {
		st, ok := block.Instrs[i].(*ssa.Store)
		if !ok {
			continue
		}
		fa, ok := st.Addr.(*ssa.FieldAddr)
		if !ok || fa.X != base {
			continue
		}
		if msg, ok := respConstString(st.Val, 3); ok {
			return msg, true
		}
	}
	return "", false
}

// respConstInt resolves an int-ish value, following struct fields back to
// the constructor call's arguments (errResp.Code pattern).
func respConstInt(v ssa.Value, depth int) (int, bool) {
	if depth < 0 || v == nil {
		return 0, false
	}
	switch t := origin(v).(type) {
	case *ssa.Const:
		if t.Value != nil && t.Value.Kind() == constant.Int {
			if i, ok := constant.Int64Val(t.Value); ok {
				return int(i), true
			}
		}
	case *ssa.Field:
		return callArgsConstInt(t.X, depth-1)
	case *ssa.UnOp:
		if fa, ok := t.X.(*ssa.FieldAddr); ok {
			// errResp.Code where errResp sits in a stack alloc: the
			// constructor call is the alloc's single store.
			if stored := ssax.SingleStore(fa.X); stored != nil {
				if v, ok := callArgsConstInt(stored, depth-1); ok {
					return v, ok
				}
			}
			return callArgsConstInt(fa.X, depth-1)
		}
	case *ssa.Call:
		return callArgsConstInt(t, depth-1)
	}
	return 0, false
}

func callArgsConstInt(v ssa.Value, depth int) (int, bool) {
	call, ok := origin(v).(*ssa.Call)
	if !ok {
		return 0, false
	}
	for _, a := range call.Common().Args {
		if s, ok := respConstInt(a, depth); ok && s >= 100 && s <= 599 {
			return s, true
		}
	}
	return 0, false
}

// respConstString finds a constant string in the value or, one hop deep,
// among the arguments of the call that produced it (following the stack
// alloc when the body value was spilled).
func respConstString(v ssa.Value, depth int) (string, bool) {
	if depth < 0 || v == nil {
		return "", false
	}
	if s, ok := ssax.ConstString(v); ok {
		return s, true
	}
	src := origin(v)
	if u, ok := src.(*ssa.UnOp); ok {
		if stored := ssax.SingleStore(u.X); stored != nil {
			src = origin(stored)
		}
	}
	if call, ok := src.(*ssa.Call); ok {
		if call.Common().IsInvoke() {
			// err.Error(): the receiver (fmt.Errorf(...)) carries the text.
			if s, ok := respConstString(call.Common().Value, depth-1); ok {
				return s, true
			}
		}
		for _, a := range call.Common().Args {
			if s, ok := respConstString(a, depth-1); ok {
				return s, true
			}
		}
	}
	return "", false
}

// classifyExit identifies the error a semantic guard returns.
func classifyExit(v ssa.Value, ret *ssa.Return) ExitError {
	e := ExitError{Kind: "unknown", Pos: ret.Pos()}
	classify(origin(v), 4, &e)
	return e
}

func classify(v ssa.Value, depth int, e *ExitError) {
	if depth == 0 || v == nil || e.Kind != "unknown" {
		return
	}
	switch t := v.(type) {
	case *ssa.Global:
		e.Kind, e.Name = "sentinel", t.Name()
	case *ssa.UnOp:
		if g, ok := t.X.(*ssa.Global); ok {
			e.Kind, e.Name = "sentinel", g.Name()
		}
	case *ssa.Call:
		cc := t.Common()
		if callee := cc.StaticCallee(); callee != nil {
			pkg := loader.FuncPackage(callee)
			if (pkg == "errors" && callee.Name() == "New") || (pkg == "fmt" && callee.Name() == "Errorf") {
				if msg, ok := ssax.ConstString(cc.Args[0]); ok {
					// "%w: %w" carries no words of its own - the wrapped
					// sentinel is the real identity: the card should read
					// "ErrPrescoringDeclined", not "…: …".
					if pkg == "fmt" && wordlessFormat(msg) && len(cc.Args) > 1 {
						if name := lastSentinelArg(cc.Args[1]); name != "" {
							e.Kind, e.Name = "sentinel", name
							return
						}
					}
					e.Kind, e.Message = "message", msg
				}
				return
			}
			// Method chained on a sentinel keeps the sentinel's name.
			if callee.Signature.Recv() != nil && len(cc.Args) > 0 {
				classify(origin(cc.Args[0]), depth-1, e)
			}
		} else if cc.IsInvoke() {
			classify(origin(cc.Value), depth-1, e)
		}
	case *ssa.Phi:
		for _, edge := range t.Edges {
			classify(origin(edge), depth-1, e)
			if e.Kind != "unknown" {
				return
			}
		}
	}
}

// wordlessFormat reports a format string that is all verbs and separators
// ("%w: %w", "%v - %w"): nothing a reader could learn from.
func wordlessFormat(format string) bool {
	runes := []rune(format)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '%' && i+1 < len(runes) {
			// Skip the verb: flags, width, precision, then the letter.
			j := i + 1
			for j < len(runes) && strings.ContainsRune("+-# .0123456789*", runes[j]) {
				j++
			}
			if j < len(runes) {
				i = j
				continue
			}
		}
		if unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) {
			return false
		}
	}
	return true
}

// lastSentinelArg finds the last package-level error variable among a
// wrap's variadic args - by convention the sentinel marker goes last
// (fmt.Errorf("%w: %w", err, ErrPrescoringDeclined)).
func lastSentinelArg(pack ssa.Value) string {
	name := ""
	for _, v := range ssax.VarargValues(pack) {
		v = origin(v)
		if mi, ok := v.(*ssa.MakeInterface); ok {
			v = origin(mi.X)
		}
		switch t := v.(type) {
		case *ssa.Global:
			name = t.Name()
		case *ssa.UnOp:
			if g, ok := t.X.(*ssa.Global); ok && ssax.IsErrorType(t.Type()) {
				name = g.Name()
			}
		}
	}
	return name
}

// condUses traces the condition's operands to module-function calls in the
// same function - the "why is this data fetched" provenance link.
func condUses(p *loader.Program, cond ssa.Value) ([]string, []*ssa.Function) {
	var out []string
	var fns []*ssa.Function
	seen := map[string]bool{}
	var walk func(v ssa.Value, depth int)
	walk = func(v ssa.Value, depth int) {
		if depth == 0 || v == nil || len(out) >= maxUses {
			return
		}
		switch t := v.(type) {
		case *ssa.Call:
			cc := t.Common()
			if _, isBuiltin := cc.Value.(*ssa.Builtin); isBuiltin {
				for _, a := range cc.Args {
					walk(a, depth-1)
				}
				return
			}
			// Interface calls (DI-wired repositories) are the norm in real
			// services: the method name IS the provenance. Never descend
			// into an invoke's args - that road leads to ctx plumbing.
			if cc.IsInvoke() {
				if m := cc.Method; m != nil && m.Pkg() != nil && p.PkgInModule(m.Pkg().Path()) {
					if name := m.Name(); !seen[name] {
						seen[name] = true
						out = append(out, name)
					}
				}
				return
			}
			if callee := cc.StaticCallee(); callee != nil && p.InModule(callee) {
				name := callee.Name()
				if !seen[name] {
					seen[name] = true
					out = append(out, name)
					fns = append(fns, callee)
				}
				return
			}
			for _, a := range cc.Args {
				walk(a, depth-1)
			}
		case *ssa.Extract:
			walk(t.Tuple, depth-1)
		case *ssa.BinOp:
			walk(t.X, depth-1)
			walk(t.Y, depth-1)
		case *ssa.UnOp:
			if stored := ssax.ReachingStore(t, t.X); stored != nil {
				walk(stored, depth-1)
				return
			}
			walk(t.X, depth-1)
		case *ssa.Lookup:
			walk(t.X, depth-1)
		case *ssa.FieldAddr:
			walk(t.X, depth-1)
		case *ssa.Field:
			walk(t.X, depth-1)
		case *ssa.MakeInterface:
			walk(t.X, depth-1)
		case *ssa.ChangeType:
			walk(t.X, depth-1)
		case *ssa.Convert:
			walk(t.X, depth-1)
		case *ssa.Phi:
			for i, edge := range t.Edges {
				if i >= 2 {
					break
				}
				walk(edge, depth-1)
			}
		}
	}
	walk(cond, 4)
	return out, fns
}

// Display renders the exit for humans: sentinel name, else message, else a
// generic marker.
func (e ExitError) Display() string {
	switch e.Kind {
	case "sentinel":
		return e.Name
	case "message":
		return `"` + e.Message + `"`
	default:
		return "error"
	}
}

// Key is the dedupe identity used when unifying with error_exits.
func (e ExitError) Key() string {
	return strings.TrimSpace(e.Display())
}
