package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pi-proxy/internal/fsview"
)

type Location struct {
	Path         string `json:"path,omitempty"`
	URI          string `json:"uri,omitempty"`
	Line         int    `json:"line"`
	Character    int    `json:"character"`
	EndLine      int    `json:"endLine"`
	EndCharacter int    `json:"endCharacter"`
	External     bool   `json:"external,omitempty"`
}

type HoverResult struct {
	Contents string `json:"contents"`
	Found    bool   `json:"found"`
}

type LangStatus struct {
	Language string `json:"language"`
	Running  bool   `json:"running"`
	Command  string `json:"command"`
}

type Manager struct {
	mu      sync.Mutex
	clients map[string]*client
}

func NewManager() *Manager {
	return &Manager{clients: make(map[string]*client)}
}

func LangForPath(relPath string) (lang string, lspLangID string, ok bool) {
	ext := strings.ToLower(filepath.Ext(relPath))
	switch ext {
	case ".go":
		return "go", "go", true
	case ".rs":
		return "rust", "rust", true
	case ".ts":
		return "typescript", "typescript", true
	case ".tsx":
		return "typescript", "typescriptreact", true
	case ".js", ".mjs", ".cjs":
		return "typescript", "javascript", true
	case ".jsx":
		return "typescript", "javascriptreact", true
	case ".py", ".pyi":
		return "python", "python", true
	default:
		return "", "", false
	}
}

func commandForLang(lang string) (string, []string, error) {
	switch lang {
	case "go":
		if p, err := exec.LookPath("gopls"); err == nil {
			return p, nil, nil
		}
		return "", nil, errors.New("gopls not found on host PATH")
	case "rust":
		if p, err := exec.LookPath("rust-analyzer"); err == nil {
			return p, nil, nil
		}
		return "", nil, errors.New("rust-analyzer not found on host PATH")
	case "typescript":
		if p, err := exec.LookPath("typescript-language-server"); err == nil {
			return p, []string{"--stdio"}, nil
		}
		npx := "npx"
		if runtime.GOOS == "windows" {
			if p, err := exec.LookPath("npx.cmd"); err == nil {
				npx = p
			}
		}
		if p, err := exec.LookPath(npx); err == nil {
			return p, []string{"-y", "-p", "typescript-language-server", "-p", "typescript", "typescript-language-server", "--stdio"}, nil
		}
		return "", nil, errors.New("typescript-language-server / npx not found on host PATH")
	case "python":
		if p, err := exec.LookPath("basedpyright-langserver"); err == nil {
			return p, []string{"--stdio"}, nil
		}
		if p, err := exec.LookPath("pyright-langserver"); err == nil {
			return p, []string{"--stdio"}, nil
		}
		if p, err := exec.LookPath("uvx"); err == nil {
			return p, []string{"--from", "basedpyright", "basedpyright-langserver", "--stdio"}, nil
		}
		return "", nil, errors.New("basedpyright-langserver / uvx not found on host PATH")
	default:
		return "", nil, fmt.Errorf("unsupported language: %s", lang)
	}
}

func (m *Manager) Status() []LangStatus {
	langs := []string{"python", "typescript", "go", "rust"}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]LangStatus, 0, len(langs))
	for _, l := range langs {
		exe, args, err := commandForLang(l)
		cmdStr := "unavailable"
		if err == nil {
			cmdStr = filepath.Base(exe)
			if len(args) > 0 {
				cmdStr += " " + strings.Join(args, " ")
			}
		}
		running := false
		for k, c := range m.clients {
			if strings.HasPrefix(k, l+":") && c.alive() {
				running = true
				break
			}
		}
		out = append(out, LangStatus{
			Language: l,
			Running:  running,
			Command:  cmdStr,
		})
	}
	return out
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, c := range m.clients {
		c.close()
		delete(m.clients, k)
	}
}

func (m *Manager) getClient(ctx context.Context, workspace, lang string) (*client, error) {
	absRoot, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	key := lang + ":" + absRoot

	m.mu.Lock()
	c, ok := m.clients[key]
	if ok && c.alive() {
		m.mu.Unlock()
		return c, nil
	}
	delete(m.clients, key)
	m.mu.Unlock()

	exe, args, err := commandForLang(lang)
	if err != nil {
		return nil, err
	}

	c, err = startClient(ctx, absRoot, lang, exe, args)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.clients[key] = c
	m.mu.Unlock()
	return c, nil
}

