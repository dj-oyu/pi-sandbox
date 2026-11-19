package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowedReadAndDeltaPolling(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "--workspace--")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessFile := filepath.Join(wsDir, "s1.jsonl")
	lines := `{"type":"message","id":"1","timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":[{"type":"text","text":"q1"}]}}
{"type":"message","id":"2","timestamp":"2026-01-01T00:00:02Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"t1"},{"type":"text","text":"a1"}]}}
{"type":"message","id":"3","timestamp":"2026-01-01T00:00:03Z","message":{"role":"user","content":[{"type":"text","text":"q2"}]}}
{"type":"message","id":"4","timestamp":"2026-01-01T00:00:04Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"c1","name":"read","arguments":{"path":"x"}}]}}
`
	if err := os.WriteFile(sessFile, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	// Latest 2 entries
	res, err := Read(dir, "--workspace--/s1.jsonl", -1, -1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 2 || res.Entries[0].ID != "3" || res.Entries[1].ID != "4" {
		t.Fatalf("unexpected latest window: %+v", res.Entries)
	}
	if !res.HasMore || res.PrevCursor <= 0 {
		t.Fatalf("expected HasMore=true and PrevCursor>0, got %+v", res)
	}

	// Load older 2 entries before PrevCursor
	older, err := Read(dir, "--workspace--/s1.jsonl", -1, res.PrevCursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Entries) != 2 || older.Entries[0].ID != "1" || older.Entries[1].ID != "2" {
		t.Fatalf("unexpected older window: %+v", older.Entries)
	}
	if older.Entries[1].Thinking != "t1" {
		t.Fatalf("expected thinking preserved, got %q", older.Entries[1].Thinking)
	}
	if older.HasMore {
		t.Fatalf("expected HasMore=false at start of file")
	}

	// Append new line and poll delta from NextOffset
	f, err := os.OpenFile(sessFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"type":"message","id":"5","timestamp":"2026-01-01T00:00:05Z","message":{"role":"toolResult","toolName":"read","isError":false,"content":[{"type":"text","text":"ok"}]}}` + "\n")
	_ = f.Close()

	delta, err := Read(dir, "--workspace--/s1.jsonl", res.NextOffset, -1, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Entries) != 1 || delta.Entries[0].ID != "5" || delta.Entries[0].ToolName != "read" {
		t.Fatalf("unexpected delta: %+v", delta.Entries)
	}
}
