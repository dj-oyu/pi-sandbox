package dream

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type EventSummary struct {
	Ts             string   `json:"ts"`
	Event          string   `json:"event"`
	Turn           int      `json:"turn,omitempty"`
	ScoreBefore    *float64 `json:"scoreBefore,omitempty"`
	ScoreNow       *float64 `json:"scoreNow,omitempty"`
	PickedIds      []string `json:"pickedIds,omitempty"`
	PickedHeadings []string `json:"pickedHeadings,omitempty"`
	Reason         string   `json:"reason,omitempty"`
}

type MemoryEntry struct {
	ID          string `json:"id"`
	Tier        string `json:"tier"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	RecallCount int    `json:"recallCount"`
}

type MemorySummary struct {
	Exists       bool           `json:"exists"`
	Path         string         `json:"path"`
	Bytes        int            `json:"bytes"`
	TotalEntries int            `json:"totalEntries"`
	Tiers        map[string]int `json:"tiers"`
	Entries      []MemoryEntry  `json:"entries"`
	BloatWarning bool           `json:"bloatWarning"`
}

type VerifyPoint struct {
	Ts        string  `json:"ts"`
	Score     float64 `json:"score"`
	TargetMet bool    `json:"targetMet"`
}

type JudgeRecall struct {
	Turn   int    `json:"turn"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type JudgeMemory struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

type JudgeResult struct {
	Available bool          `json:"available"`
	Model     string        `json:"model,omitempty"`
	Verdict   string        `json:"verdict,omitempty"`
	Summary   string        `json:"summary,omitempty"`
	Recalls   []JudgeRecall `json:"recalls,omitempty"`
	Memory    []JudgeMemory `json:"memory,omitempty"`
	Error     string        `json:"error,omitempty"`
}

type EvalSummary struct {
	Ts           string       `json:"ts"`
	RunID        string       `json:"runId"`
	DreamEnabled bool         `json:"dreamEnabled"`
	HasVerifier  bool         `json:"hasVerifier"`
	TotalTurns   int          `json:"totalTurns"`
	Writes       int          `json:"writes"`
	Recalls      int          `json:"recalls"`
	Overhead     float64      `json:"overheadRatio"`
	FinalScore   *float64     `json:"finalScore,omitempty"`
	TargetMet    bool         `json:"targetMet"`
	Verdict      string       `json:"verdict"`
	Reasons      []string     `json:"reasons,omitempty"`
	Judge        *JudgeResult `json:"judge,omitempty"`
}

type StatusResponse struct {
	HasData         bool           `json:"hasData"`
	RunID           string         `json:"runId"`
	Enabled         bool           `json:"enabled"`
	IntervalMin     float64        `json:"intervalMin"`
	RecallProb      float64        `json:"recallProb"`
	StartedAt       string         `json:"startedAt"`
	LastEventAt     string         `json:"lastEventAt"`
	LastWriteAt     string         `json:"lastWriteAt"`
	NextWriteAt     string         `json:"nextWriteAt"`
	Writes          int            `json:"writes"`
	Recalls         int            `json:"recalls"`
	FollowThroughs  int            `json:"followThroughs"`
	GuardSkips      int            `json:"guardSkips"`
	PostRecallLifts int            `json:"postRecallLifts"`
	LatestScore     *float64       `json:"latestScore,omitempty"`
	TargetMet       bool           `json:"targetMet"`
	Memory          MemorySummary  `json:"memory"`
	RecentEvents    []EventSummary `json:"recentEvents"`
	VerifyPoints    []VerifyPoint  `json:"verifyPoints"`
	LatestEval      *EvalSummary   `json:"latestEval,omitempty"`
	RecentEvals     []EvalSummary  `json:"recentEvals"`
}

type rawDreamEvent struct {
	Ts             string   `json:"ts"`
	Event          string   `json:"event"`
	RunID          string   `json:"runId"`
	DreamEnabled   bool     `json:"dreamEnabled"`
	IntervalMin    float64  `json:"intervalMin"`
	RecallProb     float64  `json:"recallProb"`
	MemoryFile     string   `json:"memoryFile"`
	Turn           int      `json:"turn"`
	ScoreBefore    *float64 `json:"scoreBefore"`
	ScoreNow       *float64 `json:"scoreNow"`
	LatestScore    *float64 `json:"latestScore"`
	TargetMet      bool     `json:"targetMet"`
	PickedIds      []string `json:"pickedIds"`
	PickedHeadings []string `json:"pickedHeadings"`
	Reason         string   `json:"reason"`
}

type rawVerifyEvent struct {
	Ts        string   `json:"ts"`
	Score     *float64 `json:"score"`
	TargetMet bool     `json:"targetMet"`
}

var entryHeadingRe = regexp.MustCompile(`(?i)^###\s+\[(Principle|Insight|Dead End|Note|Epic|Nobel Prize|Genius|Insightful|Useful)\]\s*(?:([A-Za-z0-9_-]+)\s*[-—:]\s*)?(.+)$`)

func readMemory(workspace, relPath string, recallCounts map[string]int) MemorySummary {
	if relPath == "" {
		relPath = ".pi/memory/dream.md"
	}
	res := MemorySummary{
		Path:    relPath,
		Tiers:   map[string]int{},
		Entries: []MemoryEntry{},
	}
	if workspace == "" {
		return res
	}
	full := filepath.Join(workspace, filepath.FromSlash(relPath))
	data, err := os.ReadFile(full)
	if err != nil {
		return res
	}
	res.Exists = true
	res.Bytes = len(data)
	lines := strings.Split(string(data), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		m := entryHeadingRe.FindStringSubmatch(line)
		if len(m) > 3 {
			tier := m[1]
			id := m[2]
			if id == "" {
				id = "M" + string(rune('1'+len(res.Entries)))
			}
			title := strings.TrimSpace(m[3])
			var bodyParts []string
			for j := i + 1; j < len(lines) && len(bodyParts) < 3; j++ {
				nxt := strings.TrimSpace(strings.TrimRight(lines[j], "\r"))
				if strings.HasPrefix(nxt, "### ") || strings.HasPrefix(nxt, "## ") {
					break
				}
				if nxt != "" {
					bodyParts = append(bodyParts, nxt)
				}
			}
			body := strings.Join(bodyParts, " ")
			if len(body) > 260 {
				body = body[:260] + "..."
			}
			res.Tiers[tier]++
			res.TotalEntries++
			res.Entries = append(res.Entries, MemoryEntry{
				ID:          id,
				Tier:        tier,
				Title:       title,
				Body:        body,
				RecallCount: recallCounts[id],
			})
		}
	}
	res.BloatWarning = res.Bytes > 12*1024 || res.TotalEntries > 25
	return res
}

func readJSONL[T any](filePath string) []T {
	f, err := os.Open(filePath)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var v T
		if json.Unmarshal([]byte(line), &v) == nil {
			out = append(out, v)
		}
	}
	return out
}

