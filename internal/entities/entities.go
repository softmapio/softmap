// Package entities derives business entities — the nouns an owner thinks in
// ("product", "order") — from what flows already touch. Signals, strongest
// first: SQL table names from effects, then URL path segments and Kafka
// topic prefixes. Fully deterministic; flows with no signal at all land on
// an honest "other" shelf rather than under a guessed entity.
package entities

import (
	"encoding/json"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/softmapio/softmap/internal/output"
)

// FlowDoc pairs an emitted flow document with its on-disk file name.
type FlowDoc struct {
	File string
	Doc  *output.Document
}

// Index is the --all companion document (index.json): the flow list plus
// the entity shelf. Same schema version as the per-flow files — additive.
type Index struct {
	SchemaVersion string     `json:"schema_version"`
	Module        string     `json:"module"`
	Flows         []FlowInfo `json:"flows"`
	Entities      []Entity   `json:"entities"`
	// Other: flows with no entity signal. Never guessed into a shelf.
	Other []FlowRef `json:"other,omitempty"`
}

// FlowInfo is one row of the flow list.
type FlowInfo struct {
	ID     string `json:"id"`
	File   string `json:"file"`
	Title  string `json:"title"`
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	Topic  string `json:"topic,omitempty"`
}

// Entity is one shelf: a noun with the tables that store it and the flows
// that touch it.
type Entity struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// NameOverridden: the name came from .softmap.yaml (viewer shows •).
	NameOverridden bool      `json:"name_overridden,omitempty"`
	Tables         []string  `json:"tables,omitempty"`
	Flows          []FlowRef `json:"flows"`
}

// FlowRef is a flow as seen from an entity: what this flow does to it.
type FlowRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Access kinds, from SQL verbs and messaging: "creates" | "reads" |
	// "updates" | "deletes" | "publishes <topic>" | "consumes <topic>".
	// Empty when membership came from a path/topic signal only.
	Access []string `json:"access,omitempty"`
}

// Build derives the entity index. names maps entity key → display name
// (.softmap.yaml `entities:`); merge maps entity key → satellite tables or
// keys clustered under it (.softmap.yaml `merge:`).
func Build(module string, flows []FlowDoc, names map[string]string, merge map[string][]string) *Index {
	// Manual clustering: satellite (raw table name or its derived key) →
	// target entity key. Checked before the automatic prefix merge.
	manual := map[string]string{}
	for target, sats := range merge {
		for _, s := range sats {
			manual[s] = target
			manual[entityKey(s)] = target
		}
	}
	resolve := func(raw string) string {
		if t, ok := manual[raw]; ok {
			return t
		}
		key := entityKey(raw)
		if t, ok := manual[key]; ok {
			return t
		}
		return key
	}

	ents := map[string]*entAcc{}
	touch := func(key, flowID string) *entAcc {
		e := ents[key]
		if e == nil {
			e = &entAcc{tables: map[string]bool{}, flows: map[string]map[string]bool{}}
			ents[key] = e
		}
		if e.flows[flowID] == nil {
			e.flows[flowID] = map[string]bool{}
		}
		return e
	}

	idx := &Index{SchemaVersion: output.SchemaVersion, Module: module}
	moduleRoot := module[strings.LastIndex(module, "/")+1:]
	titles := map[string]string{}
	for _, f := range flows {
		ep := f.Doc.Entrypoint
		title := flowTitle(ep.Func, moduleRoot)
		titles[ep.ID] = title
		idx.Flows = append(idx.Flows, FlowInfo{
			ID: ep.ID, File: f.File, Title: title,
			Method: ep.Method, Path: ep.Path, Topic: ep.Topic,
		})

		signalled := false
		record := func(effs []output.EffectDoc) {
			for _, e := range effs {
				switch e.Type {
				case "sql":
					verb, table := sqlAccess(e.Detail)
					if table == "" {
						continue
					}
					ent := touch(resolve(table), ep.ID)
					ent.tables[table] = true
					if verb != "" {
						ent.flows[ep.ID][verb] = true
					}
					signalled = true
				case "kafka":
					if e.Topic == nil || *e.Topic == "" {
						continue
					}
					if p := topicPrefix(*e.Topic); p != "" {
						touch(resolve(p), ep.ID).flows[ep.ID]["publishes "+*e.Topic] = true
						signalled = true
					}
				}
			}
		}
		record(ep.Effects)
		for _, n := range f.Doc.Nodes {
			record(n.Effects)
		}
		if seg := pathEntity(ep.Path); seg != "" {
			touch(resolve(seg), ep.ID)
			signalled = true
		}
		if ep.Kind == "kafka" && ep.Topic != "" {
			if p := topicPrefix(ep.Topic); p != "" {
				touch(resolve(p), ep.ID).flows[ep.ID]["consumes "+ep.Topic] = true
				signalled = true
			}
		}
		if !signalled {
			idx.Other = append(idx.Other, FlowRef{ID: ep.ID, Title: title})
		}
	}

	mergeSatellites(ents)

	keys := make([]string, 0, len(ents))
	for k := range ents {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e := ents[k]
		ent := Entity{Key: k, Name: humanizeKey(k)}
		if n, ok := names[k]; ok && n != "" {
			ent.Name = n
			ent.NameOverridden = true
		}
		for t := range e.tables {
			ent.Tables = append(ent.Tables, t)
		}
		sort.Strings(ent.Tables)
		flowIDs := make([]string, 0, len(e.flows))
		for id := range e.flows {
			flowIDs = append(flowIDs, id)
		}
		sort.Strings(flowIDs)
		for _, id := range flowIDs {
			ent.Flows = append(ent.Flows, FlowRef{ID: id, Title: titles[id], Access: sortAccess(e.flows[id])})
		}
		idx.Entities = append(idx.Entities, ent)
	}
	sort.Slice(idx.Flows, func(i, j int) bool { return idx.Flows[i].ID < idx.Flows[j].ID })
	sort.Slice(idx.Other, func(i, j int) bool { return idx.Other[i].ID < idx.Other[j].ID })
	return idx
}

