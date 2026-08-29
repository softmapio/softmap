// Package loader loads a Go module once via go/packages and builds SSA for
// it. Nothing else in the program touches go/packages: every later phase
// works off the single *Program produced here.
package loader

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/tools/go/ast/astutil"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// FileClass classifies a source file for pre-analysis exclusion. Generated
// and vendored files cannot be dropped at load time without breaking
// type-checking (a .pb.go file may define types that handwritten code uses),
// so they are classified immediately after load and excluded from every
// user-visible phase instead: entrypoint discovery, flow output, and stats.
type FileClass uint8

const (
	Normal FileClass = iota
	Generated
	Vendor
)

func (c FileClass) String() string {
	switch c {
	case Generated:
		return "generated"
	case Vendor:
		return "vendor"
	default:
		return "normal"
	}
}

// Program is the fully loaded and SSA-built analysis subject.
type Program struct {
	Prog     *ssa.Program
	Pkgs     []*packages.Package
	Fset     *token.FileSet
	Module   string // module path of the analyzed module
	Dir      string // absolute directory the module was loaded from
	Warnings []string

	fileClass map[string]FileClass
	modulePkg map[string]bool // package paths belonging to the analyzed module

	astIndexOnce sync.Once
	astIndex     map[*token.File]*ast.File
}

// Load loads the module rooted at dir and builds SSA with generic
// instantiation. Test files never load (Tests: false), so they cannot
// inflate any later phase.
func Load(dir string) (*Program, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedModule,
		Dir:   abs,
		Tests: false,
	}
	var vendorWarning string
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil && strings.Contains(err.Error(), "inconsistent vendoring") {
		// The repo's vendor/ is out of sync with go.mod (deps bumped without
		// `go mod vendor`). Analysis does not need the vendor dir - retry
		// resolving dependencies from the module cache instead of failing.
		cfg.BuildFlags = append(cfg.BuildFlags, "-mod=mod")
		vendorWarning = "vendor/ is out of sync with go.mod; analyzed with -mod=mod (module cache) - run 'go mod vendor' in the repo to fix"
		pkgs, err = packages.Load(cfg, "./...")
	}
	if err != nil {
		return nil, fmt.Errorf("loading packages: %w", err)
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("no Go packages found under %s", abs)
	}

	p := &Program{
		Pkgs:      pkgs,
		Dir:       abs,
		fileClass: make(map[string]FileClass),
		modulePkg: make(map[string]bool),
	}
	if vendorWarning != "" {
		p.Warnings = append(p.Warnings, vendorWarning)
	}

	broken := 0
	for _, pkg := range pkgs {
		p.modulePkg[pkg.PkgPath] = true
		if p.Module == "" && pkg.Module != nil {
			p.Module = pkg.Module.Path
		}
		if len(pkg.Errors) > 0 {
			broken++
		}
		for _, e := range pkg.Errors {
			if len(p.Warnings) < 20 {
				p.Warnings = append(p.Warnings, fmt.Sprintf("package %s: %v", pkg.PkgPath, e))
			}
		}
	}
	if broken == len(pkgs) && broken > 0 {
		loadErr := fmt.Errorf("all %d packages failed to load; first error: %s", broken, p.Warnings[0])
		if hint := newerGoHint(strings.Join(p.Warnings, "\n")); hint != "" {
			loadErr = fmt.Errorf("%s\n%s", loadErr, hint)
		}
		return nil, loadErr
	}
	if broken > 0 {
		// Say what the errors cost, not just that they exist: ill-typed
		// packages get no SSA, and neither does anything importing them.
		p.Warnings = append(p.Warnings, fmt.Sprintf(
			"%d of %d packages failed to type-check: their functions AND those of packages importing them are invisible to analysis (entrypoints there cannot be discovered) - fix the compile errors above and rescan",
			broken, len(pkgs)))
		if hint := newerGoHint(strings.Join(p.Warnings, "\n")); hint != "" {
			p.Warnings = append(p.Warnings, hint)
		}
	}
	if p.Module == "" {
		return nil, fmt.Errorf("%s is not inside a Go module (no module information loaded)", abs)
	}

	p.classifyFiles()

	prog, _ := ssautil.Packages(pkgs, ssa.InstantiateGenerics)
	prog.Build()
	p.Prog = prog
	p.Fset = prog.Fset
	return p, nil
}

