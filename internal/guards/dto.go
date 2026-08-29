package guards

import (
	"go/types"
	"reflect"
	"strings"

	"golang.org/x/tools/go/ssa"

	"github.com/softmapio/softmap/internal/graph"
	"github.com/softmapio/softmap/internal/ssax"
)

// Request/response DTO extraction: the data contract analysts read. The
// request type comes from bind/unmarshal calls (echo Bind, gin ShouldBind*,
// json/xml.Unmarshal) or, for gRPC-shaped handlers, straight from the
// signature; the response from the success response payload or the
// signature result.

var bindMethods = map[string]map[string]bool{
	"github.com/labstack/echo": {"Bind": true},
	"github.com/gin-gonic/gin": {
		"Bind": true, "BindJSON": true, "BindXML": true, "BindQuery": true, "BindUri": true,
		"ShouldBind": true, "ShouldBindJSON": true, "ShouldBindXML": true,
		"ShouldBindQuery": true, "ShouldBindUri": true, "ShouldBindBodyWith": true,
	},
}

const maxDTOFields = 40

// ExtractDTOs fills the root node's request/response contracts, including
// per-field validation rules gathered from the handler body.
func ExtractDTOs(f *graph.Flow) {
	fn := f.Root.Fn
	if fn == nil {
		return
	}
	defer func() {
		if f.Root.RequestDTO == nil {
			return
		}
		byField := FieldValidations(fn)
		for i := range f.Root.RequestDTO.Fields {
			fld := &f.Root.RequestDTO.Fields[i]
			if extra, ok := byField[fld.Name]; ok {
				fld.Rules = mergeRules(fld.Rules, extra)
			}
		}
	}()
	// gRPC-shaped signature: (ctx, *Req) (*Resp, error). Stdlib types are
	// transport plumbing (net/http.Request), never the data contract - a
	// plain http handler's request comes from the Decode/bind scan below.
	params := fn.Signature.Params()
	if params.Len() >= 1 {
		last := params.At(params.Len() - 1).Type()
		if !stdlibType(last) {
			if dto := dtoFromType(last, "json"); dto != nil {
				f.Root.RequestDTO = dto
			}
		}
	}
	results := fn.Signature.Results()
	for i := 0; i < results.Len(); i++ {
		if !ssax.IsErrorType(results.At(i).Type()) && !stdlibType(results.At(i).Type()) {
			if dto := dtoFromType(results.At(i).Type(), "json"); dto != nil {
				f.Root.ResponseDTO = dto
			}
		}
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
			args := site.Common().Args[info.ArgOffset:]
			switch {
			// Framework binds: the pointed-to struct is the request.
			case bindMethods[ssax.NormalizePkg(info.Pkg)][info.Name] && len(args) >= 1:
				if dto := dtoFromValue(args[len(args)-1], "json"); dto != nil && f.Root.RequestDTO == nil {
					f.Root.RequestDTO = dto
				}
			// The stdlib streaming idiom: json.NewDecoder(r.Body).Decode(&req).
			case (info.Pkg == "encoding/json" || info.Pkg == "encoding/xml") && info.Name == "Decode" && len(args) == 1:
				format := "json"
				if info.Pkg == "encoding/xml" {
					format = "xml"
				}
				if dto := dtoFromValue(args[0], format); dto != nil && f.Root.RequestDTO == nil {
					f.Root.RequestDTO = dto
				}
			case (info.Pkg == "encoding/json" || info.Pkg == "encoding/xml") && info.Name == "Unmarshal" && len(args) == 2:
				format := "json"
				if info.Pkg == "encoding/xml" {
					format = "xml"
				}
				if dto := dtoFromValue(args[1], format); dto != nil && f.Root.RequestDTO == nil {
					f.Root.RequestDTO = dto
				}
			// Success response payload.
			case responseMethods[ssax.NormalizePkg(info.Pkg)][info.Name] && len(args) >= 2:
				if status, ok := respConstInt(args[0], 2); ok && status < 400 {
					if dto := dtoFromValue(args[1], "json"); dto != nil && f.Root.ResponseDTO == nil {
						f.Root.ResponseDTO = dto
					}
				}
			// Hand-rolled respond helpers (RespondWithJSON(w, code, payload))
			// are the norm in plain net/http services: a *JSON*-named call
			// with a constant status and a payload argument right after it.
			case strings.Contains(strings.ToLower(info.Name), "json") && len(args) >= 2:
				for i := 0; i < len(args)-1; i++ {
					if status, ok := respConstInt(args[i], 2); ok {
						if status < 400 {
							if dto := dtoFromValue(args[i+1], "json"); dto != nil && f.Root.ResponseDTO == nil {
								f.Root.ResponseDTO = dto
							}
						}
						break
					}
				}
			}
		}
	}
}

