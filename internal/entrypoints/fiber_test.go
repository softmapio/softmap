package entrypoints

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/softmapio/softmap/internal/loader"
)

var fiberFixture *loader.Program

func loadFiberFixture(t *testing.T) *loader.Program {
	t.Helper()
	if fiberFixture != nil {
		return fiberFixture
	}
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fibershop"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := loader.Load(dir)
	if err != nil {
		t.Fatalf("Load(fibershop): %v", err)
	}
	fiberFixture = p
	return p
}

// TestDiscoverFibershop covers every fiber registration shape: plain verbs,
// Add and All, nested groups, a Route callback, wildcards, and the two
// handler-argument orders (v2 middleware-first, v3 endpoint-first).
func TestDiscoverFibershop(t *testing.T) {
	p := loadFiberFixture(t)
	eps := Discover(p)

	byID := map[string]Entrypoint{}
	for _, ep := range eps {
		byID[ep.ID] = ep
		t.Logf("discovered: %s -> %s", ep.ID, ep.FuncName())
	}

	tests := []struct {
		id       string
		funcPart string
		why      string
	}{
		{"http:GET:/healthz", "fibershop.healthz", "plain func handler on *App"},
		{"http:POST:/orders", "api.Handler).CreateOrder", "v2 takes the LAST handler: middleware is registered first"},
		{"http:PUT:/orders/{id}", "api.Handler).UpdateOrder", "Add(method, path, ...) with a normalized param"},
		{"http:ANY:/ping", "api.Handler).Ping", "All registers every method"},
		{"http:GET:/files/{*}", "api.Handler).DownloadFile", "wildcard normalizes to a brace form too"},
		{"http:GET:/api/v1/orders/{id}", "api.Handler).GetOrder", "nested Group() values compose their prefixes"},
		{"http:POST:/admin/orders/{id}/refund", "api.Handler).RefundOrder", "Route(prefix, callback) prefixes its subtree"},
		{"http:GET:/staff/list", "api.Handler).ListStaff", "Route callback that is a declared function, not a literal"},
		{"http:GET:/reports/daily", "api.Handler).DailyReport", "Route callback that is a method value"},
		{"http:GET:/shelf/items/{id}", "api.Handler).ShelfItem", "router reaches the registration through a struct field"},
		{"http:GET:/chain/items", "api.Handler).ListItems", "prefix survives Use() chained between Group and the verb"},
		{"http:GET:/region", "api.Handler).ListRegion", "callback mounted twice claims neither prefix, deterministically"},
		{"http:GET:/depot/crates/{id}", "api.Handler).DepotItem", "router field reached through a pointer alias"},
		{"http:GET:/assets/:name", "fibershop.serveAsset", "net/http literal: a colon segment is not a parameter there"},

		{"http:GET:/status", "v3api.Handler).Status", "v3 registration on *App"},
		{"http:GET:/v3/orders/{id}", "v3api.Handler).GetOrder", "v3 takes the FIRST handler: middleware follows it"},
		{"http:PATCH:/v3/orders/{id}", "v3api.Handler).PatchOrder", "v3 Add takes a list of methods"},
		{"http:POST:/v3/shop/orders", "v3api.Handler).CreateOrder", "v3 Group() prefix through the Router interface"},
	}
	for _, tt := range tests {
		ep, ok := byID[tt.id]
		if !ok {
			t.Errorf("entrypoint %q not discovered (%s)", tt.id, tt.why)
			continue
		}
		if ep.Kind != "http" {
			t.Errorf("%s: kind = %q, want http", tt.id, ep.Kind)
		}
		if !strings.Contains(ep.FuncName(), tt.funcPart) {
			t.Errorf("%s: func = %q, want containing %q (%s)", tt.id, ep.FuncName(), tt.funcPart, tt.why)
		}
	}
	if len(eps) != len(tests) {
		t.Errorf("discovered %d entrypoints, want %d: %v", len(eps), len(tests), ids(eps))
	}

	// Middleware is never an endpoint, however it was registered.
	for _, ep := range eps {
		if strings.Contains(ep.FuncName(), "RequireToken") || strings.Contains(ep.FuncName(), "v3api.Audit") {
			t.Errorf("%s: middleware %s discovered as an entrypoint", ep.ID, ep.FuncName())
		}
	}
}

// TestDiscoverDeterministic: ids must not depend on map iteration order.
// The prefix chase reads program-wide indexes built by iterating over all
// functions, which is exactly where an order dependency would hide.
func TestDiscoverDeterministic(t *testing.T) {
	p := loadFiberFixture(t)
	first := strings.Join(ids(Discover(p)), "\n")
	for i := 0; i < 10; i++ {
		// Drop the memoized indexes so each round rebuilds them from a fresh
		// iteration over the program.
		fiberRouteCache.Delete(p.Prog)
		callerCache.Delete(p.Prog)
		if got := strings.Join(ids(Discover(p)), "\n"); got != first {
			t.Fatalf("discovery is not deterministic; round %d differs:\n%s\n--- first ---\n%s", i, got, first)
		}
	}
}

func TestNormalizeRoutePath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/orders", "/orders"},
		{"/orders/:id", "/orders/{id}"},
		{"/orders/:id/items/:itemID", "/orders/{id}/items/{itemID}"},
		{"/orders/{id}", "/orders/{id}"},               // chi and gorilla already brace it
		{"/orders/{id:[0-9]+}", "/orders/{id:[0-9]+}"}, // chi regex form is left alone
		{"/files/*", "/files/{*}"},
		{"/files/*path", "/files/{*path}"}, // gin names its catch-all
		{"/files/+path", "/files/{+path}"}, // fiber's one-or-more wildcard
		{"/orders/:id?", "/orders/{id?}"},  // fiber optional parameter
		{"/a:b/c", "/a:b/c"},               // a colon inside a literal segment
		{"", ""},
	}
	for _, tt := range tests {
		if got := normalizeRoutePath(tt.in); got != tt.want {
			t.Errorf("normalizeRoutePath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestResolveAcceptsRouterSyntax: an id spelled the way the router itself
// writes parameters still resolves, so ids copied from a framework's own
// route table (or from an older softmap listing) keep working.
func TestResolveAcceptsRouterSyntax(t *testing.T) {
	p := loadFiberFixture(t)
	eps := Discover(p)

	ep, err := Resolve(p, eps, "http:GET:/api/v1/orders/:id")
	if err != nil {
		t.Fatalf("Resolve(colon-style id): %v", err)
	}
	if ep.ID != "http:GET:/api/v1/orders/{id}" {
		t.Errorf("resolved ID = %q, want the normalized form", ep.ID)
	}
}
