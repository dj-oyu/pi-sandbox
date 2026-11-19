package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var validName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type ServerConfig struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"` // "stdio" | "http"
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Enabled   bool              `json:"enabled"`
}

type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type ServerStatus struct {
	ServerConfig
	Running   bool       `json:"running"`
	Tools     []ToolInfo `json:"tools,omitempty"`
	LastError string     `json:"lastError,omitempty"`
}

type Manager struct {
	mu         sync.RWMutex
	configPath string
	servers    map[string]ServerConfig
	procs      map[string]*stdioProc
	toolsCache map[string][]ToolInfo
	lastErrors map[string]string
}

func NewManager(configPath string) *Manager {
	m := &Manager{
		configPath: configPath,
		servers:    make(map[string]ServerConfig),
		procs:      make(map[string]*stdioProc),
		toolsCache: make(map[string][]ToolInfo),
		lastErrors: make(map[string]string),
	}
	m.load()
	return m
}

func (m *Manager) load() {
	if m.configPath == "" {
		return
	}
	raw, err := os.ReadFile(m.configPath)
	if err != nil {
		return
	}
	var list []ServerConfig
	if err := json.Unmarshal(raw, &list); err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range list {
		if validName.MatchString(s.Name) {
			m.servers[s.Name] = s
		}
	}
}

func (m *Manager) saveLocked() error {
	if m.configPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.configPath), 0o755); err != nil {
		return err
	}
	list := make([]ServerConfig, 0, len(m.servers))
	for _, s := range m.servers {
		list = append(list, s)
	}
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.configPath, raw, 0o644)
}

func (m *Manager) List() []ServerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ServerStatus, 0, len(m.servers))
	for _, s := range m.servers {
		p := m.procs[s.Name]
		running := p != nil && !p.closed.Load()
		out = append(out, ServerStatus{
			ServerConfig: s,
			Running:      running,
			Tools:        m.toolsCache[s.Name],
			LastError:    m.lastErrors[s.Name],
		})
	}
	return out
}

