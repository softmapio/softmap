// Package repolint holds repository-hygiene tests that run as part of the
// normal test suite. They check the repo's files, not its Go code.
package repolint

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// skipDirs are directories the forbidden-terms walk does not enter: VCS
// internals, build output, local scan artifacts, and local tool state.
var skipDirs = map[string]bool{
	".git":    true,
	"bin":     true,
	"out":     true,
	".claude": true,
}

// imageExts are checked by path only. Their compressed byte streams can
// collide with short text patterns by chance, so content matching on them
// would produce spurious failures.
var imageExts = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".webp": true,
	".ico":  true,
}

// TestForbiddenTerms walks the whole repository and fails if any file's
// content or path matches an entry from forbidden.txt. The list file itself
// is excluded (it defines the entries).
func TestForbiddenTerms(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	listPath := filepath.Join(root, "internal", "repolint", "forbidden.txt")
	res := loadForbidden(t, listPath)

	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if info.IsDir() {
			if skipDirs[info.Name()] && filepath.Dir(rel) == "." {
				return filepath.SkipDir
			}
			return nil
		}
		if path == listPath || !info.Mode().IsRegular() {
			return nil
		}
		for _, re := range res {
			if re.MatchString(strings.ToLower(rel)) {
				t.Errorf("%s: file path matches forbidden entry %q", rel, re)
			}
		}
		if imageExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", rel, err)
		}
		content := strings.ToLower(string(data))
		for _, re := range res {
			if loc := re.FindStringIndex(content); loc != nil {
				line := 1 + strings.Count(content[:loc[0]], "\n")
				t.Errorf("%s:%d: content matches forbidden entry %q", rel, line, re)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// loadForbidden reads forbidden.txt: comment/blank lines are skipped, every
// other line is a base64-encoded regular expression matched against
// lowercased input.
func loadForbidden(t *testing.T, path string) []*regexp.Regexp {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open forbidden list: %v", err)
	}
	defer f.Close()

	var res []*regexp.Regexp
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(line)
		if err != nil {
			t.Fatalf("forbidden.txt:%d: not valid base64: %v", n, err)
		}
		re, err := regexp.Compile(string(raw))
		if err != nil {
			t.Fatalf("forbidden.txt:%d: not a valid regexp: %v", n, err)
		}
		res = append(res, re)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Fatal("forbidden.txt: no entries loaded")
	}
	return res
}
