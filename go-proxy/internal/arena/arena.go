package arena

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"pi-proxy/internal/binlog"
	"pi-proxy/internal/config"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ArenaTurn struct {
	Turn    int    `json:"turn"`
	Speaker string `json:"speaker"`
	Model   string `json:"model"`
	Content string `json:"content"`
}

type Service struct {
	cfg       *config.Store
	log       *binlog.Logger
	strataSem chan struct{}
}

func NewService(cfg *config.Store, log *binlog.Logger, strataSem chan struct{}) *Service {
	return &Service{cfg: cfg, log: log, strataSem: strataSem}
}

func (s *Service) HandleStrataChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Model    string    `json:"model"`
		System   string    `json:"system"`
		Prompt   string    `json:"prompt"`
		Messages []Message `json:"messages"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024*1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	msgs := req.Messages
	if len(msgs) == 0 && req.Prompt != "" {
		if req.System != "" {
			msgs = append(msgs, Message{Role: "system", Content: req.System})
		}
		msgs = append(msgs, Message{Role: "user", Content: req.Prompt})
	}
	if req.Model == "" {
		req.Model = "qwen3.8-flash-next-sync-s"
	}

	c := s.cfg.Get()
	strataURL := fmt.Sprintf("http://%s:%d/v1/chat/completions", c.StrataHost, c.StrataPort)
	reply, err := s.callChat(r.Context(), strataURL, "", req.Model, msgs, true)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	s.log.Write(map[string]any{"event": "strata_chat", "model": req.Model, "replyLen": len(reply)})
	writeJSON(w, http.StatusOK, map[string]any{"model": req.Model, "reply": reply})
}

func (s *Service) HandleArenaRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Topic        string `json:"topic"`
		Turns        int    `json:"turns"`
		StrataModel  string `json:"strataModel"`
		StrataSystem string `json:"strataSystem"`
		SaaSBaseURL  string `json:"saasBaseUrl"`
		SaaSModel    string `json:"saasModel"`
		SaaSAPIKey   string `json:"saasApiKey"`
		SaaSSystem   string `json:"saasSystem"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024*1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c := s.cfg.Get()
	if req.Topic == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "topic is required"})
		return
	}
	if req.Turns <= 0 || req.Turns > 12 {
		req.Turns = 4
	}
	if req.StrataModel == "" {
		req.StrataModel = "qwen3.8-flash-next-sync-s"
	}
	if req.SaaSBaseURL == "" {
		req.SaaSBaseURL = c.SaaSBaseURL
	}
	if req.SaaSModel == "" {
		req.SaaSModel = c.SaaSModel
	}
	if req.SaaSAPIKey == "" {
		req.SaaSAPIKey = c.SaaSAPIKey
	}
	if req.SaaSBaseURL == "" || req.SaaSModel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "saasBaseUrl and saasModel are required (pass in body or PATCH /admin/config)",
		})
		return
	}

	if req.StrataSystem == "" {
		req.StrataSystem = "You are Sync-Strata (local open-weight AI). Discuss concisely and technically with the peer AI."
	}
	if req.SaaSSystem == "" {
		req.SaaSSystem = "You are a peer SaaS AI. Challenge assumptions constructively and collaborate concisely."
	}

	strataURL := fmt.Sprintf("http://%s:%d/v1/chat/completions", c.StrataHost, c.StrataPort)
	saasURL := strings.TrimRight(req.SaaSBaseURL, "/") + "/chat/completions"

	strataMsgs := []Message{{Role: "system", Content: req.StrataSystem}, {Role: "user", Content: "Topic: " + req.Topic}}
	saasMsgs := []Message{{Role: "system", Content: req.SaaSSystem}}

	var transcript []ArenaTurn
	for i := 1; i <= req.Turns; i++ {
		if i%2 == 1 {
			reply, err := s.callChat(r.Context(), strataURL, "", req.StrataModel, strataMsgs, true)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "transcript": transcript})
				return
			}
			transcript = append(transcript, ArenaTurn{Turn: i, Speaker: "strata", Model: req.StrataModel, Content: reply})
			strataMsgs = append(strataMsgs, Message{Role: "assistant", Content: reply})
			saasMsgs = append(saasMsgs, Message{Role: "user", Content: reply})
		} else {
			reply, err := s.callChat(r.Context(), saasURL, req.SaaSAPIKey, req.SaaSModel, saasMsgs, false)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "transcript": transcript})
				return
			}
			transcript = append(transcript, ArenaTurn{Turn: i, Speaker: "saas", Model: req.SaaSModel, Content: reply})
			saasMsgs = append(saasMsgs, Message{Role: "assistant", Content: reply})
			strataMsgs = append(strataMsgs, Message{Role: "user", Content: reply})
		}
	}

	s.log.Write(map[string]any{
		"event":      "arena_match",
		"topic":      req.Topic,
		"turns":      len(transcript),
		"saasModel":  req.SaaSModel,
		"transcript": transcript,
	})
	writeJSON(w, http.StatusOK, map[string]any{"topic": req.Topic, "transcript": transcript})
}

