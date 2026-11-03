package ch

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Queries name the Distributed tables directly (X_distributed). They used to
// write a placeholder that a function rewrote before sending, and nothing
// rewrites it any more: a query copied from old code would reach ClickHouse
// with the placeholder in it and fail only at run time.
func TestNoTablePlaceholdersInQueries(t *testing.T) {
	placeholder := "@@" + "table_"
	root := ".."
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "docs", "front":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(content), placeholder) {
			t.Errorf("%s: a table placeholder %sX@@ instead of the table name X_distributed", path, placeholder)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
