package entrypoints

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/softmapio/softmap/internal/loader"
)

var fixture *loader.Program

func loadFixture(t *testing.T) *loader.Program {
	t.Helper()
	if fixture != nil {
		return fixture
	}
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "toyshop"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := loader.Load(dir)
	if err != nil {
		t.Fatalf("Load(toyshop): %v", err)
	}
	fixture = p
	return p
}

func TestDiscoverToyshop(t *testing.T) {
	p := loadFixture(t)
	eps := Discover(p)

	byID := map[string]Entrypoint{}
	for _, ep := range eps {
		byID[ep.ID] = ep
		t.Logf("discovered: %s -> %s", ep.ID, ep.FuncName())
	}

	tests := []struct {
		id       string
		kind     string
		funcPart string
	}{
		{"http:POST:/orders", "http", "CreateOrder"},
		{"http:POST:/orders/:id/approve", "http", "ApproveOrder"},
		{"http:POST:/reports/:id/callback", "http", "ReportCallback"},
		{"grpc:Orders/GetOrder", "grpc", "grpcserver.Server).GetOrder"},
		{"http:GET:/orders/:id", "http", "GetOrder"},
		{"http:ANY:/healthz", "http", "healthz"},
		{"kafka:orders.created:consumer.Run", "kafka", "consumer.Run"},
		// chi group prefix: registered as Post("/login") inside Route("/auth").
		{"http:POST:/auth/login", "http", "chiapi.Handler).Login"},
		{"http:GET:/products", "http", "chiapi.Handler).ListProducts"},
		{"http:POST:/products", "http", "chiapi.Handler).CreateProduct"},
	}
	for _, tt := range tests {
		ep, ok := byID[tt.id]
		if !ok {
			t.Errorf("entrypoint %q not discovered", tt.id)
			continue
		}
		if ep.Kind != tt.kind {
			t.Errorf("%s: kind = %q, want %q", tt.id, ep.Kind, tt.kind)
		}
		if !strings.Contains(ep.FuncName(), tt.funcPart) {
			t.Errorf("%s: func = %q, want containing %q", tt.id, ep.FuncName(), tt.funcPart)
		}
	}
	if len(eps) != len(tests) {
		t.Errorf("discovered %d entrypoints, want %d: %v", len(eps), len(tests), ids(eps))
	}
}

func TestResolveEscapeHatch(t *testing.T) {
	p := loadFixture(t)
	eps := Discover(p)

	// A discovered ID resolves to itself.
	ep, err := Resolve(p, eps, "http:POST:/orders")
	if err != nil {
		t.Fatalf("Resolve(discovered id): %v", err)
	}
	if ep.ID != "http:POST:/orders" {
		t.Errorf("resolved ID = %q", ep.ID)
	}

	// An undiscovered function resolves via func: suffix match.
	ep, err = Resolve(p, eps, "func:service.Service).GetOrder")
	if err != nil {
		t.Fatalf("Resolve(func suffix): %v", err)
	}
	if !strings.Contains(ep.FuncName(), "GetOrder") {
		t.Errorf("resolved func = %q, want GetOrder", ep.FuncName())
	}

	// Unknown names produce a helpful error.
	if _, err = Resolve(p, eps, "func:NoSuchFunction"); err == nil {
		t.Error("Resolve(unknown) succeeded, want error")
	}
}

func ids(eps []Entrypoint) []string {
	out := make([]string, len(eps))
	for i, ep := range eps {
		out[i] = ep.ID
	}
	return out
}