// classifyFiles records a FileClass for every compiled file of the module's
// own packages. Dependencies are classified lazily by path in FuncClass.
func (p *Program) classifyFiles() {
	for _, pkg := range p.Pkgs {
		for _, f := range pkg.Syntax {
			name := p.positionFile(pkg.Fset, f.Pos())
			if name == "" {
				continue
			}
			p.fileClass[name] = classifyFile(name, f)
		}
	}
}

func (p *Program) positionFile(fset *token.FileSet, pos token.Pos) string {
	if !pos.IsValid() {
		return ""
	}
	return fset.Position(pos).Filename
}

func classifyFile(name string, f *ast.File) FileClass {
	if isVendorPath(name) {
		return Vendor
	}
	base := filepath.Base(name)
	if strings.HasSuffix(base, "_gen.go") || strings.HasSuffix(base, ".pb.go") || ast.IsGenerated(f) {
		return Generated
	}
	return Normal
}

func isVendorPath(name string) bool {
	return strings.Contains(filepath.ToSlash(name), "/vendor/")
}

// FuncClass reports the FileClass of the file declaring fn. Synthetic
// functions without a position (wrappers, bound methods) are Normal so they
// never mask real code they forward to.
func (p *Program) FuncClass(fn *ssa.Function) FileClass {
	pos := fn.Pos()
	if !pos.IsValid() {
		return Normal
	}
	name := p.Fset.Position(pos).Filename
	if c, ok := p.fileClass[name]; ok {
		return c
	}
	if isVendorPath(name) {
		return Vendor
	}
	return Normal
}

// InModule reports whether fn belongs to a package of the analyzed module.
func (p *Program) InModule(fn *ssa.Function) bool {
	pkg := FuncPackage(fn)
	if pkg == "" {
		return false
	}
	return p.modulePkg[pkg]
}

// PkgInModule reports whether a package path belongs to the analyzed module.
func (p *Program) PkgInModule(pkg string) bool { return p.modulePkg[pkg] }

// FuncPackage returns the package path of fn, following Origin for generic
// instantiations and Parent for anonymous functions.
func FuncPackage(fn *ssa.Function) string {
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

// GeneratedStats counts module-local source functions by class, feeding the
// "most of your code is generated" honesty warning.
func (p *Program) GeneratedStats() (generated, total int) {
	for fn := range ssautil.AllFunctions(p.Prog) {
		if !p.InModule(fn) || !fn.Pos().IsValid() {
			continue
		}
		total++
		if p.FuncClass(fn) == Generated {
			generated++
		}
	}
	return generated, total
}

// IfCondition renders the source text of the if-statement enclosing pos
// (typically an ssa.If condition's position) and returns the statement's own
// position for deduplication: `&&`/`||` conditions lower to several SSA
// branches that all belong to one source `if`. Text is verbatim (including
// non-ASCII), single-line, capped for card display.
func (p *Program) IfCondition(pos token.Pos) (text string, stmtPos token.Pos, ok bool) {
	if !pos.IsValid() {
		return "", token.NoPos, false
	}
	f := p.astFileAt(pos)
	if f == nil {
		return "", token.NoPos, false
	}
	path, _ := astutil.PathEnclosingInterval(f, pos, pos)
	for _, n := range path { // innermost IfStmt first
		ifStmt, isIf := n.(*ast.IfStmt)
		if !isIf {
			continue
		}
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, p.Fset, ifStmt.Cond); err != nil {
			return "", token.NoPos, false
		}
		// Verbatim and complete: analysts read the condition as the
		// business rule - truncating it hides exactly the part they ask
		// about. The card wraps; the panel shows it too.
		text := strings.Join(strings.Fields(buf.String()), " ")
		return text, ifStmt.Pos(), true
	}
	return "", token.NoPos, false
}

