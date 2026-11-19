package mcp

import (
	"path/filepath"
	"testing"
)

func TestManagerRegistry(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "mcp.json")
	m := NewManager(cfgPath)

	if err := m.Upsert(ServerConfig{
		Name:      "m5stack-flasher",
		Transport: "stdio",
		Command:   "uvx",
		Args:      []string{"m5-mcp"},
		Enabled:   true,
	}); err != nil {
		t.Fatal(err)
	}
	if len(m.List()) != 1 {
		t.Fatalf("expected 1 server")
	}
	if err := m.SetEnabled("m5stack-flasher", false); err != nil {
		t.Fatal(err)
	}
	if m.List()[0].Enabled {
		t.Fatal("expected disabled")
	}

	// Reload from persisted json
	m2 := NewManager(cfgPath)
	if len(m2.List()) != 1 || m2.List()[0].Name != "m5stack-flasher" {
		t.Fatalf("expected persisted server, got %+v", m2.List())
	}
	if err := m2.Delete("m5stack-flasher"); err != nil {
		t.Fatal(err)
	}
	if len(m2.List()) != 0 {
		t.Fatal("expected empty after delete")
	}
}
