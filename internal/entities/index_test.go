package entities_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/softmapio/softmap/internal/entities"
	"github.com/softmapio/softmap/internal/output"
)

var update = flag.Bool("update", false, "rewrite the index golden with current output")

// TestIndexGolden builds the entity index from the per-flow goldens - the
// same documents scan --all writes - and pins index.json byte-for-byte plus
// the milestone content assertions.
func TestIndexGolden(t *testing.T) {
	goldenDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "golden"))
	if err != nil {
		t.Fatal(err)
	}
	names, err := os.ReadDir(goldenDir)
	if err != nil {
		t.Fatal(err)
	}
	var flows []entities.FlowDoc
	var module string
	for _, e := range names {
		n := e.Name()
		if !strings.HasSuffix(n, ".json") || n == "index.json" || strings.HasSuffix(n, ".raw.json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(goldenDir, n))
		if err != nil {
			t.Fatal(err)
		}
		var doc output.Document
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		module = doc.Module
		flows = append(flows, entities.FlowDoc{File: n, Doc: &doc})
	}
	if len(flows) != 10 {
		t.Fatalf("loaded %d flow goldens, want 10", len(flows))
	}
	sort.Slice(flows, func(i, j int) bool { return flows[i].File < flows[j].File })

	cfg, err := output.LoadConfig(filepath.Join("..", "..", "testdata", "toyshop"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Entities) == 0 {
		t.Fatal("fixture .softmap.yaml must define at least one entity name")
	}
	idx := entities.Build(module, flows, cfg.Entities, cfg.Merge)

	var buf bytes.Buffer
	if err := idx.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(goldenDir, "index.json")
	if *update {
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing golden index.json (run: go test ./internal/entities -update): %v", err)
		}
		if !bytes.Equal(buf.Bytes(), want) {
			t.Errorf("index.json differs from golden:\n--- got ---\n%s\n--- want ---\n%s", buf.Bytes(), want)
		}
	}

	assertIndex(t, idx)
}

// assertIndex pins the milestone success criteria independently of golden
// bytes: satellite merge, access kinds, the override name, the honest Other
// shelf.
func assertIndex(t *testing.T, idx *entities.Index) {
	t.Helper()
	byKey := map[string]entities.Entity{}
	for _, e := range idx.Entities {
		byKey[e.Key] = e
	}

	// order: tables merged by prefix, name overridden with the • marker data.
	order, ok := byKey["order"]
	if !ok {
		t.Fatalf("no order entity; got %v", keys(idx))
	}
	if order.Name != "Заказ" || !order.NameOverridden {
		t.Errorf("order name = %q (overridden=%v), want Заказ via .softmap.yaml", order.Name, order.NameOverridden)
	}
	wantTables := []string{"order_items", "orders"}
	if strings.Join(order.Tables, ",") != strings.Join(wantTables, ",") {
		t.Errorf("order tables = %v, want %v (satellite merged)", order.Tables, wantTables)
	}
	accessOf := func(e entities.Entity, flowID string) string {
		for _, f := range e.Flows {
			if f.ID == flowID {
				return strings.Join(f.Access, "|")
			}
		}
		return "<flow missing>"
	}
	if got := accessOf(order, "http:POST:/orders"); got != "creates|publishes orders.created" {
		t.Errorf("order access for POST /orders = %q, want creates|publishes orders.created", got)
	}
	if got := accessOf(order, "http:GET:/orders/{id}"); got != "reads" {
		t.Errorf("order access for GET /orders/{id} = %q, want reads", got)
	}

	// product: both access kinds from SQL verbs.
	product, ok := byKey["product"]
	if !ok {
		t.Fatalf("no product entity; got %v", keys(idx))
	}
	if product.NameOverridden || product.Name != "Product" {
		t.Errorf("product name = %q (overridden=%v), want derived \"Product\"", product.Name, product.NameOverridden)
	}
	if got := accessOf(product, "http:GET:/products"); got != "reads" {
		t.Errorf("product access for GET /products = %q, want reads", got)
	}
	if got := accessOf(product, "http:POST:/products"); got != "creates" {
		t.Errorf("product access for POST /products = %q, want creates", got)
	}

	// The shelf must be rich enough for a real home screen: order, product,
	// audit at minimum, plus the honest Other shelf.
	if len(idx.Entities) < 3 {
		t.Errorf("only %d entities: %v", len(idx.Entities), keys(idx))
	}
	otherIDs := make([]string, 0, len(idx.Other))
	for _, f := range idx.Other {
		otherIDs = append(otherIDs, f.ID)
	}
	if strings.Join(otherIDs, ",") != "http:ANY:/healthz" {
		t.Errorf("Other shelf = %v, want exactly the signal-less healthz flow", otherIDs)
	}

	if len(idx.Flows) != 10 {
		t.Errorf("flow list has %d rows, want 10", len(idx.Flows))
	}
}

func keys(idx *entities.Index) []string {
	out := make([]string, len(idx.Entities))
	for i, e := range idx.Entities {
		out[i] = e.Key
	}
	return out
}
