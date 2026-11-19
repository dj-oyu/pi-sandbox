package binlog

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteRotateAndExportJSONL(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "audit.bin")
	l, err := NewLogger(logPath, 160, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		l.Write(map[string]any{"event": "test", "seq": i})
	}

	var buf bytes.Buffer
	n, err := ExportJSONL(logPath, &buf, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("expected exported records > 0")
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	for _, line := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("invalid json line %q: %v", line, err)
		}
	}
}
