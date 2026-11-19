package config

import "testing"

func TestPatchAndAllowlist(t *testing.T) {
	s := NewStore(RuntimeConfig{
		AllowedHosts:      []string{"github.com", "*.espressif.com"},
		StrataConcurrency: 1,
		AutoHealEnabled:   true,
	})
	if !s.IsHostAllowed("github.com") || !s.IsHostAllowed("docs.espressif.com") {
		t.Fatal("expected initial hosts to be allowed")
	}
	if s.IsHostAllowed("evil.example.com") {
		t.Fatal("expected unknown host to be blocked")
	}

	newHosts := []string{"example.org"}
	newConc := 2
	s.Patch(PatchRequest{AllowedHosts: &newHosts, StrataConcurrency: &newConc})
	if s.IsHostAllowed("github.com") || !s.IsHostAllowed("example.org") {
		t.Fatal("expected dynamic patch to update allowlist")
	}
	if s.Get().StrataConcurrency != 2 {
		t.Fatal("expected concurrency=2")
	}
}
