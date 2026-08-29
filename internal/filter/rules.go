// Package filter is the noise-reduction pipeline - the part that turns a
// raw call graph into a readable flow map. It is config-driven from day one:
// the built-in defaults ship as an embedded YAML file in exactly the format
// a user rule file will use, so the rule format is inspectable (softmap
// rules --defaults) and stable.
package filter

import (
	_ "embed"
	"fmt"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed rules/default.yaml
var defaultYAML []byte

// DefaultYAML returns the embedded default rule file verbatim.
func DefaultYAML() []byte { return defaultYAML }

// DefaultRules parses the embedded defaults.
func DefaultRules() ([]Rule, error) {
	rules, err := ParseRules(defaultYAML)
	if err != nil {
		return nil, fmt.Errorf("embedded default rules are broken (this is a softmap bug): %w", err)
	}
	return rules, nil
}

type ruleFile struct {
	Version int    `yaml:"version"`
	Rules   []Rule `yaml:"rules"`
}

// Rule pairs match criteria with an action. Rules apply in file order; the
// first matching rule decides a node's fate. Effect nodes and unresolved
// dynamic terminals are immune at the engine level - that immunity is what
// makes broad rules like "drop all stdlib" safe to write.
type Rule struct {
	ID     string `yaml:"id"`
	Action string `yaml:"action"` // "drop" | "collapse"
	Match  Match  `yaml:"match"`
}

// Match criteria AND together; the string lists inside each criterion OR.
type Match struct {
	Pkg       StringList `yaml:"pkg"`       // package path globs; pseudo-prefixes std: and dep:
	Func      StringList `yaml:"func"`      // function/method name globs
	Heuristic string     `yaml:"heuristic"` // named built-in predicate
}

// StringList accepts either a YAML scalar or a sequence.
type StringList []string

func (l *StringList) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var s string
		if err := node.Decode(&s); err != nil {
			return err
		}
		*l = StringList{s}
		return nil
	}
	var s []string
	if err := node.Decode(&s); err != nil {
		return err
	}
	*l = s
	return nil
}

// ParseRules parses and validates a rule file.
func ParseRules(data []byte) ([]Rule, error) {
	var file ruleFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parsing rules: %w", err)
	}
	seen := map[string]bool{}
	for i, r := range file.Rules {
		if r.ID == "" {
			return nil, fmt.Errorf("rule %d: missing id", i+1)
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("rule %q: duplicate id", r.ID)
		}
		seen[r.ID] = true
		if r.Action != "drop" && r.Action != "collapse" {
			return nil, fmt.Errorf("rule %q: action must be drop or collapse, got %q", r.ID, r.Action)
		}
		if len(r.Match.Pkg) == 0 && len(r.Match.Func) == 0 && r.Match.Heuristic == "" {
			return nil, fmt.Errorf("rule %q: empty match (need pkg, func, or heuristic)", r.ID)
		}
		if h := r.Match.Heuristic; h != "" {
			if _, ok := predicates[h]; !ok {
				return nil, fmt.Errorf("rule %q: unknown heuristic %q (known: %s)", r.ID, h, strings.Join(predicateNames(), ", "))
			}
		}
	}
	return file.Rules, nil
}

func predicateNames() []string {
	names := make([]string, 0, len(predicates))
	for name := range predicates {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// matchPkg matches a package path against one pattern. Beyond plain globs
// (a trailing * matches any suffix), two pseudo-prefixes classify packages:
// "std:<pattern>" restricts to the standard library, "dep:*" matches
// anything outside the analyzed module that is not stdlib.
func matchPkg(pattern, pkg, module string) bool {
	switch {
	case strings.HasPrefix(pattern, "std:"):
		return isStdlib(pkg) && matchGlob(strings.TrimPrefix(pattern, "std:"), pkg)
	case strings.HasPrefix(pattern, "dep:"):
		return pkg != "" && !isStdlib(pkg) && !inModule(pkg, module) &&
			matchGlob(strings.TrimPrefix(pattern, "dep:"), pkg)
	default:
		return matchGlob(pattern, pkg)
	}
}

func isStdlib(pkg string) bool {
	if pkg == "" {
		return false
	}
	first, _, _ := strings.Cut(pkg, "/")
	return !strings.Contains(first, ".")
}

func inModule(pkg, module string) bool {
	return module != "" && (pkg == module || strings.HasPrefix(pkg, module+"/"))
}

// matchGlob: "*" matches everything, a trailing "*" matches any suffix
// (crossing path separators, unlike path.Match), anything else falls back
// to path.Match semantics, then exact comparison.
func matchGlob(pattern, s string) bool {
	switch {
	case pattern == "*":
		return true
	case strings.HasSuffix(pattern, "*") && !strings.ContainsAny(strings.TrimSuffix(pattern, "*"), "*?["):
		return strings.HasPrefix(s, strings.TrimSuffix(pattern, "*"))
	default:
		if ok, err := path.Match(pattern, s); err == nil && ok {
			return true
		}
		return pattern == s
	}
}
