package entities

import "testing"

func TestEntityKey(t *testing.T) {
	tests := map[string]string{
		"products":     "product",
		"orders":       "order",
		"order_items":  "order_item",
		"companies":    "company",
		"statuses":     "status",
		"boxes":        "box",
		"address":      "address",
		"audit":        "audit",
		"ORDER_ITEMS":  "order_item",
		"order_status": "order_status",
	}
	for in, want := range tests {
		if got := entityKey(in); got != want {
			t.Errorf("entityKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSQLAccess(t *testing.T) {
	tests := []struct {
		detail, verb, table string
	}{
		{"SELECT id, item FROM orders WHERE id = $1", "reads", "orders"},
		{"select * from public.users", "reads", "users"},
		{"INSERT INTO order_items (a) VALUES ($1)", "creates", "order_items"},
		{"UPDATE products SET price = $1", "updates", "products"},
		{"DELETE FROM sessions WHERE id = $1", "deletes", "sessions"},
		{`SELECT x FROM "quoted"`, "reads", "quoted"},
		{"WITH t AS (SELECT 1) SELECT * FROM orders", "reads", "orders"},
		// ORM method names carry no table — no entity signal.
		{"Find", "", ""},
		{"REFRESH MATERIALIZED VIEW order_stats", "", ""},
	}
	for _, tt := range tests {
		verb, table := sqlAccess(tt.detail)
		if verb != tt.verb || table != tt.table {
			t.Errorf("sqlAccess(%q) = (%q, %q), want (%q, %q)", tt.detail, verb, table, tt.verb, tt.table)
		}
	}
}

func TestPathEntity(t *testing.T) {
	tests := map[string]string{
		"/products/{id}":         "products",
		"/orders/:id/approve":    "orders",
		"/v1/reports":            "reports",
		"/auth/login":            "",
		"/healthz":               "",
		"/api/v2/users/{id}":     "users",
		"/{tenant}/orders":       "",
		"":                       "",
		"/metrics":               "",
	}
	for in, want := range tests {
		if got := pathEntity(in); got != want {
			t.Errorf("pathEntity(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTopicPrefix(t *testing.T) {
	tests := map[string]string{
		"order.created":  "order",
		"orders.created": "orders",
		"order_paid":     "order",
		"payments-done":  "payments",
		"events":         "", // unsegmented — no evidence
	}
	for in, want := range tests {
		if got := topicPrefix(in); got != want {
			t.Errorf("topicPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFlowTitle(t *testing.T) {
	tests := map[string]string{
		"(*chiapi.Handler).CreateProduct": "Create product",
		"(*handlers.Handler).GetOrder":    "Get order",
		"consumer.Run":                    "Run consumer",
		"example.com/toyshop.healthz":     "Healthz",
	}
	for in, want := range tests {
		if got := flowTitle(in, "toyshop"); got != want {
			t.Errorf("flowTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMergeSatellites: automatic prefix clustering needs the base entity to
// exist; a lone satellite keeps its own shelf.
func TestMergeSatellites(t *testing.T) {
	ents := map[string]*entAcc{
		"order": {tables: map[string]bool{"orders": true},
			flows: map[string]map[string]bool{"f1": {"creates": true}}},
		"order_item": {tables: map[string]bool{"order_items": true},
			flows: map[string]map[string]bool{"f1": {"creates": true}, "f2": {"reads": true}}},
		"user_profile": {tables: map[string]bool{"user_profiles": true},
			flows: map[string]map[string]bool{"f3": {"reads": true}}},
	}
	mergeSatellites(ents)
	if _, ok := ents["order_item"]; ok {
		t.Error("order_item was not folded into order")
	}
	if !ents["order"].tables["order_items"] {
		t.Error("order did not inherit the order_items table")
	}
	if !ents["order"].flows["f2"]["reads"] {
		t.Error("order did not inherit satellite flow access")
	}
	if _, ok := ents["user_profile"]; !ok {
		t.Error("user_profile merged away despite no user base entity")
	}
}
