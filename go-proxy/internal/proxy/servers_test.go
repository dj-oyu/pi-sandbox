package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInjectAdviceIntoChatBody(t *testing.T) {
	orig := []byte(`{"model":"qwen3.8","messages":[{"role":"user","content":"fix bug"}]}`)
	updated, ok := injectAdviceIntoChatBody(orig, "Use ExpressionAttributeNames for reserved words")
	if !ok {
		t.Fatal("expected injection to succeed")
	}
	var parsed struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(updated, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(parsed.Messages))
	}
	last := parsed.Messages[1]
	if last.Role != "user" || !strings.Contains(last.Content, "ExpressionAttributeNames") {
		t.Fatalf("unexpected injected message: %+v", last)
	}
}
