// Package render emits a self-contained interactive HTML viewer for one or
// more flow documents. The page is a single file with no external
// dependencies — inline CSS/JS, data embedded — so it can live in CI
// artifacts, be committed, or be opened from disk. The JSON schema remains
// the machine contract; this is the human view of the same data.
package render

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/softmapio/softmap/internal/entities"
	"github.com/softmapio/softmap/internal/output"
)

//go:embed viewer.html
var viewerHTML string

// Flow pairs a document with the label shown in the viewer's switcher.
type Flow struct {
	Label string           `json:"label"`
	Doc   *output.Document `json:"doc"`
}

// WriteHTML renders the viewer page for the given flows. toolName only
// brands the page chrome; it is injected so the name keeps living in one
// place (cmd's toolName constant). index is the entity shelf (nil when the
// scan covered a single flow — the page then opens straight on it).
func WriteHTML(w io.Writer, toolName string, flows []Flow, index *entities.Index) error {
	if len(flows) == 0 {
		return fmt.Errorf("no flows to render")
	}
	embed := func(v any) (string, error) {
		var blob bytes.Buffer
		enc := json.NewEncoder(&blob)
		enc.SetEscapeHTML(true) // "<" becomes <, so data can never close the script tag
		if err := enc.Encode(v); err != nil {
			return "", err
		}
		return strings.TrimSpace(blob.String()), nil
	}
	flowsJSON, err := embed(flows)
	if err != nil {
		return fmt.Errorf("encoding flows: %w", err)
	}
	indexJSON := "null"
	if index != nil {
		if indexJSON, err = embed(index); err != nil {
			return fmt.Errorf("encoding index: %w", err)
		}
	}
	page := strings.ReplaceAll(viewerHTML, "__TOOL__", toolName)
	page = strings.Replace(page, "__FLOWS__", flowsJSON, 1)
	page = strings.Replace(page, "__INDEX__", indexJSON, 1)
	_, err = io.WriteString(w, page)
	return err
}
