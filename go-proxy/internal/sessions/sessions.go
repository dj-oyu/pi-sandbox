package sessions

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type SessionInfo struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

type Entry struct {
	ID         string         `json:"id"`
	Timestamp  string         `json:"timestamp"`
	Kind       string         `json:"kind"` // user | assistant | toolResult | meta
	Role       string         `json:"role,omitempty"`
	Text       string         `json:"text,omitempty"`
	Thinking   string         `json:"thinking,omitempty"`
	ToolCalls  []ToolCallItem `json:"toolCalls,omitempty"`
	ToolName   string         `json:"toolName,omitempty"`
	ToolCallID string         `json:"toolCallId,omitempty"`
	IsError    bool           `json:"isError,omitempty"`
	Usage      map[string]any `json:"usage,omitempty"`
	Model      string         `json:"model,omitempty"`
	StopReason string         `json:"stopReason,omitempty"`
}

type ToolCallItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type SessionReadResult struct {
	ID         string  `json:"id"`
	Path       string  `json:"path"`
	Entries    []Entry `json:"entries"`
	NextOffset int64   `json:"nextOffset"`
	PrevCursor int64   `json:"prevCursor"`
	HasMore    bool    `json:"hasMore"`
	Size       int64   `json:"size"`
}

type indexedEntry struct {
	startOff int64
	entry    Entry
}

func List(sessionsDir string) ([]SessionInfo, error) {
	if sessionsDir == "" {
		return nil, nil
	}
	var out []SessionInfo
	_ = filepath.WalkDir(sessionsDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".jsonl") {
			return nil
		}
		rel, err := filepath.Rel(sessionsDir, p)
		if err != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if !strings.Contains(relSlash, "/") && (d.Name() == "verify-metrics.jsonl" || d.Name() == "perturbation.jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, SessionInfo{
			ID:      relSlash,
			Path:    relSlash,
			Size:    info.Size(),
			ModTime: info.ModTime().UTC().Format(time.RFC3339Nano),
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		return out[i].ModTime > out[j].ModTime
	})
	if len(out) > 50 {
		out = out[:50]
	}
	return out, nil
}

// Read returns either:
// - delta entries from offset >= 0 (when offset >= 0 and before < 0)
// - a bounded window of up to `limit` entries ending at `before` (or EOF when before < 0 and offset < 0).
func Read(sessionsDir, id string, offset, before int64, limit int) (SessionReadResult, error) {
	var res SessionReadResult
	if sessionsDir == "" {
		return res, os.ErrNotExist
	}
	if limit <= 0 || limit > 200 {
		limit = 40
	}
	cleanRel := filepath.Clean(filepath.FromSlash(id))
	if cleanRel == "." || strings.HasPrefix(cleanRel, "..") || filepath.IsAbs(cleanRel) {
		return res, os.ErrPermission
	}
	full := filepath.Join(sessionsDir, cleanRel)
	relCheck, err := filepath.Rel(sessionsDir, full)
	if err != nil || strings.HasPrefix(relCheck, "..") {
		return res, os.ErrPermission
	}

	f, err := os.Open(full)
	if err != nil {
		return res, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return res, err
	}
	res.ID = id
	res.Path = filepath.ToSlash(cleanRel)
	res.Size = st.Size()

	// Mode 1: Incremental tail polling from a known byte offset
	if offset >= 0 && before < 0 {
		if offset > st.Size() {
			offset = 0
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return res, err
		}
		items, consumed := scanEntries(f, offset, -1)
		res.NextOffset = consumed
		res.Entries = unwrapEntries(items)
		return res, nil
	}

	// Mode 2: Bounded window (either latest `limit` entries, or `limit` entries before byte cursor `before`)
	endLimit := st.Size()
	if before >= 0 && before < endLimit {
		endLimit = before
	}
	items, consumed := scanEntries(f, 0, endLimit)
	res.NextOffset = consumed
	if len(items) > limit {
		startIdx := len(items) - limit
		res.HasMore = true
		res.PrevCursor = items[startIdx].startOff
		items = items[startIdx:]
	} else {
		res.HasMore = false
		res.PrevCursor = 0
	}
	res.Entries = unwrapEntries(items)
	return res, nil
}

func scanEntries(f *os.File, startOffset, maxByte int64) ([]indexedEntry, int64) {
	reader := bufio.NewReaderSize(f, 64*1024)
	pos := startOffset
	var items []indexedEntry
	for {
		if maxByte >= 0 && pos >= maxByte {
			break
		}
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if line[len(line)-1] == '\n' {
				lineStart := pos
				pos += int64(len(line))
				if maxByte >= 0 && pos > maxByte {
					pos = lineStart
					break
				}
				if entry, ok := parseLine(line); ok {
					items = append(items, indexedEntry{startOff: lineStart, entry: entry})
				}
			} else if err == io.EOF {
				break
			}
		}
		if err != nil {
			break
		}
	}
	return items, pos
}