func (m *Manager) Hover(ctx context.Context, workspace, relPath string, line, character int) (HoverResult, error) {
	lang, lspLangID, ok := LangForPath(relPath)
	if !ok {
		return HoverResult{}, nil
	}
	target, _, err := fsview.ResolveSafe(workspace, relPath)
	if err != nil {
		return HoverResult{}, err
	}
	c, err := m.getClient(ctx, workspace, lang)
	if err != nil {
		return HoverResult{}, err
	}
	uri := pathToURI(target)
	if err := c.ensureOpen(target, uri, lspLangID); err != nil {
		return HoverResult{}, err
	}

	raw, err := c.call(ctx, "textDocument/hover", map[string]any{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": line, "character": character},
	})
	if err != nil {
		return HoverResult{}, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return HoverResult{}, nil
	}
	var resp struct {
		Contents any `json:"contents"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || resp.Contents == nil {
		return HoverResult{}, nil
	}
	text := extractHoverText(resp.Contents)
	if strings.TrimSpace(text) == "" {
		return HoverResult{}, nil
	}
	return HoverResult{Contents: text, Found: true}, nil
}

func (m *Manager) Definition(ctx context.Context, workspace, relPath string, line, character int) ([]Location, error) {
	lang, lspLangID, ok := LangForPath(relPath)
	if !ok {
		return nil, nil
	}
	target, _, err := fsview.ResolveSafe(workspace, relPath)
	if err != nil {
		return nil, err
	}
	c, err := m.getClient(ctx, workspace, lang)
	if err != nil {
		return nil, err
	}
	uri := pathToURI(target)
	if err := c.ensureOpen(target, uri, lspLangID); err != nil {
		return nil, err
	}

	raw, err := c.call(ctx, "textDocument/definition", map[string]any{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": line, "character": character},
	})
	if err != nil {
		return nil, err
	}
	return parseLocations(workspace, raw), nil
}

// --- JSON-RPC stdio client ---

type client struct {
	root    string
	lang    string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[int64]chan rpcResponse
	nextID    atomic.Int64

	docMu   sync.Mutex
	docVers map[string]docState

	closed atomic.Bool
}

type docState struct {
	version int
	mtime   time.Time
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func startClient(ctx context.Context, root, lang, exe string, args []string) (*client, error) {
	cmd := exec.Command(exe, args...)
	cmd.Dir = root
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

	c := &client{
		root:    root,
		lang:    lang,
		cmd:     cmd,
		stdin:   stdin,
		pending: make(map[int64]chan rpcResponse),
		docVers: make(map[string]docState),
	}
	go c.readLoop(stdout)
	go func() {
		_ = cmd.Wait()
		c.closed.Store(true)
	}()

	initCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	initParams := map[string]any{
		"processId": os.Getpid(),
		"rootUri":   pathToURI(root),
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"hover": map[string]any{
					"contentFormat": []string{"markdown", "plaintext"},
				},
				"definition": map[string]any{
					"linkSupport": true,
				},
			},
		},
	}
	if _, err := c.call(initCtx, "initialize", initParams); err != nil {
		c.close()
		return nil, fmt.Errorf("lsp initialize (%s): %w", lang, err)
	}
	_ = c.notify("initialized", map[string]any{})

	if lang == "python" {
		if extra := detectPythonExtraPaths(root); len(extra) > 0 {
			_ = c.notify("workspace/didChangeConfiguration", map[string]any{
				"settings": map[string]any{
					"python": map[string]any{
						"analysis": map[string]any{
							"extraPaths": extra,
						},
					},
				},
			})
		}
	}
	return c, nil
}

func detectPythonExtraPaths(root string) []string {
	var paths []string
	// Container uv creates .venv/lib/python3.x/site-packages; Windows venv creates .venv/Lib/site-packages
	patterns := []string{
		filepath.Join(root, ".venv", "lib", "python*", "site-packages"),
		filepath.Join(root, ".venv", "Lib", "site-packages"),
	}
	for _, pat := range patterns {
		matches, _ := filepath.Glob(pat)
		for _, m := range matches {
			if st, err := os.Stat(m); err == nil && st.IsDir() {
				paths = append(paths, m)
			}
		}
	}
	return paths
}

func (c *client) alive() bool {
	return !c.closed.Load()
}

func (c *client) close() {
	if c.closed.Swap(true) {
		return
	}
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

func (c *client) ensureOpen(fullPath, uri, langID string) error {
	st, err := os.Stat(fullPath)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(fullPath)
	if err != nil {
		return err
	}

	c.docMu.Lock()
	defer c.docMu.Unlock()
	prev, ok := c.docVers[uri]
	if !ok {
		c.docVers[uri] = docState{version: 1, mtime: st.ModTime()}
		return c.notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri":        uri,
				"languageId": langID,
				"version":    1,
				"text":       string(raw),
			},
		})
	}
	if st.ModTime().After(prev.mtime) {
		nextVer := prev.version + 1
		c.docVers[uri] = docState{version: nextVer, mtime: st.ModTime()}
		return c.notify("textDocument/didChange", map[string]any{
			"textDocument": map[string]any{
				"uri":     uri,
				"version": nextVer,
			},
			"contentChanges": []map[string]any{
				{"text": string(raw)},
			},
		})
	}
	return nil
}

func (c *client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if c.closed.Load() {
		return nil, errors.New("lsp client closed")
	}
	id := c.nextID.Add(1)
	ch := make(chan rpcResponse, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	if err := c.send(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("lsp %s: %s", method, resp.Error.Message)
		}
		return resp.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *client) notify(method string, params any) error {
	return c.send(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func (c *client) send(msg any) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(payload))
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := io.WriteString(c.stdin, frame); err != nil {
		return err
	}
	_, err = c.stdin.Write(payload)
	return err
}

func (c *client) readLoop(r io.Reader) {
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		contentLen := -1
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				c.closed.Store(true)
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			if strings.HasPrefix(strings.ToLower(line), "content-length:") {
				val := strings.TrimSpace(line[len("content-length:"):])
				if n, err := strconv.Atoi(val); err == nil {
					contentLen = n
				}
			}
		}
		if contentLen <= 0 {
			continue
		}
		body := make([]byte, contentLen)
		if _, err := io.ReadFull(br, body); err != nil {
			c.closed.Store(true)
			return
		}
		var env struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			continue
		}
		// Respond to server-initiated requests (e.g. workspace/configuration) with null
		if env.ID != nil && env.Method != "" {
			_ = c.send(map[string]any{
				"jsonrpc": "2.0",
				"id":      *env.ID,
				"result":  nil,
			})
			continue
		}
		if env.ID != nil {
			c.pendingMu.Lock()
			ch := c.pending[*env.ID]
			c.pendingMu.Unlock()
			if ch != nil {
				ch <- rpcResponse{Result: env.Result, Error: env.Error}
			}
		}
	}
}

func pathToURI(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	slashed := filepath.ToSlash(abs)
	if !strings.HasPrefix(slashed, "/") {
		// Windows drive letter C:/... -> /C:/...
		slashed = "/" + slashed
	}
	u := url.URL{Scheme: "file", Path: slashed}
	return u.String()
}

func uriToPath(rawURI string) string {
	u, err := url.Parse(rawURI)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	p := u.Path
	if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p)
}

func parseLocations(workspace string, raw json.RawMessage) []Location {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	absRoot, _ := filepath.Abs(workspace)
	convert := func(uri string, rng struct {
		Start struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"start"`
		End struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"end"`
	}) Location {
		loc := Location{
			URI:          uri,
			Line:         rng.Start.Line,
			Character:    rng.Start.Character,
			EndLine:      rng.End.Line,
			EndCharacter: rng.End.Character,
			External:     true,
		}
		if fsPath := uriToPath(uri); fsPath != "" && absRoot != "" {
			if rel, err := filepath.Rel(absRoot, fsPath); err == nil && !strings.HasPrefix(rel, "..") {
				loc.Path = filepath.ToSlash(rel)
				loc.External = false
			}
		}
		return loc
	}

	type rawRng struct {
		Start struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"start"`
		End struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"end"`
	}
	type rawLoc struct {
		URI            string  `json:"uri"`
		Range          rawRng  `json:"range"`
		TargetURI      string  `json:"targetUri"`
		TargetRange    rawRng  `json:"targetSelectionRange"`
		TargetFullRng  rawRng  `json:"targetRange"`
	}

	trimmed := bytes.TrimSpace(raw)
	var items []rawLoc
	if len(trimmed) > 0 && trimmed[0] == '[' {
		_ = json.Unmarshal(trimmed, &items)
	} else {
		var single rawLoc
		if err := json.Unmarshal(trimmed, &single); err == nil {
			items = []rawLoc{single}
		}
	}

	out := make([]Location, 0, len(items))
	for _, it := range items {
		if it.TargetURI != "" {
			rng := it.TargetRange
			if rng.Start.Line == 0 && rng.End.Line == 0 && it.TargetFullRng.End.Line > 0 {
				rng = it.TargetFullRng
			}
			out = append(out, convert(it.TargetURI, rng))
		} else if it.URI != "" {
			out = append(out, convert(it.URI, it.Range))
		}
	}
	return out
}

func extractHoverText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		if val, ok := x["value"].(string); ok {
			return val
		}
	case []any:
		var parts []string
		for _, item := range x {
			if s := extractHoverText(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}
