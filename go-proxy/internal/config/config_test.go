package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestSaaSPersistenceDPAPI(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "saas-config.json")
	s := NewStore(RuntimeConfig{SaaSConfigPath: cfgPath})

	fmtVal := "anthropic"
	urlVal := "https://api.anthropic.com"
	mdlVal := "claude-sonnet-4-5"
	keyVal := "sk-ant-super-secret-123"
	s.Patch(PatchRequest{
		SaaSAPIFormat: &fmtVal,
		SaaSBaseURL:   &urlVal,
		SaaSModel:     &mdlVal,
		SaaSAPIKey:    &keyVal,
	})

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), keyVal) {
		t.Fatal("plaintext api key must not be stored on disk")
	}

	s2 := NewStore(RuntimeConfig{SaaSConfigPath: cfgPath})
	got := s2.Get()
	if got.SaaSAPIFormat != fmtVal || got.SaaSBaseURL != urlVal || got.SaaSModel != mdlVal {
		t.Fatalf("unexpected reloaded config: %+v", got)
	}
	if !got.SaaSKeySet || got.SaaSAPIKey != keyVal {
		t.Fatalf("expected decrypted key %q, got %q (keySet=%v)", keyVal, got.SaaSAPIKey, got.SaaSKeySet)
	}
}
