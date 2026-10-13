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

var allowedMutations = []string{"login", "updateStrategies", "updateConfig", "create", "update"}

func sources(t *testing.T) map[string]string {
	out := map[string]string{}
	filepath.WalkDir("../..", func(path string, d os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			b, _ := os.ReadFile(path)
			out[path] = string(b)
		}
		return nil
	})
	if len(out) == 0 {
		t.Fatal("no sources found")
	}
	return out
}

func TestOnlyTheNamedMutations(t *testing.T) {
	field := regexp.MustCompile(`(?s)mutation\([^)]*\)\{\w+\{(\w+)\(`)
	for path, src := range sources(t) {
		for _, m := range field.FindAllStringSubmatch(src, -1) {
			if !slices.Contains(allowedMutations, m[1]) {
				t.Errorf("%s: mutation %s is not one the spec names", path, m[1])
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
