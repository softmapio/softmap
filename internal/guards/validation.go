package guards

import (
	"fmt"
	"go/constant"
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"

	"github.com/softmapio/softmap/internal/ssax"
)

// Validation-rule extraction: the rules developers already wrote
// (validation.Validate(req.Phone, Required, Length(11,11), Digit)) are the
// data-quality contract product people ask about. They surface in two
// places: on the decision card that enforces them and as a column of the
// request-body table.

const ozzoPkg = "github.com/go-ozzo/ozzo-validation"

func isValidationPkg(pkg string) bool {
	return strings.HasPrefix(ssax.NormalizePkg(pkg), ozzoPkg)
}

// ValidationChecks renders what a validation call enforces, e.g.
// "phone — required, length(11, 11), digit". Empty when call is not a
// recognized validation entrypoint.
func ValidationChecks(call *ssa.Call) string {
	field, rules := validationOfCall(call)
	if len(rules) == 0 {
		return ""
	}
	if field == "" {
		return strings.Join(rules, ", ")
	}
	return field + " — " + strings.Join(rules, ", ")
}

func validationOfCall(call *ssa.Call) (string, []string) {
	if call == nil {
		return "", nil
	}
	info := ssax.Callee(call)
	if info == nil || !isValidationPkg(info.Pkg) {
		return "", nil
	}
	args := call.Common().Args[info.ArgOffset:]
	switch info.Name {
	case "Validate":
		if len(args) < 2 {
			return "", nil
		}
		return fieldRefName(args[0]), ruleStrings(args[1])
	case "ValidateStruct":
		if len(args) < 2 {
			return "", nil
		}
		var parts []string
		for _, fr := range ssax.VarargValues(args[1]) {
			if fc, ok := origin(fr).(*ssa.Call); ok {
				if f, rs := fieldRulesOfCall(fc); f != "" && len(rs) > 0 {
					parts = append(parts, f+": "+strings.Join(rs, ", "))
				}
			}
		}
		return "", parts
	}
	return "", nil
}

// FieldValidations maps request-struct field names to their rules, from
// every validation call inside fn (feeds the request-body table).
func FieldValidations(fn *ssa.Function) map[string]string {
	if fn == nil || fn.Blocks == nil {
		return nil
	}
	out := map[string]string{}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			call, ok := instr.(*ssa.Call)
			if !ok {
				continue
			}
			info := ssax.Callee(call)
			if info == nil || !isValidationPkg(info.Pkg) {
				continue
			}
			args := call.Common().Args[info.ArgOffset:]
			switch info.Name {
			case "Validate":
				if len(args) >= 2 {
					if f := fieldRefName(args[0]); f != "" {
						addRules(out, f, ruleStrings(args[1]))
					}
				}
			case "ValidateStruct":
				if len(args) >= 2 {
					for _, fr := range ssax.VarargValues(args[1]) {
						if fc, ok := origin(fr).(*ssa.Call); ok {
							if f, rs := fieldRulesOfCall(fc); f != "" {
								addRules(out, f, rs)
							}
						}
					}
				}
			}
		}
	}
	return out
}

func addRules(m map[string]string, field string, rules []string) {
	if len(rules) == 0 {
		return
	}
	joined := strings.Join(rules, ", ")
	if cur, ok := m[field]; ok && cur != "" {
		joined = cur + ", " + joined
	}
	m[field] = joined
}

// fieldRulesOfCall handles validation.Field(&req.X, rules...).
func fieldRulesOfCall(call *ssa.Call) (string, []string) {
	info := ssax.Callee(call)
	if info == nil || !isValidationPkg(info.Pkg) || info.Name != "Field" {
		return "", nil
	}
	args := call.Common().Args[info.ArgOffset:]
	if len(args) < 2 {
		return "", nil
	}
	return fieldRefName(args[0]), ruleStrings(args[1])
}

// fieldRefName: req.Phone (loaded or addressed) -> "Phone".
func fieldRefName(v ssa.Value) string {
	for depth := 0; depth < 5; depth++ {
		switch t := v.(type) {
		case *ssa.MakeInterface:
			v = t.X
		case *ssa.UnOp:
			v = t.X
		case *ssa.Field:
			return structFieldName(t.X.Type(), t.Field)
		case *ssa.FieldAddr:
			return structFieldName(deref(t.X.Type()), t.Field)
		default:
			return ""
		}
	}
	return ""
}

func deref(t types.Type) types.Type {
	if ptr, ok := types.Unalias(t).(*types.Pointer); ok {
		return ptr.Elem()
	}
	return t
}

func structFieldName(t types.Type, idx int) string {
	if strct, ok := deref(types.Unalias(t)).Underlying().(*types.Struct); ok && idx < strct.NumFields() {
		return strct.Field(idx).Name()
	}
	return ""
}

// ruleStrings renders the variadic rule pack: globals become their
// humanized names (Required -> required), constructor calls keep their
// constant arguments (Length(12, 12)).
func ruleStrings(pack ssa.Value) []string {
	var out []string
	for _, v := range ssax.VarargValues(pack) {
		if s := ruleString(v); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func ruleString(v ssa.Value) string {
	for depth := 0; depth < 4; depth++ {
		switch t := v.(type) {
		case *ssa.MakeInterface:
			v = t.X
		case *ssa.UnOp:
			if g, ok := t.X.(*ssa.Global); ok {
				return camelLower(g.Name())
			}
			v = t.X
		case *ssa.Global:
			return camelLower(t.Name())
		case *ssa.Call:
			callee := t.Common().StaticCallee()
			if callee == nil {
				return ""
			}
			var consts []string
			for _, a := range t.Common().Args {
				if c, ok := a.(*ssa.Const); ok && c.Value != nil {
					switch c.Value.Kind() {
					case constant.Int:
						consts = append(consts, c.Value.String())
					case constant.String:
						consts = append(consts, constant.StringVal(c.Value))
					}
				}
			}
			name := camelLower(callee.Name())
			if len(consts) > 0 {
				return fmt.Sprintf("%s(%s)", name, strings.Join(consts, ", "))
			}
			return name
		default:
			return ""
		}
	}
	return ""
}

// camelLower: "NilOrNotEmpty" -> "nil or not empty".
func camelLower(name string) string {
	var b strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}