// entAcc accumulates one entity's evidence while flows are scanned.
type entAcc struct {
	tables map[string]bool
	flows  map[string]map[string]bool // flow id → access set
}

// mergeSatellites folds prefix satellites into their base entity:
// order_item (from order_items) merges into order when order exists. The
// base must already be an entity — a lone order_items table does not invent
// an "order" shelf. The shortest existing base wins.
func mergeSatellites(ents map[string]*entAcc) {
	keys := make([]string, 0, len(ents))
	for k := range ents {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !strings.Contains(k, "_") {
			continue
		}
		parts := strings.Split(k, "_")
		for cut := 1; cut < len(parts); cut++ {
			base := strings.Join(parts[:cut], "_")
			target, ok := ents[base]
			if !ok || target == ents[k] {
				continue
			}
			sat := ents[k]
			for t := range sat.tables {
				target.tables[t] = true
			}
			for id, acc := range sat.flows {
				if target.flows[id] == nil {
					target.flows[id] = map[string]bool{}
				}
				for a := range acc {
					target.flows[id][a] = true
				}
			}
			delete(ents, k)
			break
		}
	}
}

// WriteJSON emits the index with the same stable formatting as flow docs.
func (idx *Index) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(idx)
}

// accessRank fixes the display/serialization order of access kinds.
func accessRank(a string) int {
	switch {
	case a == "creates":
		return 0
	case a == "reads":
		return 1
	case a == "updates":
		return 2
	case a == "deletes":
		return 3
	case strings.HasPrefix(a, "publishes"):
		return 4
	default: // consumes
		return 5
	}
}

func sortAccess(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := accessRank(out[i]), accessRank(out[j])
		if ri != rj {
			return ri < rj
		}
		return out[i] < out[j]
	})
	return out
}

// --- signal parsing ---

var (
	fromRe   = regexp.MustCompile(`(?i)\bFROM\s+([\w."'` + "`" + `]+)`)
	intoRe   = regexp.MustCompile(`(?i)\bINTO\s+([\w."'` + "`" + `]+)`)
	updateRe = regexp.MustCompile(`(?i)^UPDATE\s+([\w."'` + "`" + `]+)`)
)

// sqlAccess parses the access kind and target table from a statement. ORM
// calls carry only a method name — no table, so no entity signal; the flow
// can still shelf via its URL path.
func sqlAccess(detail string) (verb, table string) {
	s := strings.TrimSpace(detail)
	up := strings.ToUpper(s)
	pick := func(re *regexp.Regexp) string {
		m := re.FindStringSubmatch(s)
		if m == nil {
			return ""
		}
		t := strings.Trim(m[1], "\"'`")
		if i := strings.LastIndex(t, "."); i >= 0 {
			t = t[i+1:]
		}
		if !identRe.MatchString(t) {
			return ""
		}
		return strings.ToLower(t)
	}
	switch {
	case strings.HasPrefix(up, "SELECT"), strings.HasPrefix(up, "WITH"):
		return "reads", pick(fromRe)
	case strings.HasPrefix(up, "INSERT"):
		return "creates", pick(intoRe)
	case strings.HasPrefix(up, "UPDATE"):
		return "updates", pick(updateRe)
	case strings.HasPrefix(up, "DELETE"):
		return "deletes", pick(fromRe)
	}
	return "", ""
}

var identRe = regexp.MustCompile(`^\w+$`)

