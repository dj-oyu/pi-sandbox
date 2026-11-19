package fsview

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSafeAndListDir(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "src"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "src", "main.py"), []byte("print(1)\n"), 0o644)

	if _, _, err := ResolveSafe(root, "../secret.txt"); err == nil {
		t.Fatal("expected path traversal error")
	}

	entries, err := ListDir(root, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "src" {
		t.Fatalf("expected only src (node_modules skipped), got %+v", entries)
	}

	data, _, trunc, err := ReadFilePreview(root, "src/main.py")
	if err != nil || trunc || string(data) != "print(1)\n" {
		t.Fatalf("unexpected preview: %q trunc=%v err=%v", string(data), trunc, err)
	}
}