func (m *Manager) Upsert(cfg ServerConfig) error {
	cfg.Name = strings.TrimSpace(cfg.Name)
	if !validName.MatchString(cfg.Name) {
		return errors.New("invalid server name (use [a-zA-Z0-9_-])")
	}
	cfg.Transport = strings.ToLower(strings.TrimSpace(cfg.Transport))
	if cfg.Transport != "stdio" && cfg.Transport != "http" {
		return errors.New("transport must be stdio or http")
	}
	if cfg.Transport == "stdio" && strings.TrimSpace(cfg.Command) == "" {
		return errors.New("command is required for stdio transport")
	}
	if cfg.Transport == "http" && strings.TrimSpace(cfg.URL) == "" {
		return errors.New("url is required for http transport")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.procs[cfg.Name]; ok {
		old.close()
		delete(m.procs, cfg.Name)
	}
	delete(m.lastErrors, cfg.Name)
	delete(m.toolsCache, cfg.Name)
	m.servers[cfg.Name] = cfg
	return m.saveLocked()
}

func (m *Manager) Delete(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.procs[name]; ok {
		old.close()
		delete(m.procs, name)
	}
	delete(m.servers, name)
	delete(m.toolsCache, name)
	delete(m.lastErrors, name)
	return m.saveLocked()
}

func (m *Manager) SetEnabled(name string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, ok := m.servers[name]
	if !ok {
		return os.ErrNotExist
	}
	cfg.Enabled = enabled
	m.servers[name] = cfg
	if !enabled {
		if old, ok := m.procs[name]; ok {
			old.close()
			delete(m.procs, name)
		}
	}
	return m.saveLocked()
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, p := range m.procs {
		p.close()
		delete(m.procs, k)
	}
}

func (m *Manager) Probe(ctx context.Context, name string) ([]ToolInfo, error) {
	reqPayload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
		"params":  map[string]any{},
	})
	respRaw, err := m.ProxyJSONRPC(ctx, name, reqPayload)
	if err != nil {
		m.mu.Lock()
		m.lastErrors[name] = err.Error()
		m.mu.Unlock()
		return nil, err
	}
	var parsed struct {
		Result struct {
			Tools []ToolInfo `json:"tools"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respRaw, &parsed); err != nil {
		return nil, err
	}
	if parsed.Error != nil {
		return nil, errors.New(parsed.Error.Message)
	}
	m.mu.Lock()
	m.toolsCache[name] = parsed.Result.Tools
	delete(m.lastErrors, name)
	m.mu.Unlock()
	return parsed.Result.Tools, nil
}

func (m *Manager) ProxyJSONRPC(ctx context.Context, name string, rawBody []byte) ([]byte, error) {
	m.mu.RLock()
	cfg, ok := m.servers[name]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("mcp server %q not found", name)
	}
	if !cfg.Enabled {
		return nil, fmt.Errorf("mcp server %q is disabled", name)
	}

	if cfg.Transport == "http" {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(rawBody))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		for k, v := range cfg.Headers {
			req.Header.Set(k, v)
		}
		resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("mcp http %d: %s", resp.StatusCode, string(out))
		}
		return out, nil
	}

	proc, err := m.getOrStartStdio(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return proc.forward(ctx, rawBody)
}

func (m *Manager) getOrStartStdio(ctx context.Context, cfg ServerConfig) (*stdioProc, error) {
	m.mu.Lock()
	if p, ok := m.procs[cfg.Name]; ok && !p.closed.Load() {
		m.mu.Unlock()
		return p, nil
	}
	delete(m.procs, cfg.Name)
	m.mu.Unlock()

	p, err := startStdioProc(ctx, cfg)
	if err != nil {
		m.mu.Lock()
		m.lastErrors[cfg.Name] = err.Error()
		m.mu.Unlock()
		return nil, err
	}
	m.mu.Lock()
	m.procs[cfg.Name] = p
	delete(m.lastErrors, cfg.Name)
	m.mu.Unlock()
	return p, nil
}

// --- newline-delimited JSON-RPC over stdio (MCP standard) ---

type stdioProc struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[string]chan []byte
	seq       atomic.Int64
	closed    atomic.Bool
}

func startStdioProc(ctx context.Context, cfg ServerConfig) (*stdioProc, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	if len(cfg.Env) > 0 {
		env := os.Environ()
		for k, v := range cfg.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	p := &stdioProc{
		cmd:     cmd,
		stdin:   stdin,
		pending: make(map[string]chan []byte),
	}
	go p.readLoop(stdout)
	go func() {
		_ = cmd.Wait()
		p.closed.Store(true)
	}()

	initCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	initReq, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "__init__",
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]string{"name": "pi-proxy", "version": "1.0.0"},
		},
	})
	if _, err := p.forward(initCtx, initReq); err != nil {
		p.close()
		return nil, fmt.Errorf("mcp initialize: %w", err)
	}
	_ = p.writeLine([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`))
	return p, nil
}

func (p *stdioProc) close() {
	if p.closed.Swap(true) {
		return
	}
	_ = p.stdin.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

func (p *stdioProc) writeLine(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if _, err := p.stdin.Write(trimmed); err != nil {
		return err
	}
	_, err := p.stdin.Write([]byte("\n"))
	return err
}

func (p *stdioProc) forward(ctx context.Context, rawBody []byte) ([]byte, error) {
	if p.closed.Load() {
		return nil, errors.New("mcp process closed")
	}
	var msg map[string]any
	if err := json.Unmarshal(rawBody, &msg); err != nil {
		return nil, err
	}
	origID, hasID := msg["id"]
	if !hasID {
		return []byte(`{"jsonrpc":"2.0","result":{}}`), p.writeLine(rawBody)
	}

	internalID := fmt.Sprintf("r-%d", p.seq.Add(1))
	msg["id"] = internalID
	rewritten, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}

	ch := make(chan []byte, 1)
	p.pendingMu.Lock()
	p.pending[internalID] = ch
	p.pendingMu.Unlock()
	defer func() {
		p.pendingMu.Lock()
		delete(p.pending, internalID)
		p.pendingMu.Unlock()
	}()

	if err := p.writeLine(rewritten); err != nil {
		return nil, err
	}

	select {
	case respRaw := <-ch:
		var respMap map[string]any
		if err := json.Unmarshal(respRaw, &respMap); err != nil {
			return respRaw, nil
		}
		respMap["id"] = origID
		return json.Marshal(respMap)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *stdioProc) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var env struct {
			ID any `json:"id"`
		}
		if err := json.Unmarshal(line, &env); err != nil || env.ID == nil {
			continue
		}
		key := fmt.Sprintf("%v", env.ID)
		p.pendingMu.Lock()
		ch := p.pending[key]
		p.pendingMu.Unlock()
		if ch != nil {
			cp := append([]byte(nil), line...)
			ch <- cp
		}
	}
	p.closed.Store(true)
}
