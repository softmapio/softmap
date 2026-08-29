package render

import (
	"bytes"
	"strings"
	"testing"

	"github.com/softmapio/softmap/internal/output"
)

func doc(id string) *output.Document {
	return &output.Document{
		SchemaVersion: output.SchemaVersion,
		Module:        "example.com/app",
		Entrypoint:    output.EntrypointDoc{ID: id, Kind: "http", Method: "POST", Path: "/x", Func: "h.Create", Pos: "h.go:1"},
		Nodes: []output.NodeDoc{{
			ID: "n1", Func: "svc.Do", Package: "example.com/app/svc", Kind: "step",
			Resolution: "static",
		}},
		Edges: []output.EdgeDoc{{From: "entrypoint", To: "n1"}},
		Stats: output.Stats{RawNodes: 5, KeptNodes: 1, DroppedByRule: map[string]int{}},
	}
}

func TestWriteHTML(t *testing.T) {
	var buf bytes.Buffer
	flows := []Flow{{Label: "http:POST:/x", Doc: doc("http:POST:/x")}}
	if err := WriteHTML(&buf, "softmap", flows, nil); err != nil {
		t.Fatal(err)
	}
	page := buf.String()
	for _, want := range []string{
		"<title>softmap - flow maps</title>",
		`"schema_version"`,
		`"http:POST:/x"`,
		"const FLOWS =",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing %q", want)
		}
	}
	for _, leftover := range []string{"__FLOWS__", "__TOOL__"} {
		if strings.Contains(page, leftover) {
			t.Errorf("unreplaced placeholder %s", leftover)
		}
	}
}

// TestWriteHTMLEscapesScriptBreakout: SQL text (or any detail) containing
// "</script>" must not terminate the inline data script.
func TestWriteHTMLEscapesScriptBreakout(t *testing.T) {
	d := doc("http:POST:/x")
	d.Nodes[0].Func = `evil</script><script>alert(1)</script>`
	var buf bytes.Buffer
	if err := WriteHTML(&buf, "softmap", []Flow{{Label: "x", Doc: d}}, nil); err != nil {
		t.Fatal(err)
	}
	page := buf.String()
	if strings.Contains(page, "evil</script>") {
		t.Error("raw </script> sequence leaked into the page data")
	}
	if !strings.Contains(page, `evil\u003c/script\u003e`) {
		t.Error("expected unicode-escaped angle brackets in embedded data")
	}
}

func TestWriteHTMLEmpty(t *testing.T) {
	if err := WriteHTML(&bytes.Buffer{}, "softmap", nil, nil); err == nil {
		t.Error("expected error for zero flows")
	}
}