// stdlibType: the named type (after pointer/alias peeling) lives in the
// standard library - first import-path segment has no dot.
func stdlibType(t types.Type) bool {
	t = types.Unalias(t)
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	return !strings.Contains(strings.Split(named.Obj().Pkg().Path(), "/")[0], ".")
}

func dtoFromValue(v ssa.Value, format string) *graph.DTOInfo {
	switch t := v.(type) {
	case *ssa.MakeInterface:
		return dtoFromValue(t.X, format)
	case *ssa.UnOp:
		return dtoFromType(t.X.Type(), format)
	default:
		return dtoFromType(v.Type(), format)
	}
}

func dtoFromType(t types.Type, format string) *graph.DTOInfo {
	t = types.Unalias(t)
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil
	}
	strct, ok := named.Underlying().(*types.Struct)
	if !ok {
		return nil
	}
	dto := &graph.DTOInfo{Type: typeShort(named), Format: format}
	collectFields(strct, "", &dto.Fields)
	if len(dto.Fields) == 0 {
		return nil
	}
	if len(dto.Fields) > maxDTOFields {
		dto.Fields = dto.Fields[:maxDTOFields]
		dto.Truncated = true
	}
	return dto
}

func collectFields(strct *types.Struct, prefix string, out *[]graph.DTOField) {
	for i := 0; i < strct.NumFields(); i++ {
		f := strct.Field(i)
		tag := reflect.StructTag(strct.Tag(i))
		wire := firstTag(tag, "json", "xml", "form", "query", "param")
		if wire == "-" {
			continue
		}
		// Embedded structs flatten, like encoding/json treats them.
		if f.Embedded() {
			if inner, ok := types.Unalias(f.Type()).(*types.Named); ok {
				if is, ok := inner.Underlying().(*types.Struct); ok {
					collectFields(is, prefix, out)
					continue
				}
			}
		}
		if !f.Exported() {
			continue
		}
		*out = append(*out, graph.DTOField{
			Name:  prefix + f.Name(),
			Tag:   wire,
			Type:  types.TypeString(f.Type(), func(p *types.Package) string { return p.Name() }),
			Rules: rulesTag(tag),
		})
	}
}

// rulesTag keeps the whole rule list ("required,len=12"), unlike wire-name
// tags where only the first comma token is the name.
func rulesTag(tag reflect.StructTag) string {
	for _, k := range []string{"validate", "binding"} {
		if v, ok := tag.Lookup(k); ok && v != "-" {
			return strings.ReplaceAll(v, ",", ", ")
		}
	}
	return ""
}

func firstTag(tag reflect.StructTag, keys ...string) string {
	for _, k := range keys {
		if v, ok := tag.Lookup(k); ok {
			return strings.Split(v, ",")[0]
		}
	}
	return ""
}

// mergeRules joins rule lists from tags and validation calls without
// repeating rules both declare (required, required).
func mergeRules(a, b string) string {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(a+", "+b, ", ") {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	return strings.Join(out, ", ")
}

func typeShort(named *types.Named) string {
	if named.Obj().Pkg() != nil {
		return named.Obj().Pkg().Name() + "." + named.Obj().Name()
	}
	return named.Obj().Name()
}
