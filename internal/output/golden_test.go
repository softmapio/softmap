package output

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/ssa"

	"github.com/softmapio/softmap/internal/entrypoints"
	"github.com/softmapio/softmap/internal/filter"
	"github.com/softmapio/softmap/internal/graph"
	"github.com/softmapio/softmap/internal/guards"
	"github.com/softmapio/softmap/internal/loader"
)

var update = flag.Bool("update", false, "rewrite golden files with current output")

// TestGolden runs the full pipeline (load → discover → callgraph → extract →
// filter → serialize) on the toyshop fixture and compares every emitted
// document byte-for-byte with the goldens in testdata/golden. This is the
// contract test for the schema and the filter defaults: refresh deliberately
// with `go test ./internal/output -update`.
func TestGolden(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "toyshop"))
	if err != nil {
		t.Fatal(err)
	}
	goldenDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "golden"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := loader.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	eps := entrypoints.Discover(p)
	if len(eps) != 10 {
		t.Fatalf("discovered %d entrypoints, want 10", len(eps))
	}
	roots := make([]*ssa.Function, len(eps))
	for i := range eps {
		roots[i] = eps[i].Fn
	}
	cgRes, err := graph.Build(p, roots, graph.Options{Algo: "auto"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cgRes.Algo != "vta" {
		t.Fatalf("expected VTA on the fixture, got %s (%s)", cgRes.Algo, cgRes.Warn)
	}
	rules, err := filter.DefaultRules()
	if err != nil {
		t.Fatal(err)
	}
	overrides, err := LoadOverrides(dir)
	if err != nil {
		t.Fatalf("LoadOverrides: %v", err)
	}
	if len(overrides) == 0 {
		t.Fatal("fixture .softmap.yaml must define at least one override")
	}

	run := func(ep *entrypoints.Entrypoint, filtered bool) (jsonOut, tree []byte) {
		t.Helper()
		f, err := graph.Extract(p, cgRes.Graph, cgRes.CHA, ep.Fn, graph.Limits{})
		if err != nil {
			t.Fatalf("Extract(%s): %v", ep.ID, err)
		}
		raw := len(f.Order)
		var dropped map[string]int
		var treeBuf bytes.Buffer
		if filtered {
			filter.Mark(f, rules, p)
			guards.Annotate(f, p)
			WriteDebugTree(&treeBuf, f, p.Module)
			dropped = filter.Prune(f, p.Module)
		} else {
			WriteDebugTree(&treeBuf, f, p.Module)
		}
		doc := FromFlow(f, ep, p.Module, p.Position(ep.Pos), raw, dropped)
		overrides.Apply(doc)
		var buf bytes.Buffer
		if err := doc.WriteJSON(&buf); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes(), treeBuf.Bytes()
	}

	check := func(name string, got []byte) {
		t.Helper()
		path := filepath.Join(goldenDir, name)
		if *update {
			if err := os.MkdirAll(goldenDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing golden %s (run: go test ./internal/output -update): %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from golden:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
		}
	}

	for i := range eps {
		ep := &eps[i]
		jsonOut, tree := run(ep, true)
		check(sanitize(ep.ID)+".json", jsonOut)
		if ep.ID == "http:POST:/orders" {
			check("http-POST--orders.debug-tree.txt", tree)
			rawJSON, _ := run(ep, false)
			check("http-POST--orders.raw.json", rawJSON)
			assertFlowContent(t, jsonOut)
		}
		if ep.ID == "http:POST:/orders/:id/approve" {
			assertGuardContent(t, jsonOut)
		}
	}
}

// assertFlowContent pins the milestone success criteria for the main flow
// independently of golden bytes, so a careless -update cannot silently
// regress them.
func assertFlowContent(t *testing.T, jsonOut []byte) {
	t.Helper()
	var doc struct {
		Nodes []struct {
			Func       string `json:"func"`
			Pos        string `json:"pos"`
			Kind       string `json:"kind"`
			Resolution string `json:"resolution"`
			Effects    []struct {
				Type  string  `json:"type"`
				Topic *string `json:"topic"`
			} `json:"effects"`
			Async bool `json:"async"`
		} `json:"nodes"`
		Stats struct {
			RawNodes  int `json:"raw_nodes"`
			KeptNodes int `json:"kept_nodes"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(jsonOut, &doc); err != nil {
		t.Fatalf("re-parsing emitted JSON: %v", err)
	}

	effectTypes := map[string]int{}
	multi, dynamic, async := 0, 0, 0
	var kafkaTopic string
	for _, n := range doc.Nodes {
		// Noise must be gone entirely — among steps; an exit naming the
		// failed helper ("HTTP 422 (validateOrder failed)") is an outcome.
		// validateOrder itself is NOT noise anymore: its result feeds a
		// rendered decision, and guard evidence is exempt from filtering.
		if n.Kind == "step" || n.Kind == "terminal" {
			for _, noise := range []string{"pkg/log", "pkg/metrics", "config.", "wrapErr"} {
				if strings.Contains(n.Func, noise) {
					t.Errorf("noise node survived filtering: %s", n.Func)
				}
			}
		}
		// The generated file loads for type soundness but must never
		// surface in a flow.
		if strings.Contains(n.Func, "toyshop/gen") || strings.Contains(n.Pos, ".pb.go") {
			t.Errorf("generated-file node surfaced: %s (%s)", n.Func, n.Pos)
		}
		for _, e := range n.Effects {
			effectTypes[e.Type]++
			if e.Type == "kafka" && e.Topic != nil {
				kafkaTopic = *e.Topic
			}
		}
		if n.Resolution == "static-multi" {
			multi++
		}
		if n.Resolution == "dynamic" {
			dynamic++
		}
		if n.Async {
			async++
		}
	}
	if effectTypes["sql"] != 3 || effectTypes["redis"] != 1 || effectTypes["kafka"] != 1 || effectTypes["http"] != 3 {
		t.Errorf("effect counts = %v, want sql:3 (orders + order_items + audit) redis:1 kafka:1 http:3 (Post, NewRequest, Do)", effectTypes)
	}
	if kafkaTopic != "orders.created" {
		t.Errorf("kafka topic = %q, want orders.created", kafkaTopic)
	}
	if multi != 2 {
		t.Errorf("static-multi nodes = %d, want 2 (both Notifier implementations)", multi)
	}
	if dynamic != 1 {
		t.Errorf("dynamic terminals = %d, want 1 (dyn.Hook)", dynamic)
	}
	if async == 0 {
		t.Error("no async node; go s.audit(...) must tag its subtree")
	}
	if doc.Stats.KeptNodes >= doc.Stats.RawNodes {
		t.Errorf("kept %d >= raw %d; the filter did nothing", doc.Stats.KeptNodes, doc.Stats.RawNodes)
	}
}

// assertGuardContent pins milestone 1.6: exactly the semantic guards became
// decisions, mechanical propagation produced fallible badges and zero
// decisions, provenance points at the fetch, and the non-ASCII message
// survives byte-exact.
func assertGuardContent(t *testing.T, jsonOut []byte) {
	t.Helper()
	var doc struct {
		SchemaVersion string `json:"schema_version"`
		Nodes         []struct {
			Func      string   `json:"func"`
			Kind      string   `json:"kind"`
			Condition string   `json:"condition"`
			Gate      bool     `json:"gate"`
			Uses      []string `json:"uses"`
			Fallible  bool     `json:"fallible"`
			Error     *struct {
				Kind    string `json:"kind"`
				Name    string `json:"name"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"nodes"`
		Edges []struct {
			Kind string `json:"kind"`
		} `json:"edges"`
	}
	if err := json.Unmarshal(jsonOut, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != "2" {
		t.Errorf("schema_version = %q, want 2", doc.SchemaVersion)
	}
	var decisions, gates, exits, fallible int
	var conditions, sentinels, messages []string
	usesOK := false
	gateOK := false
	for _, n := range doc.Nodes {
		switch n.Kind {
		case "decision":
			if n.Gate {
				gates++
				// The gate must have claimed its gated call: the audit SQL
				// hangs under the condition that triggers it.
				if n.Condition == "o.Qty > 10" {
					gateOK = true
				}
				break
			}
			decisions++
			conditions = append(conditions, n.Condition)
			for _, u := range n.Uses {
				if u == "fetchResellers" {
					usesOK = true
				}
			}
		case "exit":
			exits++
			if n.Error != nil && n.Error.Kind == "sentinel" {
				sentinels = append(sentinels, n.Error.Name)
			}
			if n.Error != nil && n.Error.Kind == "message" {
				messages = append(messages, n.Error.Message)
			}
		}
		if n.Fallible {
			fallible++
		}
	}
	// 4 in the service + the handler-level rejection (void gin handler:
	// `if err := svc.ApproveOrder(); err != nil { c.JSON(500) }`).
	if decisions != 5 {
		t.Errorf("decisions = %d, want exactly 5: %v", decisions, conditions)
	}
	if gates != 1 || !gateOK {
		t.Errorf("gates = %d (gateOK=%v), want exactly one gate decision \"o.Qty > 10\"", gates, gateOK)
	}
	if exits != 5 {
		t.Errorf("exits = %d, want 5", exits)
	}
	if !usesOK {
		t.Error("no decision carries uses=[fetchResellers] provenance")
	}
	// ErrApprovalStopped arrives through a wordless "%w: %w" wrap — the
	// classifier must surface the sentinel, not an "…: …" message.
	wantSent := map[string]bool{"ErrNoResellersAttached": true, "ErrApprovalStopped": true}
	if len(sentinels) != 2 || !wantSent[sentinels[0]] || !wantSent[sentinels[1]] {
		t.Errorf("sentinel exits = %v, want ErrNoResellersAttached + ErrApprovalStopped", sentinels)
	}
	wantMsg := "нет прав на заказ %s"
	found := false
	for _, m := range messages {
		if m == wantMsg {
			found = true
		}
	}
	if !found {
		t.Errorf("non-ASCII message not byte-exact; messages = %q", messages)
	}
	if fallible < 2 {
		t.Errorf("fallible nodes = %d, want >= 2 (FindOrder, CacheOrder targets)", fallible)
	}
	var passes, fails int
	for _, e := range doc.Edges {
		if e.Kind == "pass" {
			passes++
		}
		if e.Kind == "fail" {
			fails++
		}
	}
	if fails != 5 || passes < 3 {
		t.Errorf("edge kinds: fail=%d (want 5) pass=%d (want >=3)", fails, passes)
	}
}

func sanitize(id string) string {
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