func (s *Service) HandleUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(uiHTML))
}

func (s *Service) callChat(ctx context.Context, endpoint, apiKey, model string, messages []Message, useStrataSem bool) (string, error) {
	if useStrataSem && s.strataSem != nil {
		select {
		case s.strataSem <- struct{}{}:
			defer func() { <-s.strataSem }()
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("chat HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Choices) == 0 {
		return "", fmt.Errorf("unexpected response: %s", string(raw))
	}
	return parsed.Choices[0].Message.Content, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

const uiHTML = `<!doctype html>
<html lang="ja">
<head><meta charset="utf-8"><title>pi-proxy Control & Strata Console</title>
<style>
body{font-family:system-ui,sans-serif;max-width:960px;margin:24px auto;padding:0 16px;background:#0f172a;color:#e2e8f0}
.card{background:#1e293b;border-radius:10px;padding:16px;margin-bottom:16px}
textarea,input{width:100%;box-sizing:border-box;background:#0f172a;color:#e2e8f0;border:1px solid #334155;border-radius:6px;padding:8px;margin:6px 0}
button{background:#38bdf8;color:#0f172a;border:0;border-radius:6px;padding:8px 14px;font-weight:600;cursor:pointer}
pre{background:#020617;padding:12px;border-radius:6px;overflow:auto;white-space:pre-wrap}
</style></head>
<body>
<h2>pi-proxy Control &amp; Strata Console</h2>
<div class="card">
  <h3>1. Strata 対話窓口</h3>
  <input id="tok" placeholder="Proxy Bearer Token (PI_PROXY_TOKEN; empty if unset)">
  <textarea id="prompt" rows="3" placeholder="Strata へのメッセージ"></textarea>
  <button onclick="chat()">送信</button>
  <pre id="chatOut"></pre>
</div>
<div class="card">
  <h3>2. Strata ↔ SaaS AI マッチング</h3>
  <input id="topic" placeholder="議論テーマ">
  <input id="saasUrl" placeholder="SaaS OpenAI互換 Base URL (例: https://api.openai.com/v1)">
  <input id="saasModel" placeholder="SaaS Model (例: gpt-4o-mini)">
  <input id="saasKey" type="password" placeholder="SaaS API Key (省略時はホスト設定値)">
  <button onclick="arena()">4ターン会話開始</button>
  <pre id="arenaOut"></pre>
</div>
<script>
const hdr = () => { const t = document.getElementById('tok').value.trim(); return t ? {'Content-Type':'application/json','Authorization':'Bearer '+t} : {'Content-Type':'application/json'}; };
async function chat(){
  document.getElementById('chatOut').textContent='...';
  const r=await fetch('/admin/strata/chat',{method:'POST',headers:hdr(),body:JSON.stringify({prompt:document.getElementById('prompt').value})});
  document.getElementById('chatOut').textContent=JSON.stringify(await r.json(),null,2);
}
async function arena(){
  document.getElementById('arenaOut').textContent='...';
  const r=await fetch('/admin/arena/run',{method:'POST',headers:hdr(),body:JSON.stringify({
    topic:document.getElementById('topic').value,
    saasBaseUrl:document.getElementById('saasUrl').value,
    saasModel:document.getElementById('saasModel').value,
    saasApiKey:document.getElementById('saasKey').value,
    turns:4
  })});
  document.getElementById('arenaOut').textContent=JSON.stringify(await r.json(),null,2);
}
</script></body></html>`
