package effects

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/softmapio/softmap/internal/loader"
	"github.com/softmapio/softmap/internal/ssax"
)

func TestMatchers(t *testing.T) {
	tests := []struct {
		pkg, typ, name string
		wantType       string // "" = no effect
	}{
		{"database/sql", "DB", "QueryContext", "sql"},
		{"database/sql", "Tx", "ExecContext", "sql"},
		{"database/sql", "DB", "Close", ""},
		{"github.com/jackc/pgx/v5/pgxpool", "Pool", "Exec", "sql"},
		{"github.com/jackc/pgx/v5", "Conn", "QueryRow", "sql"},
		{"github.com/jmoiron/sqlx", "DB", "GetContext", "sql"},
		{"gorm.io/gorm", "DB", "Find", "sql"},
		{"gorm.io/gorm", "DB", "WithContext", ""},
		{"github.com/redis/go-redis/v9", "Client", "Get", "redis"},
		{"github.com/redis/go-redis/v9", "Client", "HSet", "redis"},
		{"github.com/redis/go-redis/v9", "Client", "Close", ""},
		{"github.com/go-redis/redis/v8", "ClusterClient", "Set", "redis"},
		{"net/http", "Client", "Do", "http"},
		{"net/http", "", "Post", "http"},
		{"net/http", "", "NewRequest", "http"}, // request construction carries the URL/template
		{"net/http", "ServeMux", "Handle", ""},
		{"github.com/segmentio/kafka-go", "Writer", "WriteMessages", "kafka"},
		{"github.com/segmentio/kafka-go", "Reader", "ReadMessage", ""},
		{"github.com/IBM/sarama", "SyncProducer", "SendMessage", "kafka"},
		{"github.com/Shopify/sarama", "AsyncProducer", "Input", "kafka"},
		{"github.com/confluentinc/confluent-kafka-go/v2/kafka", "Producer", "Produce", "kafka"},
		{"google.golang.org/grpc", "ClientConn", "Invoke", "grpc"},
		{"google.golang.org/grpc", "ClientConn", "Close", ""},
		{"example.com/myapp/db", "DB", "Query", ""}, // lookalike, wrong package
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s.%s.%s", tt.pkg, tt.typ, tt.name), func(t *testing.T) {
			info := &ssax.CalleeInfo{Pkg: tt.pkg, Type: tt.typ, Name: tt.name}
			got := ""
			for _, d := range detectors {
				if d.matches(info) {
					got = d.name
					break
				}
			}
			switch {
			case tt.wantType == "" && got != "":
				t.Errorf("matched %s, want no match", got)
			case tt.wantType != "" && got == "":
				t.Errorf("no detector matched, want a %s detector", tt.wantType)
			}
		})
	}
}

// TestDetectOnFixture runs detection over every call site in the toyshop
// fixture and checks the concrete effects, including topic and query
// resolution through real SSA values.
func TestDetectOnFixture(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "toyshop"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := loader.Load(dir)
	if err != nil {
		t.Fatalf("Load(toyshop): %v", err)
	}

	type found struct {
		effType, detail, topic, inFunc string
	}
	var all []found
	for fn := range ssautil.AllFunctions(p.Prog) {
		if !p.InModule(fn) {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				site, ok := instr.(ssa.CallInstruction)
				if !ok {
					continue
				}
				if e := Detect(ssax.Callee(site), site); e != nil {
					all = append(all, found{e.Type, e.Detail, e.Topic, fn.Name()})
				}
			}
		}
	}

	want := []struct {
		effType, detailPart, topic, inFunc string
	}{
		{"sql", "INSERT INTO orders", "", "save"},
		{"sql", "INSERT INTO order_items", "", "save"},
		{"sql", "SELECT id, name, price FROM products", "", "ListProducts"},
		{"sql", "INSERT INTO products", "", "CreateProduct"},
		{"sql", "SELECT id, item, qty FROM orders", "", "FindOrder"},
		{"sql", "INSERT INTO audit", "", "AuditLog"},
		{"sql", "REFRESH MATERIALIZED VIEW", "", "Sync"},
		{"grpc", "/toyshop.Orders/GetOrder", "", "GetOrder"},
		{"redis", "Set", "", "CacheOrder"},
		{"redis", "Get", "", "CachedOrder"},
		{"kafka", "WriteMessages", "orders.created", "OrderCreated"},
		{"http", "Post http://mailer.internal/send", "", "Notify"},
		{"http", "POST http://slack.internal/hook", "", "Notify"},
		{"http", "Do", "", "Notify"},
		{"security", "verifies password", "", "Login"},
		{"security", "issues token", "", "Login"},
		{"storage", "PresignedGetObject", "", "Login"},
	}
	for _, w := range want {
		ok := false
		for _, f := range all {
			if f.effType == w.effType && strings.Contains(f.detail, w.detailPart) &&
				f.topic == w.topic && f.inFunc == w.inFunc {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("missing effect %+v in detected set:\n%+v", w, all)
		}
	}
	if len(all) != len(want) {
		t.Errorf("detected %d effects, want %d: %+v", len(all), len(want), all)
	}
}
