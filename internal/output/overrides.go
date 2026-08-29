package output

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Overrides are hand-written labels from the analyzed repo's .softmap.yaml:
//
//	overrides:
//	  "(*service.Service).FindByPhone": "Поиск заказа по телефону"
//	  "repo/repo.go:31": "загрузка заказа из БД"
//
// A key matches a node when it is a suffix of the node's `func` (as shown
// in Tech mode / the panel), or an effect when it is a suffix of the
// effect's `pos`. Matched labels replace the derived Business-mode title
// and are marked in the viewer so manual text is distinguishable.
type Overrides map[string]string

// Config is the whole .softmap.yaml: label overrides plus the entity-shelf
// knobs - display names for entity keys and manual satellite clustering:
//
//	entities:
//	  order: "Заказ"
//	merge:
//	  order: [order_items, order_status]
type Config struct {
	Overrides Overrides           `yaml:"overrides"`
	Entities  map[string]string   `yaml:"entities"`
	Merge     map[string][]string `yaml:"merge"`
}

// LoadConfig reads .softmap.yaml from the scanned module's root. A missing
// file is not an error (empty config); a malformed one is.
func LoadConfig(dir string) (*Config, error) {
	cfg := &Config{}
	raw, err := os.ReadFile(filepath.Join(dir, ".softmap.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadOverrides keeps the original narrow accessor: just the label map.
func LoadOverrides(dir string) (Overrides, error) {
	cfg, err := LoadConfig(dir)
	if err != nil {
		return nil, err
	}
	return cfg.Overrides, nil
}

// Apply stamps matching labels onto the document.
func (ov Overrides) Apply(doc *Document) {
	if len(ov) == 0 || doc == nil {
		return
	}
	label := func(fn string) string {
		for key, l := range ov {
			if strings.HasSuffix(fn, key) {
				return l
			}
		}
		return ""
	}
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if l := label(n.Func); l != "" {
			n.Label = l
		}
		for j := range n.Effects {
			if l := label(n.Effects[j].Pos); l != "" {
				n.Effects[j].Label = l
			}
		}
	}
	for j := range doc.Entrypoint.Effects {
		if l := label(doc.Entrypoint.Effects[j].Pos); l != "" {
			doc.Entrypoint.Effects[j].Label = l
		}
	}
}
