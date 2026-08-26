package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/softmapio/softmap/internal/entities"
	"github.com/softmapio/softmap/internal/entrypoints"
	"github.com/softmapio/softmap/internal/filter"
	"github.com/softmapio/softmap/internal/graph"
	"github.com/softmapio/softmap/internal/guards"
	"github.com/softmapio/softmap/internal/loader"
	"github.com/softmapio/softmap/internal/output"
	"github.com/softmapio/softmap/internal/render"
	"github.com/softmapio/softmap/internal/ssax"
)

type scanOptions struct {
	dir        string
	entrypoint string
	all        bool
	outDir     string
	noFilter   bool
	debugTree  bool
	html       bool
	algo       string
	vtaTimeout time.Duration
}

func runScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	var opts scanOptions
	fs.StringVar(&opts.entrypoint, "entrypoint", "", "entrypoint id (or func:<pkg>.<name>) to extract a flow for")
	fs.BoolVar(&opts.all, "all", false, "emit one flow per discovered entrypoint")
	fs.StringVar(&opts.outDir, "o", "", "output directory (default: stdout)")
	fs.BoolVar(&opts.noFilter, "no-filter", false, "emit the raw graph without the noise filter pipeline")
	fs.BoolVar(&opts.debugTree, "debug-tree", false, "print an indented flow tree with filter decisions to stderr")
	fs.BoolVar(&opts.html, "html", false, "emit a self-contained interactive HTML viewer (with -o: <dir>/index.html plus the JSON files)")
	fs.StringVar(&opts.algo, "algo", "auto", "call-graph algorithm: auto|vta|cha")
	fs.DurationVar(&opts.vtaTimeout, "vta-timeout", 120*time.Second, "wall-clock budget for VTA before falling back to CHA")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s scan <path> [flags]\n\nFlags:\n", toolName)
		fs.PrintDefaults()
	}
	// Accept flags on either side of the path: "scan <path> --flags" is the
	// documented form, but stdlib flag stops at the first positional arg.
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return fmt.Errorf("scan: expected a path argument")
	}
	opts.dir = rest[0]
	if err := fs.Parse(rest[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return fmt.Errorf("scan: unexpected extra arguments: %v", fs.Args())
	}
	return scan(&opts)
}

func scan(opts *scanOptions) error {
	start := time.Now()
	p, err := loader.Load(opts.dir)
	if err != nil {
		return err
	}
	funcs := len(ssautil.AllFunctions(p.Prog))
	progress("load", time.Since(start), "module=%s pkgs=%d funcs=%d", p.Module, len(p.Pkgs), funcs)
	for _, w := range p.Warnings {
		warn("%s", w)
	}
	if gen, total := p.GeneratedStats(); total > 0 && gen*2 > total {
		warn("%d%% of module functions are in generated files; flow maps may be sparse (check exclusion rules)", gen*100/total)
	}

	phase := time.Now()
	eps := entrypoints.Discover(p)
	progress("discover", time.Since(phase), "entrypoints=%d", len(eps))
	if len(eps) == 0 && opts.entrypoint == "" {
		warn("0 entrypoints discovered; point at a handler with --entrypoint func:<pkg>.<Name>")
	}

	if opts.entrypoint == "" && !opts.all {
		return listEntrypoints(p, eps)
	}

	var targets []*entrypoints.Entrypoint
	if opts.all {
		if opts.outDir == "" {
			return fmt.Errorf("--all requires -o <dir> (one JSON file per entrypoint)")
		}
		for i := range eps {
			targets = append(targets, &eps[i])
		}
		if len(targets) == 0 {
			return fmt.Errorf("--all: no entrypoints discovered")
		}
	} else {
		ep, err := entrypoints.Resolve(p, eps, opts.entrypoint)
		if err != nil {
			return err
		}
		targets = append(targets, ep)
	}

	phase = time.Now()
	roots := make([]*ssa.Function, len(targets))
	for i, ep := range targets {
		roots[i] = ep.Fn
	}
	cgRes, err := graph.Build(p, roots, graph.Options{Algo: opts.algo, VTATimeout: opts.vtaTimeout})
	if err != nil {
		return err
	}
	progress("callgraph", time.Since(phase), "algo=%s funcs=%d", cgRes.Algo, cgRes.Funcs)
	if cgRes.Warn != "" {
		warn("%s", cgRes.Warn)
	}

	if opts.outDir != "" {
		if err := os.MkdirAll(opts.outDir, 0o755); err != nil {
			return err
		}
	}
	cfg, err := output.LoadConfig(opts.dir)
	if err != nil {
		warn(".softmap.yaml: %v", err)
		cfg = &output.Config{}
	}
	var flows []render.Flow
	var flowDocs []entities.FlowDoc
	for _, ep := range targets {
		doc, err := buildFlowDoc(p, cgRes, ep, opts)
		if err != nil {
			return err
		}
		cfg.Overrides.Apply(doc)
		if opts.html {
			flows = append(flows, render.Flow{Label: ep.ID, Doc: doc})
		}
		flowDocs = append(flowDocs, entities.FlowDoc{File: sanitizeFilename(ep.ID) + ".json", Doc: doc})
		// JSON remains the machine contract: always written, except when the
		// page itself goes to stdout.
		if !opts.html || opts.outDir != "" {
			if err := writeJSON(doc, ep, opts); err != nil {
				return err
			}
		}
	}
	// The entity shelf only makes sense over the whole surface: --all emits
	// index.json (flow list + entities) next to the per-flow files.
	var index *entities.Index
	if opts.all {
		index = entities.Build(p.Module, flowDocs, cfg.Entities, cfg.Merge)
		name := filepath.Join(opts.outDir, "index.json")
		file, err := os.Create(name)
		if err != nil {
			return err
		}
		err = index.WriteJSON(file)
		file.Close()
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %s (%d entities)\n", name, len(index.Entities))
	}
	if opts.html {
		if opts.outDir == "" {
			return render.WriteHTML(os.Stdout, toolName, flows, index)
		}
		name := filepath.Join(opts.outDir, "index.html")
		file, err := os.Create(name)
		if err != nil {
			return err
		}
		defer file.Close()
		if err := render.WriteHTML(file, toolName, flows, index); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %s (%d flows)\n", name, len(flows))
	}
	return nil
}

