package arena

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pi-proxy/internal/binlog"
	"pi-proxy/internal/config"
	"pi-proxy/internal/events"
	"pi-proxy/internal/fsview"
	"pi-proxy/internal/jobs"
	"pi-proxy/internal/sessions"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ArenaTurn struct {
	Turn       int      `json:"turn"`
	Speaker    string   `json:"speaker"`
	Model      string   `json:"model"`
	Content    string   `json:"content"`
	FilesRead  []string `json:"filesRead,omitempty"`
	ErrorsRead bool     `json:"errorsRead,omitempty"`
}

type Service struct {
	cfg           *config.Store
	log           *binlog.Logger
	strataSem     chan struct{}
	hub           *events.Hub
	runner        *jobs.Runner
	adviceMu      sync.Mutex
	pendingAdvice string
}

func (s *Service) QueueAdvice(text string) {
	s.adviceMu.Lock()
	s.pendingAdvice = strings.TrimSpace(text)
	s.adviceMu.Unlock()
	if s.hub != nil {
		s.hub.Publish("advice", map[string]any{"pending": s.PeekAdvice()})
	}
}

func (s *Service) PeekAdvice() string {
	s.adviceMu.Lock()
	defer s.adviceMu.Unlock()
	return s.pendingAdvice
}

func (s *Service) PopAdvice() string {
	s.adviceMu.Lock()
	adv := s.pendingAdvice
	s.pendingAdvice = ""
	s.adviceMu.Unlock()
	if adv != "" && s.hub != nil {
		s.hub.Publish("advice", map[string]any{"pending": ""})
	}
	return adv
}

func (s *Service) HandleAdvice(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"pending": s.PeekAdvice()})
	case http.MethodPost:
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 256*1024)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		txt := strings.TrimSpace(body.Text)
		if txt == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text is required"})
			return
		}
		s.QueueAdvice(txt)
		s.log.Write(map[string]any{"event": "arena_advice_queued", "len": len(txt)})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pending": s.PeekAdvice()})
	case http.MethodDelete:
		s.QueueAdvice("")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pending": ""})
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func NewService(cfg *config.Store, log *binlog.Logger, strataSem chan struct{}) *Service {
	return &Service{cfg: cfg, log: log, strataSem: strataSem}
}

func (s *Service) SetHub(h *events.Hub) {
	s.hub = h
}

