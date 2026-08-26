package filter

import (
	"strings"
	"testing"
)

func TestParseRulesValidation(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"valid scalar pkg", "rules:\n  - id: a\n    action: drop\n    match: { pkg: \"std:*\" }\n", ""},
		{"valid list pkg", "rules:\n  - id: a\n    action: drop\n    match: { pkg: [\"a/*\", \"b\"] }\n", ""},
		{"missing id", "rules:\n  - action: drop\n    match: { pkg: x }\n", "missing id"},
		{"duplicate id", "rules:\n  - id: a\n    action: drop\n    match: { pkg: x }\n  - id: a\n    action: drop\n    match: { pkg: y }\n", "duplicate id"},
		{"bad action", "rules:\n  - id: a\n    action: obliterate\n    match: { pkg: x }\n", "action must be"},
		{"empty match", "rules:\n  - id: a\n    action: drop\n    match: {}\n", "empty match"},
		{"unknown heuristic", "rules:\n  - id: a\n    action: drop\n    match: { heuristic: no-such }\n", "unknown heuristic"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseRules([]byte(tt.yaml))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestDefaultRulesParse(t *testing.T) {
	rules, err := DefaultRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) < 8 {
		t.Errorf("only %d default rules; expected the full pipeline", len(rules))
	}
	for _, r := range rules {
		if !strings.HasPrefix(r.ID, "default:") {
			t.Errorf("default rule id %q should be namespaced default:", r.ID)
		}
	}
}

func TestMatchPkg(t *testing.T) {
	const module = "example.com/app"
	tests := []struct {
		pattern, pkg string
		want         bool
	}{
		{"std:*", "context", true},
		{"std:*", "net/http", true},
		{"std:*", "github.com/x/y", false},
		{"std:context", "context", true},
		{"std:context", "fmt", false},
		{"dep:*", "github.com/x/y", true},
		{"dep:*", "net/http", false},
		{"dep:*", "example.com/app/svc", false},
		{"dep:*", "example.com/app", false},
		{"github.com/prometheus/*", "github.com/prometheus/client_golang/prometheus", true},
		{"github.com/prometheus/*", "github.com/prom/other", false},
		{"example.com/app/pkg/log", "example.com/app/pkg/log", true},
		{"*", "anything/at/all", true},
	}
	for _, tt := range tests {
		if got := matchPkg(tt.pattern, tt.pkg, module); got != tt.want {
			t.Errorf("matchPkg(%q, %q) = %v, want %v", tt.pattern, tt.pkg, got, tt.want)
		}
	}
}
