package entrypoints

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/softmapio/softmap/internal/loader"
)

var gorillaFixture *loader.Program

func loadGorillaFixture(t *testing.T) *loader.Program {
	t.Helper()
	if gorillaFixture != nil {
		return gorillaFixture
	}
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "gorillashop"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := loader.Load(dir)
	if err != nil {
		t.Fatalf("Load(gorillashop): %v", err)
	}
	gorillaFixture = p
	return p
}

// TestDiscoverGorillashop covers the registration shapes from the first
// field report (issue #1): subrouters created with PathPrefix().Subrouter()
// and passed into helper methods, a nested subrouter with middleware, and
// route-builder chains, all of which must compose their full path.
func TestDiscoverGorillashop(t *testing.T) {
	p := loadGorillaFixture(t)
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
		{"http:GET:/api/bot", "Application).bot", "HandleFunc directly on a subrouter keeps the /api prefix"},
		{"http:POST:/api/login", "Application).login", "subrouter passed into a helper method"},
		{"http:GET:/api/me", "Application).me", "second route in the same helper"},
		{"http:GET:/api/admin/stats", "Application).stats", "nested PathPrefix().Subrouter() composes /api/admin"},
		{"http:DELETE:/api/admin/users/{id}", "Application).deleteUser", "builder chain inside a helper: Methods before Path"},
		{"http:POST:/api/telegram/webhook", "Application).telegramWebhook", "subrouter created inside the helper itself"},
		{"http:ANY:/healthz", "Application).healthz", "Path().HandlerFunc() builder chain on the root router"},
		{"http:POST:/orders", "Application).createOrder", "Methods().Path().HandlerFunc() chain on the root router"},
		{"http:ANY:/api/files/", "Application).serveFiles", "PathPrefix().Handler() with an http.HandlerFunc value"},
		{"http:GET:/api/v2/ping", "Application).pingV2", "StrictSlash is a passthrough, prefix survives"},
		{"http:PUT:/export", "Application).export", "Methods reached through a Name link after the handler"},
		{"http:GET:/api/reports/daily", "Application).dailyReport", "subrouter held in a struct field set by a constructor"},
		{"http:GET:/api/metrics/latency", "Application).latency", "subrouter captured by a closure"},
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

	// Middleware never becomes an entrypoint.
	for _, ep := range eps {
		if strings.Contains(ep.FuncName(), "authMiddleware") {
			t.Errorf("%s: middleware discovered as an entrypoint", ep.ID)
		}
	}
}

// TestGorillaDeterministic: the prefix chase reads program-wide caller
// indexes, so ids must not depend on map iteration order.
func TestGorillaDeterministic(t *testing.T) {
	p := loadGorillaFixture(t)
	first := strings.Join(ids(Discover(p)), "\n")
	for i := 0; i < 10; i++ {
		callerCache.Delete(p.Prog)
		if got := strings.Join(ids(Discover(p)), "\n"); got != first {
			t.Fatalf("discovery is not deterministic; round %d differs:\n%s\n--- first ---\n%s", i, got, first)
		}
	}
}