func Status(sessionsDir, workspace string) StatusResponse {
	recallCounts := map[string]int{}
	resp := StatusResponse{
		Memory:       readMemory(workspace, ".pi/memory/dream.md", recallCounts),
		RecentEvents: []EventSummary{},
		VerifyPoints: []VerifyPoint{},
		RecentEvals:  []EvalSummary{},
	}
	if sessionsDir == "" {
		return resp
	}
	events := readJSONL[rawDreamEvent](filepath.Join(sessionsDir, "dream-metrics.jsonl"))
	verifies := readJSONL[rawVerifyEvent](filepath.Join(sessionsDir, "verify-metrics.jsonl"))
	evals := readJSONL[EvalSummary](filepath.Join(sessionsDir, "dream-evals.jsonl"))

	// Count total recalls per memory ID across all runs (cross-session reuse!).
	for _, ev := range events {
		if ev.Event == "dream_recall" {
			for _, id := range ev.PickedIds {
				recallCounts[id]++
			}
		}
	}

	if len(evals) > 0 {
		// Deduplicate by runId keeping the latest evaluation per run.
		seen := map[string]bool{}
		var dedup []EvalSummary
		for i := len(evals) - 1; i >= 0 && len(dedup) < 10; i-- {
			if seen[evals[i].RunID] {
				continue
			}
			seen[evals[i].RunID] = true
			dedup = append(dedup, evals[i])
		}
		resp.RecentEvals = dedup
		latest := dedup[0]
		resp.LatestEval = &latest
	}

	if len(verifies) > 0 {
		lastV := verifies[len(verifies)-1]
		resp.LatestScore = lastV.Score
		resp.TargetMet = lastV.TargetMet
	}
	if len(events) == 0 {
		resp.Memory = readMemory(workspace, ".pi/memory/dream.md", recallCounts)
		return resp
	}

	latestRunID := ""
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Event == "session_start" && events[i].RunID != "" {
			latestRunID = events[i].RunID
			break
		}
	}
	if latestRunID == "" {
		latestRunID = events[len(events)-1].RunID
	}

	resp.HasData = true
	resp.RunID = latestRunID
	memFile := ".pi/memory/dream.md"

	var recalls []rawDreamEvent
	for _, ev := range events {
		if ev.RunID != latestRunID {
			continue
		}
		if ev.Ts != "" {
			resp.LastEventAt = ev.Ts
		}
		switch ev.Event {
		case "session_start":
			resp.Enabled = ev.DreamEnabled
			resp.IntervalMin = ev.IntervalMin
			resp.RecallProb = ev.RecallProb
			resp.StartedAt = ev.Ts
			if ev.MemoryFile != "" {
				memFile = ev.MemoryFile
			}
		case "dream_write":
			resp.Writes++
			resp.LastWriteAt = ev.Ts
		case "dream_recall":
			resp.Recalls++
			recalls = append(recalls, ev)
		case "recall_followthrough":
			resp.FollowThroughs++
		case "guard_skip":
			resp.GuardSkips++
		case "session_turn_final":
			if ev.LatestScore != nil {
				resp.LatestScore = ev.LatestScore
			}
			if ev.TargetMet {
				resp.TargetMet = true
			}
		}
		if ev.Event != "session_start" {
			resp.RecentEvents = append(resp.RecentEvents, EventSummary{
				Ts:             ev.Ts,
				Event:          ev.Event,
				Turn:           ev.Turn,
				ScoreBefore:    ev.ScoreBefore,
				ScoreNow:       ev.ScoreNow,
				PickedIds:      ev.PickedIds,
				PickedHeadings: ev.PickedHeadings,
				Reason:         ev.Reason,
			})
		}
	}

	if len(resp.RecentEvents) > 12 {
		resp.RecentEvents = resp.RecentEvents[len(resp.RecentEvents)-12:]
	}

	// Collect verify points belonging to this run (only when verifier exists).
	if startT, err := time.Parse(time.RFC3339Nano, resp.StartedAt); err == nil {
		for _, v := range verifies {
			if v.Score == nil {
				continue
			}
			if tv, err := time.Parse(time.RFC3339Nano, v.Ts); err == nil && !tv.Before(startT.Add(-5*time.Second)) {
				resp.VerifyPoints = append(resp.VerifyPoints, VerifyPoint{
					Ts:        v.Ts,
					Score:     *v.Score,
					TargetMet: v.TargetMet,
				})
			}
		}
	}

	// Estimate next write time when Dream is enabled.
	if resp.Enabled && resp.IntervalMin > 0 {
		baseTs := resp.LastWriteAt
		if baseTs == "" {
			baseTs = resp.StartedAt
		}
		if t, err := time.Parse(time.RFC3339Nano, baseTs); err == nil {
			resp.NextWriteAt = t.Add(time.Duration(resp.IntervalMin * float64(time.Minute))).UTC().Format(time.RFC3339)
		}
	}

	for _, rec := range recalls {
		if rec.ScoreBefore == nil {
			continue
		}
		t0, err := time.Parse(time.RFC3339Nano, rec.Ts)
		if err != nil {
			continue
		}
		checked := 0
		lifted := false
		for _, v := range verifies {
			tv, err := time.Parse(time.RFC3339Nano, v.Ts)
			if err != nil || !tv.After(t0) || tv.Sub(t0) > 10*time.Minute {
				continue
			}
			checked++
			if v.Score != nil && *v.Score > *rec.ScoreBefore {
				lifted = true
				break
			}
			if checked >= 3 {
				break
			}
		}
		if lifted {
			resp.PostRecallLifts++
		}
	}

	// Match latestEval specifically for this run if available.
	for i := range resp.RecentEvals {
		if resp.RecentEvals[i].RunID == latestRunID {
			ev := resp.RecentEvals[i]
			resp.LatestEval = &ev
			break
		}
	}

	resp.Memory = readMemory(workspace, memFile, recallCounts)
	return resp
}