func (s *Service) SetRunner(r *jobs.Runner) {
	s.runner = r
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
		Topic         string   `json:"topic"`
		Turns         int      `json:"turns"`
		StrataModel   string   `json:"strataModel"`
		StrataSystem  string   `json:"strataSystem"`
		SaaSBaseURL   string   `json:"saasBaseUrl"`
		SaaSModel     string   `json:"saasModel"`
		SaaSAPIKey    string   `json:"saasApiKey"`
		SaaSSystem    string   `json:"saasSystem"`
		InitialPrompt string   `json:"initialPrompt"`
		AllowedFiles  []string `json:"allowedFiles"`
		AllowErrors   bool     `json:"allowErrors"`
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
		if c.StrataSystem != "" {
			req.StrataSystem = c.StrataSystem
		} else {
			req.StrataSystem = "You are Sync-Strata (local open-weight AI). Discuss concisely and technically with the peer AI."
		}
	}
	if req.SaaSSystem == "" {
		if c.SaaSSystem != "" {
			req.SaaSSystem = c.SaaSSystem
		} else {
			req.SaaSSystem = "You are a peer SaaS AI. Challenge assumptions constructively and collaborate concisely."
		}
	}

	strataURL := fmt.Sprintf("http://%s:%d/v1/chat/completions", c.StrataHost, c.StrataPort)
	saasFormat := c.SaaSAPIFormat
	if saasFormat == "" {
		saasFormat = "openai"
	}

	allowedSet := make(map[string]bool, len(req.AllowedFiles))
	cleanAllowed := make([]string, 0, len(req.AllowedFiles))
	for _, f := range req.AllowedFiles {
		nf := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(f))))
		if nf != "" && nf != "." && !strings.HasPrefix(nf, "..") && !allowedSet[nf] {
			allowedSet[nf] = true
			cleanAllowed = append(cleanAllowed, nf)
		}
	}

	var sharedCtx []string
	if strings.TrimSpace(req.InitialPrompt) != "" {
		sharedCtx = append(sharedCtx, "## Initial User Prompt\n"+strings.TrimSpace(req.InitialPrompt))
	}
	if len(cleanAllowed) > 0 {
		sharedCtx = append(sharedCtx, "## Visible Workspace Files\n"+strings.Join(cleanAllowed, "\n")+"\n\nUse `read_file` with any path above when you need to inspect file contents.")
	}
	if req.AllowErrors {
		sharedCtx = append(sharedCtx, "## Error Inspection\nIf needed, call `get_recent_errors` to inspect recent Pi tool or build job failures.")
	}
	sharedSuffix := strings.Join(sharedCtx, "\n\n")
	fullStrataSystem := req.StrataSystem
	fullSaaSSystem := req.SaaSSystem
	if sharedSuffix != "" {
		fullStrataSystem += "\n\n" + sharedSuffix
		fullSaaSSystem += "\n\n" + sharedSuffix
	}

	strataMsgs := []Message{{Role: "system", Content: fullStrataSystem}, {Role: "user", Content: "Topic: " + req.Topic}}
	saasMsgs := []Message{{Role: "system", Content: fullSaaSSystem}}

	var transcript []ArenaTurn
	for i := 1; i <= req.Turns; i++ {
		if i%2 == 1 {
			reply, filesRead, errsRead, err := s.callModelWithTools(r.Context(), c, "openai", strataURL, "", req.StrataModel, strataMsgs, allowedSet, req.AllowErrors, true)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "transcript": transcript})
				return
			}
			turn := ArenaTurn{Turn: i, Speaker: "strata", Model: req.StrataModel, Content: reply, FilesRead: filesRead, ErrorsRead: errsRead}
			transcript = append(transcript, turn)
			if s.hub != nil {
				s.hub.Publish("arena_turn", map[string]any{"topic": req.Topic, "turn": turn})
			}
			strataMsgs = append(strataMsgs, Message{Role: "assistant", Content: reply})
			saasMsgs = append(saasMsgs, Message{Role: "user", Content: reply})
		} else {
			reply, filesRead, errsRead, err := s.callModelWithTools(r.Context(), c, saasFormat, req.SaaSBaseURL, req.SaaSAPIKey, req.SaaSModel, saasMsgs, allowedSet, req.AllowErrors, false)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "transcript": transcript})
				return
			}
			turn := ArenaTurn{Turn: i, Speaker: "saas", Model: req.SaaSModel, Content: reply, FilesRead: filesRead, ErrorsRead: errsRead}
			transcript = append(transcript, turn)
			if s.hub != nil {
				s.hub.Publish("arena_turn", map[string]any{"topic": req.Topic, "turn": turn})
			}
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

func (s *Service) HandleArenaHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	c := s.cfg.Get()
	recs, err := binlog.ReadTail(filepath.Join(c.LogDir, "control.bin"), 300)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var matches []map[string]any
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i]["event"] == "arena_match" {
			matches = append(matches, recs[i])
			if len(matches) >= 30 {
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"matches": matches})
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