func buildFlowDoc(p *loader.Program, cgRes *graph.Result, ep *entrypoints.Entrypoint, opts *scanOptions) (*output.Document, error) {
	start := time.Now()
	f, err := graph.Extract(p, cgRes.Graph, cgRes.CHA, ep.Fn, graph.Limits{})
	if err != nil {
		return nil, fmt.Errorf("extracting %s: %w", ep.ID, err)
	}
	rawNodes := len(f.Order)

	var dropped map[string]int
	if !opts.noFilter {
		rules, err := filter.DefaultRules()
		if err != nil {
			return nil, err
		}
		filter.Mark(f, rules, p)
		// Guards run on the marked flow: decisions/exits are added for
		// surviving steps, so the debug tree shows filter decisions AND the
		// flowchart layer together; Prune then removes only marked noise.
		guards.Annotate(f, p)
		if opts.debugTree {
			fmt.Fprintf(os.Stderr, "── %s (before prune) ──\n", ep.ID)
			output.WriteDebugTree(os.Stderr, f, p.Module)
		}
		dropped = filter.Prune(f, p.Module)
	} else if opts.debugTree {
		fmt.Fprintf(os.Stderr, "── %s (raw) ──\n", ep.ID)
		output.WriteDebugTree(os.Stderr, f, p.Module)
	}
	for _, w := range f.Warnings {
		warn("%s", w)
	}

	doc := output.FromFlow(f, ep, p.Module, p.Position(ep.Pos), rawNodes, dropped)
	progress("flow", time.Since(start), "entrypoint=%s raw=%d kept=%d", ep.ID, doc.Stats.RawNodes, doc.Stats.KeptNodes)
	return doc, nil
}

func writeJSON(doc *output.Document, ep *entrypoints.Entrypoint, opts *scanOptions) error {
	if opts.outDir == "" {
		return doc.WriteJSON(os.Stdout)
	}
	name := filepath.Join(opts.outDir, sanitizeFilename(ep.ID)+".json")
	file, err := os.Create(name)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := doc.WriteJSON(file); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", name)
	return nil
}

func sanitizeFilename(id string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, id)
}

func listEntrypoints(p *loader.Program, eps []entrypoints.Entrypoint) error {
	if len(eps) == 0 {
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tFUNC\tPOS")
	for _, ep := range eps {
		fmt.Fprintf(w, "%s\t%s\t%s\n", ep.ID, ssax.TrimModule(ep.FuncName(), p.Module), p.Position(ep.Pos))
	}
	return w.Flush()
}

// progress prints a phase-completion line with counts and elapsed time.
func progress(phase string, elapsed time.Duration, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "phase=%s elapsed=%s %s\n", phase, elapsed.Round(time.Millisecond), fmt.Sprintf(format, args...))
}

// warn prints an honesty warning; warnings are for anything that looks wrong
// or degrades the result, so a run is never silently ambiguous.
func warn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "warning: %s\n", fmt.Sprintf(format, args...))
}