func unwrapEntries(items []indexedEntry) []Entry {
	out := make([]Entry, len(items))
	for i, it := range items {
		out[i] = it.entry
	}
	return out
}

func parseLine(raw []byte) (Entry, bool) {
	var top struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Timestamp string          `json:"timestamp"`
		ModelID   string          `json:"modelId"`
		Message   json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return Entry{}, false
	}
	if top.Type == "model_change" {
		return Entry{
			ID:        top.ID,
			Timestamp: top.Timestamp,
			Kind:      "meta",
			Text:      "model: " + top.ModelID,
		}, true
	}
	if top.Type != "message" || len(top.Message) == 0 {
		return Entry{}, false
	}

	var msg struct {
		Role       string            `json:"role"`
		Content    []json.RawMessage `json:"content"`
		ToolName   string            `json:"toolName"`
		ToolCallID string            `json:"toolCallId"`
		IsError    bool              `json:"isError"`
		Usage      map[string]any    `json:"usage"`
		Model      string            `json:"model"`
		StopReason string            `json:"stopReason"`
	}
	if err := json.Unmarshal(top.Message, &msg); err != nil {
		return Entry{}, false
	}

	e := Entry{
		ID:         top.ID,
		Timestamp:  top.Timestamp,
		Kind:       msg.Role,
		Role:       msg.Role,
		ToolName:   msg.ToolName,
		ToolCallID: msg.ToolCallID,
		IsError:    msg.IsError,
		Usage:      msg.Usage,
		Model:      msg.Model,
		StopReason: msg.StopReason,
	}

	var texts []string
	var thinkings []string
	for _, blockRaw := range msg.Content {
		var blk struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(blockRaw, &blk); err != nil {
			continue
		}
		switch blk.Type {
		case "text":
			if blk.Text != "" {
				texts = append(texts, blk.Text)
			}
		case "thinking":
			if blk.Thinking != "" {
				thinkings = append(thinkings, blk.Thinking)
			}
		case "toolCall":
			e.ToolCalls = append(e.ToolCalls, ToolCallItem{
				ID:        blk.ID,
				Name:      blk.Name,
				Arguments: string(blk.Arguments),
			})
		}
	}
	e.Text = strings.Join(texts, "\n")
	e.Thinking = strings.Join(thinkings, "\n")
	return e, true
}

// LatestInitialPrompt returns the first user message text from the most recent session.
func LatestInitialPrompt(sessionsDir string) string {
	list, err := List(sessionsDir)
	if err != nil || len(list) == 0 {
		return ""
	}
	res, err := Read(sessionsDir, list[0].ID, -1, -1, 200)
	if err != nil {
		return ""
	}
	// Read from start if file has more entries
	if res.HasMore {
		full := filepath.Join(sessionsDir, filepath.FromSlash(list[0].ID))
		if f, err := os.Open(full); err == nil {
			defer f.Close()
			items, _ := scanEntries(f, 0, -1)
			for _, it := range items {
				if it.entry.Kind == "user" && strings.TrimSpace(it.entry.Text) != "" {
					return it.entry.Text
				}
			}
		}
	}
	for _, e := range res.Entries {
		if e.Kind == "user" && strings.TrimSpace(e.Text) != "" {
			return e.Text
		}
	}
	return ""
}

type ErrorSnippet struct {
	Timestamp string `json:"timestamp"`
	Source    string `json:"source"`
	Detail    string `json:"detail"`
}

// RecentErrors returns up to `max` recent tool errors from the latest Pi session.
func RecentErrors(sessionsDir string, max int) []ErrorSnippet {
	if max <= 0 {
		max = 5
	}
	list, err := List(sessionsDir)
	if err != nil || len(list) == 0 {
		return nil
	}
	res, err := Read(sessionsDir, list[0].ID, -1, -1, 150)
	if err != nil {
		return nil
	}
	var out []ErrorSnippet
	for i := len(res.Entries) - 1; i >= 0 && len(out) < max; i-- {
		e := res.Entries[i]
		if e.IsError {
			txt := e.Text
			if len(txt) > 1200 {
				txt = txt[:1200] + "\n...(truncated)"
			}
			out = append(out, ErrorSnippet{
				Timestamp: e.Timestamp,
				Source:    "pi_tool:" + e.ToolName,
				Detail:    txt,
			})
		}
	}
	return out
}
