// Package audit holds the test that keeps the bootstrap's writes to the ones
// its spec names: any other GraphQL mutation, or any SQL that writes, fails it.
package audit

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Each is namespace.field, exactly as the GraphQL document names it: a bare
// field name would let users{update(...)} or pages{update(...)} through.
var allowedMutations = []string{
	"authentication.login",
	"authentication.updateStrategies",
	"site.updateConfig",
	"groups.create",
	"groups.update",
}

func sources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir("../..", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != "../.." && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir // .git, and other worktrees under .worktrees
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[path] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("reading the sources: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("no sources found")
	}
	return out
}

func TestOnlyTheNamedMutations(t *testing.T) {
	field := regexp.MustCompile(`(?s)mutation\([^)]*\)\{(\w+)\{(\w+)\(`)
	for path, src := range sources(t) {
		for _, m := range field.FindAllStringSubmatch(src, -1) {
			if name := m[1] + "." + m[2]; !slices.Contains(allowedMutations, name) {
				t.Errorf("%s: mutation %s is not one the spec names", path, name)
			}
		}
		if strings.Contains(src, "mutation{") || strings.Contains(src, "mutation {") {
			t.Errorf("%s: a mutation without variables escapes this check", path)
		}
	}
}

func TestNoSQLWrites(t *testing.T) {
	// A string literal that is a writing SQL statement, or a SELECT that
	// calls one in a subquery.
	starts := regexp.MustCompile(`(?i)^\s*(INSERT|UPDATE|DELETE|TRUNCATE|DROP|ALTER|CREATE|MERGE|COPY)\s`)
	inside := regexp.MustCompile(`(?i)\b(INSERT\s+INTO|DELETE\s+FROM|TRUNCATE|DROP\s|ALTER\s)`)
	literal := regexp.MustCompile("`[^`]*`|\"(?:[^\"\\\\]|\\\\.)*\"")
	for path, src := range sources(t) {
		for _, lit := range literal.FindAllString(src, -1) {
			body := strings.Trim(lit, "`\"")
			if starts.MatchString(body) || inside.MatchString(body) {
				t.Errorf("%s: SQL that writes: %s", path, lit)
			}
		}
	}
}