// pathEntity: the first noun-like path segment — /products/{id} → products,
// /api/v2/users/{id} → users. Mount prefixes (api, versions) are skipped;
// infrastructure routes (auth, health, metrics…) give no signal; a leading
// parameter gives no signal. Deeper segments are never mined — that would
// be guessing.
var pathTransparent = map[string]bool{
	"api": true, "internal": true, "public": true, "external": true,
}
var pathStop = map[string]bool{
	"auth": true, "login": true, "logout": true, "health": true,
	"healthz": true, "livez": true, "readyz": true, "metrics": true,
	"ping": true, "status": true, "swagger": true, "docs": true, "debug": true,
}
var versionRe = regexp.MustCompile(`^v\d+$`)

func pathEntity(path string) string {
	for _, seg := range strings.Split(path, "/") {
		if seg == "" {
			continue
		}
		low := strings.ToLower(seg)
		if versionRe.MatchString(low) || pathTransparent[low] {
			continue
		}
		if pathStop[low] {
			return "" // infrastructure route, not an entity path
		}
		if strings.HasPrefix(seg, ":") || strings.HasPrefix(seg, "{") || strings.HasPrefix(seg, "*") {
			return ""
		}
		if !identRe.MatchString(seg) {
			return ""
		}
		return low
	}
	return ""
}

// topicPrefix: order.created → order. Split on the customary separators and
// keep the leading noun.
func topicPrefix(topic string) string {
	p := strings.FieldsFunc(topic, func(r rune) bool { return r == '.' || r == '_' || r == '-' })
	if len(p) < 2 {
		return "" // an unsegmented topic name is not evidence of an entity
	}
	if !identRe.MatchString(p[0]) {
		return ""
	}
	return strings.ToLower(p[0])
}

// --- normalization ---

// entityKey normalizes a table/segment to a singular snake_case key:
// products → product, order_items → order_item, companies → company.
func entityKey(raw string) string {
	parts := strings.Split(strings.ToLower(raw), "_")
	parts[len(parts)-1] = singular(parts[len(parts)-1])
	return strings.Join(parts, "_")
}

func singular(w string) string {
	switch {
	case len(w) > 3 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 3 && (strings.HasSuffix(w, "ses") || strings.HasSuffix(w, "xes") ||
		strings.HasSuffix(w, "zes") || strings.HasSuffix(w, "ches") || strings.HasSuffix(w, "shes")):
		return w[:len(w)-2]
	case len(w) > 1 && strings.HasSuffix(w, "s") &&
		!strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is"):
		return w[:len(w)-1]
	}
	return w
}

func humanizeKey(key string) string {
	words := strings.Split(key, "_")
	out := strings.Join(words, " ")
	if out == "" {
		return key
	}
	return strings.ToUpper(out[:1]) + out[1:]
}

// --- flow titles ---

var titleAcronyms = map[string]bool{
	"id": true, "url": true, "api": true, "sql": true, "db": true,
	"http": true, "json": true, "jwt": true, "otp": true, "grpc": true,
}
var layerRe = regexp.MustCompile(`^(use ?cases?|uc|handlers?|services?|svc|repositor(y|ies)|repo|impl|delivery|controllers?|servers?|v\d+|http|grpc|api)$`)

// flowTitle humanizes the handler name: "(*chiapi.Handler).CreateProduct" →
// "Create product". A bare verb borrows the receiver or package qualifier
// ("Run consumer") the same way the viewer does — except the module root
// package, which qualifies nothing.
func flowTitle(fn, moduleRoot string) string {
	name := fn
	var qual string
	if m := regexp.MustCompile(`^\(\*?(.+)\)\.(\w+)$`).FindStringSubmatch(fn); m != nil {
		qual = m[1][strings.LastIndex(m[1], "/")+1:]
		if i := strings.LastIndex(qual, "."); i >= 0 {
			qual = qual[i+1:]
		}
		name = m[2]
	} else {
		name = fn[strings.LastIndex(fn, "/")+1:]
		if i := strings.LastIndex(name, "."); i >= 0 {
			qual = name[:i]
			name = name[i+1:]
		}
	}
	words := splitIdent(name)
	if len(words) == 1 && qual != "" {
		q := strings.ToLower(strings.Join(splitIdent(qual), " "))
		if q != "" && q != strings.ToLower(words[0]) && q != strings.ToLower(moduleRoot) && !layerRe.MatchString(q) {
			words = append(words, q)
		}
	}
	for i, w := range words {
		lw := strings.ToLower(w)
		if titleAcronyms[lw] {
			words[i] = strings.ToUpper(lw)
		} else {
			words[i] = lw
		}
	}
	title := strings.Join(words, " ")
	if title == "" {
		return fn
	}
	return strings.ToUpper(title[:1]) + title[1:]
}

var identBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])|([A-Z]+)([A-Z][a-z])`)

func splitIdent(word string) []string {
	s := strings.ReplaceAll(word, "_", " ")
	s = identBoundary.ReplaceAllString(s, "$1$3 $2$4")
	return strings.Fields(s)
}
