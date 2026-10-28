// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestProviderAndApplicationBoundaries(t *testing.T) {
	root := filepath.Join("..", "..")
	sql := regexp.MustCompile(`(?i)^\s*(select|insert\s+into|update|delete\s+from|create\s+table|pragma)\s`)
	for _, tree := range []string{"financial", "source", "provider", "cmd", "internal", "testdata/external-provider", "testdata/socket-consumer", "providers"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
			if err != nil {
				return err
			}

			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}

			rel = filepath.ToSlash(rel)
			for _, spec := range file.Imports {
				value, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return err
				}

				for _, retired := range []string{"/internal/storage", "/internal/domain", "/internal/providers", "/internal/reconciliation", "/internal/application/acquisition"} {
					if strings.Contains(value, retired) {
						t.Errorf("%s imports retired pipeline %s", rel, value)
					}
				}

				if tree == "financial" || tree == "source" || tree == "provider" ||
					(tree == "testdata/external-provider" || tree == "providers") {
					if strings.Contains(value, "/internal/") || value == "database/sql" ||
						strings.Contains(value, "sqlite") ||
						strings.Contains(value, "templ") {
						t.Errorf("%s leaks private, SQL or UI dependency %s", rel, value)
					}

					if strings.HasPrefix(value, "go.trippwill.dev/wake/") &&
						value != "go.trippwill.dev/wake/financial" &&
						value != "go.trippwill.dev/wake/source" &&
						value != "go.trippwill.dev/wake/provider" {
						t.Errorf("%s imports non-contract Wake package %s", rel, value)
					}
				}

				if tree == "testdata/socket-consumer" &&
					(strings.Contains(value, "go.trippwill.dev/wake") || value == "database/sql" || strings.Contains(value, "sqlite")) {
					t.Errorf("independent consumer depends on private data or Wake packages: %s", value)
				}

				if strings.HasPrefix(rel, "internal/transport/") &&
					(value == "database/sql" || strings.Contains(value, "/internal/persistence/")) {
					t.Errorf("presentation bypasses application boundary: %s", rel)
				}

				if strings.HasPrefix(rel, "internal/host/") &&
					(value == "database/sql" || strings.Contains(value, "/internal/persistence/") || strings.Contains(value, "/internal/transport/")) {
					t.Errorf("host depends on implementation adapter: %s", value)
				}
			}

			if !strings.HasSuffix(path, "_templ.go") && !strings.Contains(rel, "/sqlc/") {
				ast.Inspect(file, func(node ast.Node) bool {
					switch node := node.(type) {
					case *ast.BasicLit:
						if node.Kind == token.STRING {
							value, err := strconv.Unquote(node.Value)
							if err == nil && sql.MatchString(value) {
								t.Errorf("production SQL must live in external .sql sources: %s", rel)
							}
						}
					case *ast.FuncDecl:
						if (tree == "provider" || tree == "testdata/external-provider" || tree == "providers") &&
							node.Name.Name == "init" {
							t.Errorf("provider registration has startup side effects: %s", rel)
						}
					}

					return true
				})
			}

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
