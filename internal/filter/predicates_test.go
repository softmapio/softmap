package filter

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/softmapio/softmap/internal/graph"
	"github.com/softmapio/softmap/internal/loader"
	"github.com/softmapio/softmap/internal/ssax"
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

// nodeFor builds a graph.Node for the module function whose qualified name
// has the given suffix.
func nodeFor(t *testing.T, p *loader.Program, suffix string) *graph.Node {
	t.Helper()
	for fn := range ssautil.AllFunctions(p.Prog) {
		if !p.InModule(fn) || fn.Blocks == nil {
			continue
		}
		name := ssax.FuncDisplayName(fn)
		if strings.HasSuffix(name, suffix) {
			return &graph.Node{Name: name, Fn: fn, Pkg: loader.FuncPackage(fn)}
		}
	}
	t.Fatalf("no module function with suffix %q", suffix)
	return nil
}

func TestPredicates(t *testing.T) {
	p := loadFixture(t)
	tests := []struct {
		predicate string
		fnSuffix  string
		want      bool
	}{
		{"logger-method", "log.Logger).Info", true},
		{"logger-method", "log.Logger).Error", true},
		{"logger-method", "Service).CreateOrder", false},
		// Notify is not a log level; Error-named methods outside loggerish
		// receivers/packages must survive.
		{"logger-method", "EmailNotifier).Notify", false},

		{"logger-package", "log.New", true},
		{"logger-factory", "Service).tracedLogger", true},
		{"logger-factory", "Service).GetOrder", false},
		{"logger-package", "service.New", false},

		{"metrics-tracing", "metrics.Counter).Inc", true},
		{"metrics-tracing", "opTimings).flush", true},
		{"metrics-tracing", "Repo).CacheOrder", false},

		{"config-reader", "config.Load", true},
		{"config-reader", "config.Config).Database", true},
		{"config-reader", "service.New", false},

		{"validation-helper", "handlers.validateOrder", true},
		{"validation-helper", "Service).GetOrder", false},

		{"trivial-wrapper", "Repo).Save", true},
		{"trivial-wrapper", "Repo).CacheOrder", false}, // two calls: Set + Err
		{"trivial-wrapper", "Service).CreateOrder", false},

		{"inline-closure", "CreateOrder$1", true},        // go func(){ AuditLog + log } - one real call
		{"inline-closure", "Service).CreateOrder", false}, // named method, not a closure

		{"getter", "config.Config).Database", false}, // name is not getter-shaped
		{"getter", "model.Order).GetID", true},

		{"trivial-constructor", "echoapi.newErrResp", true},
		{"trivial-constructor", "repo.New", true},      // pure struct wiring collapses too
		{"trivial-constructor", "handlers.New", false}, // real construction: makes calls
		{"error-wrapper", "service.wrapErr", true},
		{"error-wrapper", "Repo).save", false}, // calls the SQL driver
	}
	for _, tt := range tests {
		t.Run(tt.predicate+"/"+tt.fnSuffix, func(t *testing.T) {
			pred, ok := predicates[tt.predicate]
			if !ok {
				t.Fatalf("unknown predicate %q", tt.predicate)
			}
			n := nodeFor(t, p, tt.fnSuffix)
			if got := pred(n, p); got != tt.want {
				t.Errorf("%s(%s) = %v, want %v", tt.predicate, n.Name, got, tt.want)
			}
		})
	}
}

// TestSentinelErrors pins the error-exit extraction on real fixture bodies.
func TestSentinelErrors(t *testing.T) {
	p := loadFixture(t)
	tests := []struct {
		fnSuffix string
		want     []string
	}{
		{"Service).CreateOrder", []string{"ErrTooManyItems"}},
		{"handlers.validateOrder", []string{"\"item is required\"", "\"qty must be positive\""}},
		{"service.wrapErr", nil}, // %w wrapping is plumbing, not an outcome
	}
	for _, tt := range tests {
		n := nodeFor(t, p, tt.fnSuffix)
		got := ssax.SentinelErrors(n.Fn)
		if len(got) != len(tt.want) {
			t.Errorf("SentinelErrors(%s) = %v, want %v", tt.fnSuffix, got, tt.want)
			continue
		}
		for _, w := range tt.want {
			found := false
			for _, g := range got {
				if g == w {
					found = true
				}
			}
			if !found {
				t.Errorf("SentinelErrors(%s) = %v, missing %v", tt.fnSuffix, got, w)
			}
		}
	}
}