// IfConditionOfBody finds the If statement whose BODY (or else-branch)
// contains pos and renders its condition. Used for gates: their condition
// value (a phi, a stored bool) often carries no position inside the if, so
// the anchor comes from the first instruction of a branch instead. Walking
// outward and requiring pos inside Body/Else skips a nested if whose
// condition happens to open the branch.
func (p *Program) IfConditionOfBody(pos token.Pos) (text string, stmtPos token.Pos, ok bool) {
	if !pos.IsValid() {
		return "", token.NoPos, false
	}
	f := p.astFileAt(pos)
	if f == nil {
		return "", token.NoPos, false
	}
	path, _ := astutil.PathEnclosingInterval(f, pos, pos)
	for _, n := range path { // innermost first
		ifStmt, isIf := n.(*ast.IfStmt)
		if !isIf {
			continue
		}
		inBody := ifStmt.Body != nil && ifStmt.Body.Lbrace <= pos && pos <= ifStmt.Body.Rbrace
		inElse := ifStmt.Else != nil && ifStmt.Else.Pos() <= pos && pos <= ifStmt.Else.End()
		if !inBody && !inElse {
			continue
		}
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, p.Fset, ifStmt.Cond); err != nil {
			return "", token.NoPos, false
		}
		// Verbatim and complete: analysts read the condition as the
		// business rule - truncating it hides exactly the part they ask
		// about. The card wraps; the panel shows it too.
		text := strings.Join(strings.Fields(buf.String()), " ")
		return text, ifStmt.Pos(), true
	}
	return "", token.NoPos, false
}

// astFileAt finds the module syntax tree containing pos (lazily indexed).
func (p *Program) astFileAt(pos token.Pos) *ast.File {
	p.astIndexOnce.Do(func() {
		p.astIndex = make(map[*token.File]*ast.File)
		for _, pkg := range p.Pkgs {
			for _, f := range pkg.Syntax {
				if tf := pkg.Fset.File(f.Pos()); tf != nil {
					p.astIndex[tf] = f
				}
			}
		}
	})
	if tf := p.Fset.File(pos); tf != nil {
		return p.astIndex[tf]
	}
	return nil
}

// Position formats fn's declaration position relative to the module root,
// e.g. "service/pull.go:44". Unknown positions return "".
func (p *Program) Position(pos token.Pos) string {
	if !pos.IsValid() {
		return ""
	}
	position := p.Fset.Position(pos)
	name := position.Filename
	if rel, err := filepath.Rel(p.Dir, name); err == nil && !strings.HasPrefix(rel, "..") {
		name = filepath.ToSlash(rel)
	}
	return fmt.Sprintf("%s:%d", name, position.Line)
}

// newerGoVersion matches the go/types complaint emitted when a package's go
// directive is newer than the toolchain this binary was built with.
var newerGoVersion = regexp.MustCompile(`requires newer Go version go(\d+)\.(\d+)`)

// newerGoHint turns that complaint into an actionable fix. The analyzer can
// only type-check language versions up to its own build toolchain, so the
// remedy is always to rebuild the binary, never to change the target repo.
func newerGoHint(msg string) string {
	m := newerGoVersion.FindStringSubmatch(msg)
	if m == nil {
		return ""
	}
	return fmt.Sprintf(
		"this softmap binary was built with %s and cannot type-check code that requires go%s.%s - rebuild it with a newer toolchain:\n"+
			"  GOTOOLCHAIN=go%s.%s.0 go install github.com/softmapio/softmap/cmd/softmap@latest",
		runtime.Version(), m[1], m[2], m[1], m[2])
}