func (s *Service) callModelWithTools(ctx context.Context, c config.RuntimeConfig, format, baseURL, apiKey, model string, baseMsgs []Message, allowedSet map[string]bool, allowErrors, useStrataSem bool) (string, []string, bool, error) {
	base := strings.TrimRight(baseURL, "/")
	var endpoint string
	if format == "anthropic" {
		if strings.HasSuffix(base, "/v1") {
			endpoint = base + "/messages"
		} else if strings.HasSuffix(base, "/v1/messages") {
			endpoint = base
		} else {
			endpoint = base + "/v1/messages"
		}
	} else {
		if strings.HasSuffix(base, "/chat/completions") {
			endpoint = base
		} else if strings.HasSuffix(base, "/v1") {
			endpoint = base + "/chat/completions"
		} else {
			endpoint = base + "/v1/chat/completions"
		}
	}

	readAllowed := func(rawPath string) string {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(rawPath))))
		clean = strings.TrimPrefix(clean, "/workspace/")
		if !allowedSet[clean] {
			return fmt.Sprintf("Error: access denied for %q (file not shared)", rawPath)
		}
		data, _, trunc, err := fsview.ReadFilePreview(c.Workspace, clean)
		if err != nil {
			return fmt.Sprintf("Error reading %q: %v", clean, err)
		}
		if len(data) > 64*1024 {
			data = data[:64*1024]
			trunc = true
		}
		out := string(data)
		if trunc {
			out += "\n...(truncated)"
		}
		return out
	}

	readErrors := func() string {
		if !allowErrors {
			return "Error: error inspection is disabled by user"
		}
		var items []sessions.ErrorSnippet
		items = append(items, sessions.RecentErrors(c.SessionsDir, 5)...)
		if s.runner != nil {
			for _, j := range s.runner.List() {
				if j.Status == jobs.StatusFailed || j.Status == jobs.StatusTimedOut {
					detail := j.Stderr
					if detail == "" {
						detail = j.Stdout
					}
					if len(detail) > 1000 {
						detail = detail[len(detail)-1000:]
					}
					items = append(items, sessions.ErrorSnippet{
						Timestamp: j.FinishedAt,
						Source:    "job:" + j.Command,
						Detail:    detail,
					})
					if len(items) >= 8 {
						break
					}
				}
			}
		}
		if len(items) == 0 {
			return "No recent errors found."
		}
		b, _ := json.MarshalIndent(items, "", "  ")
		return string(b)
	}

	var sysText string
	convo := make([]map[string]any, 0, len(baseMsgs)+4)
	for _, m := range baseMsgs {
		if format == "anthropic" && m.Role == "system" {
			if sysText != "" {
				sysText += "\n"
			}
			sysText += m.Content
		} else {
			convo = append(convo, map[string]any{"role": m.Role, "content": m.Content})
		}
	}

	var filesRead []string
	seenRead := make(map[string]bool)
	errorsRead := false

	buildOpenAITools := func() []map[string]any {
		var tools []map[string]any
		if len(allowedSet) > 0 {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "read_file",
					"description": "Read a visible workspace file by relative path",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path": map[string]any{"type": "string"},
						},
						"required": []string{"path"},
					},
				},
			})
		}
		if allowErrors {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "get_recent_errors",
					"description": "Inspect recent Pi tool execution or build job errors",
					"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
				},
			})
		}
		return tools
	}

	buildAnthropicTools := func() []map[string]any {
		var tools []map[string]any
		if len(allowedSet) > 0 {
			tools = append(tools, map[string]any{
				"name":        "read_file",
				"description": "Read a visible workspace file by relative path",
				"input_schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
					"required": []string{"path"},
				},
			})
		}
		if allowErrors {
			tools = append(tools, map[string]any{
				"name":        "get_recent_errors",
				"description": "Inspect recent Pi tool execution or build job errors",
				"input_schema": map[string]any{"type": "object", "properties": map[string]any{}},
			})
		}
		return tools
	}

	for step := 0; step < 6; step++ {
		var bodyMap map[string]any
		if format == "anthropic" {
			bodyMap = map[string]any{
				"model":      model,
				"max_tokens": 2048,
				"messages":   convo,
			}
			if sysText != "" {
				bodyMap["system"] = sysText
			}
			if tools := buildAnthropicTools(); len(tools) > 0 {
				bodyMap["tools"] = tools
			}
		} else {
			bodyMap = map[string]any{
				"model":    model,
				"messages": convo,
				"stream":   false,
			}
			if tools := buildOpenAITools(); len(tools) > 0 {
				bodyMap["tools"] = tools
			}
		}

		raw, err := s.doModelRequest(ctx, c, format, endpoint, apiKey, bodyMap, useStrataSem)
		if err != nil {
			return "", filesRead, errorsRead, err
		}

		if format == "anthropic" {
			var parsed struct {
				Content []struct {
					Type  string          `json:"type"`
					Text  string          `json:"text"`
					ID    string          `json:"id"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content"`
			}
			if err := json.Unmarshal(raw, &parsed); err != nil {
				return "", filesRead, errorsRead, fmt.Errorf("unexpected anthropic response: %s", string(raw))
			}
			var texts []string
			var toolUses []struct {
				ID   string
				Name string
				Path string
			}
			var rawBlocks []any
			for _, blk := range parsed.Content {
				switch blk.Type {
				case "text":
					if blk.Text != "" {
						texts = append(texts, blk.Text)
						rawBlocks = append(rawBlocks, map[string]any{"type": "text", "text": blk.Text})
					}
				case "tool_use":
					var args struct {
						Path string `json:"path"`
					}
					_ = json.Unmarshal(blk.Input, &args)
					rawBlocks = append(rawBlocks, map[string]any{"type": "tool_use", "id": blk.ID, "name": blk.Name, "input": json.RawMessage(blk.Input)})
					toolUses = append(toolUses, struct {
						ID   string
						Name string
						Path string
					}{ID: blk.ID, Name: blk.Name, Path: args.Path})
				}
			}
			if len(toolUses) == 0 {
				if len(texts) == 0 {
					return "", filesRead, errorsRead, fmt.Errorf("empty anthropic content: %s", string(raw))
				}
				return strings.Join(texts, "\n"), filesRead, errorsRead, nil
			}
			convo = append(convo, map[string]any{"role": "assistant", "content": rawBlocks})
			var results []map[string]any
			for _, tu := range toolUses {
				var resText string
				switch tu.Name {
				case "read_file":
					if !seenRead[tu.Path] {
						seenRead[tu.Path] = true
						filesRead = append(filesRead, tu.Path)
					}
					resText = readAllowed(tu.Path)
				case "get_recent_errors":
					errorsRead = true
					resText = readErrors()
				default:
					resText = "Error: unsupported tool " + tu.Name
				}
				results = append(results, map[string]any{
					"type":        "tool_result",
					"tool_use_id": tu.ID,
					"content":     resText,
				})
			}
			convo = append(convo, map[string]any{"role": "user", "content": results})
			continue
		}

		// OpenAI format
		var parsed struct {
			Choices []struct {
				Message struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Choices) == 0 {
			return "", filesRead, errorsRead, fmt.Errorf("unexpected response: %s", string(raw))
		}
		msg := parsed.Choices[0].Message
		if len(msg.ToolCalls) == 0 {
			return msg.Content, filesRead, errorsRead, nil
		}
		convo = append(convo, map[string]any{
			"role":       "assistant",
			"content":    msg.Content,
			"tool_calls": msg.ToolCalls,
		})
		for _, tc := range msg.ToolCalls {
			var resText string
			switch tc.Function.Name {
			case "read_file":
				var args struct {
					Path string `json:"path"`
				}
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
				if !seenRead[args.Path] {
					seenRead[args.Path] = true
					filesRead = append(filesRead, args.Path)
				}
				resText = readAllowed(args.Path)
			case "get_recent_errors":
				errorsRead = true
				resText = readErrors()
			default:
				resText = "Error: unsupported tool " + tc.Function.Name
			}
			convo = append(convo, map[string]any{
				"role":         "tool",
				"tool_call_id": tc.ID,
				"content":      resText,
			})
		}
	}
	return "", filesRead, errorsRead, fmt.Errorf("exceeded max tool_call rounds")
}

func (s *Service) doModelRequest(ctx context.Context, c config.RuntimeConfig, format, endpoint, apiKey string, bodyMap map[string]any, useStrataSem bool) ([]byte, error) {
	if useStrataSem && s.strataSem != nil {
		select {
		case s.strataSem <- struct{}{}:
			defer func() { <-s.strataSem }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	payload, _ := json.Marshal(bodyMap)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if !useStrataSem {
		if format == "anthropic" {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
		authType := c.SaaSAuthType
		if authType == "" {
			if format == "anthropic" {
				authType = "x-api-key"
			} else {
				authType = "bearer"
			}
		}
		if apiKey != "" {
			switch authType {
			case "x-api-key":
				req.Header.Set("x-api-key", apiKey)
			case "bearer":
				req.Header.Set("Authorization", "Bearer "+apiKey)
			case "custom":
				if c.SaaSCustomHeader != "" {
					req.Header.Set(c.SaaSCustomHeader, apiKey)
				}
			}
		}
		for k, v := range c.SaaSExtraHeaders {
			req.Header.Set(k, v)
		}
	}
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("chat HTTP %d: %s", resp.StatusCode, string(raw))
	}
	return raw, nil
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
