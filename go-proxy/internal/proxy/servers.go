package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"pi-proxy/internal/arena"
	"pi-proxy/internal/binlog"
	"pi-proxy/internal/config"
	"pi-proxy/internal/events"
	"pi-proxy/internal/fsview"
	"pi-proxy/internal/github"
	"pi-proxy/internal/health"
	"pi-proxy/internal/jobs"
	"pi-proxy/internal/lsp"
	"pi-proxy/internal/mcp"
	"pi-proxy/internal/sessions"
	"pi-proxy/internal/strata"
	"pi-proxy/internal/ui"
)

var (
	ghCIAllowed = []*regexp.Regexp{
		regexp.MustCompile(`^/repos/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/actions/runs(\?[A-Za-z0-9_=&%.-]+)?$`),
		regexp.MustCompile(`^/repos/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/actions/runs/\d+/jobs(\?[A-Za-z0-9_=&%.-]+)?$`),
		regexp.MustCompile(`^/repos/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/actions/jobs/\d+/logs$`),
	}
	espValidTargets = map[string]bool{
		"esp32": true, "esp32s2": true, "esp32s3": true,
		"esp32c2": true, "esp32c3": true, "esp32c5": true,
		"esp32c6": true, "esp32h2": true, "esp32p4": true,
	}
)

type speedTracker struct {
	mu        sync.RWMutex
	tokPerSec float64
}

func (st *speedTracker) set(v float64) {
	st.mu.Lock()
	st.tokPerSec = v
	st.mu.Unlock()
}

func (st *speedTracker) get() float64 {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.tokPerSec
}

type ServerDeps struct {
	Cfg       *config.Store
	APILog    *binlog.Logger
	EgressLog *binlog.Logger
	CtrlLog   *binlog.Logger
	Runner    *jobs.Runner
	Monitor   *health.Monitor
	Arena     *arena.Service
	Hub       *events.Hub
	LSP         *lsp.Manager
	MCP         *mcp.Manager
	StrataQueue *strata.Queue
	speed       *speedTracker
}

func NewAPIHandler(d ServerDeps) http.Handler {
	if d.speed == nil {
		d.speed = &speedTracker{}
	}
	c := d.Cfg.Get()
	strataURL, _ := url.Parse(fmt.Sprintf("http://%s:%d", c.StrataHost, c.StrataPort))
	strataRP := httputil.NewSingleHostReverseProxy(strataURL)
	strataRP.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		d.APILog.Write(map[string]any{"event": "upstream_error", "url": r.URL.RequestURI(), "error": err.Error()})
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Bad Gateway", "detail": err.Error()})
	}
	uiHandler := ui.Handler()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/favicon.ico" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/admin/ui" || strings.HasPrefix(r.URL.Path, "/admin/ui/") {
			if r.URL.Path == "/admin/ui" {
				http.Redirect(w, r, "/admin/ui/", http.StatusMovedPermanently)
				return
			}
			uiHandler.ServeHTTP(w, r)
			return
		}
		if !checkAuth(d.Cfg, r) {
			d.APILog.Write(map[string]any{"event": "auth_denied", "remoteIp": r.RemoteAddr, "method": r.Method, "url": r.URL.RequestURI()})
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
			return
		}

		switch {
		case r.URL.Path == "/admin/events":
			handleSSE(d, w, r)
		case r.URL.Path == "/admin/config":
			handleConfig(d, w, r)
		case r.URL.Path == "/admin/health":
			writeJSON(w, http.StatusOK, map[string]any{"targets": d.Monitor.Snapshot()})
		case r.URL.Path == "/admin/strata/queue":
			var items []strata.QueueItem
			if d.StrataQueue != nil {
				items = d.StrataQueue.Snapshot()
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": items})
		case r.URL.Path == "/admin/jobs":
			writeJSON(w, http.StatusOK, map[string]any{"jobs": d.Runner.List()})
		case strings.HasPrefix(r.URL.Path, "/admin/jobs/"):
			id := strings.TrimPrefix(r.URL.Path, "/admin/jobs/")
			if j, ok := d.Runner.Get(id); ok {
				writeJSON(w, http.StatusOK, j)
			} else {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
			}
		case r.URL.Path == "/admin/logs/recent":
			handleLogsRecent(d, w, r)
		case r.URL.Path == "/admin/logs/export":
			handleLogExport(d, w, r)
		case r.URL.Path == "/admin/pi/sessions":
			handlePiSessions(d, w, r)
		case r.URL.Path == "/admin/pi/session":
			handlePiSessionRead(d, w, r)
		case r.URL.Path == "/admin/fs/tree":
			handleFSTree(d, w, r)
		case r.URL.Path == "/admin/fs/raw":
			handleFSRaw(d, w, r)
		case r.URL.Path == "/admin/github/status":
			c := d.Cfg.Get()
			writeJSON(w, http.StatusOK, github.Status(r.Context(), c.Workspace, c.GHExe))
		case r.URL.Path == "/admin/github/create-repo":
			handleGHCreateRepo(d, w, r)
		case r.URL.Path == "/admin/github/push":
			handleGHPush(d, w, r)
		case r.URL.Path == "/admin/github/pr":
			handleGHCreatePR(d, w, r)
		case r.URL.Path == "/admin/lsp/status":
			if d.LSP == nil {
				writeJSON(w, http.StatusOK, map[string]any{"languages": []any{}})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"languages": d.LSP.Status()})
		case r.URL.Path == "/admin/lsp/hover":
			handleLSPHover(d, w, r)
		case r.URL.Path == "/admin/lsp/definition":
			handleLSPDefinition(d, w, r)
		case r.URL.Path == "/admin/mcp/servers":
			handleMCPServers(d, w, r)
		case strings.HasPrefix(r.URL.Path, "/admin/mcp/servers/"):
			handleMCPServerAction(d, w, r)
		case strings.HasPrefix(r.URL.Path, "/mcp/"):
			handleMCPGateway(d, w, r)
		case r.URL.Path == "/admin/strata/chat":
			d.Arena.HandleStrataChat(w, r)
		case r.URL.Path == "/admin/arena/run":
			d.Arena.HandleArenaRun(w, r)
		case r.URL.Path == "/admin/arena/history":
			d.Arena.HandleArenaHistory(w, r)
		case r.URL.Path == "/admin/arena/context":
			handleArenaContext(d, w, r)
		case r.URL.Path == "/admin/arena/advice":
			d.Arena.HandleAdvice(w, r)
		case r.URL.Path == "/admin/saas/presets":
			handleSaaSPresets(d, w, r)
		case r.URL.Path == "/admin/saas/presets/activate":
			handleSaaSPresetActivate(d, w, r)
		case strings.HasPrefix(r.URL.Path, "/gh-ci/"):
			handleGHCI(d, w, r)
		case r.URL.Path == "/esp-idf/run":
			handleESPIDF(d, w, r)
		default:
			handleStrataProxy(d, strataRP, w, r)
		}
	})
}

func NewEgressHandler(cfg *config.Store, egressLog *binlog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			handleConnect(cfg, egressLog, w, r)
			return
		}
		host := r.URL.Hostname()
		if !cfg.IsHostAllowed(host) {
			egressLog.Write(map[string]any{"event": "block", "remoteIp": r.RemoteAddr, "method": r.Method, "host": host, "url": r.URL.String()})
			http.Error(w, "Blocked by pi-sandbox allowlist: "+host, http.StatusForbidden)
			return
		}
		egressLog.Write(map[string]any{"event": "allow_http", "remoteIp": r.RemoteAddr, "method": r.Method, "host": host})
		outReq, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		outReq.Header = r.Header.Clone()
		resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(outReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vals := range resp.Header {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
}

func handleConnect(cfg *config.Store, egressLog *binlog.Logger, w http.ResponseWriter, r *http.Request) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
		port = "443"
	}
	if !cfg.IsHostAllowed(host) {
		egressLog.Write(map[string]any{"event": "block_connect", "remoteIp": r.RemoteAddr, "host": host, "port": port})
		http.Error(w, "Blocked by pi-sandbox allowlist", http.StatusForbidden)
		return
	}
	upstream, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 10*time.Second)
	if err != nil {
		egressLog.Write(map[string]any{"event": "connect_error", "remoteIp": r.RemoteAddr, "host": host, "error": err.Error()})
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "Hijack unsupported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	egressLog.Write(map[string]any{"event": "allow_connect", "remoteIp": r.RemoteAddr, "host": host, "port": port})
	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	go func() {
		defer upstream.Close()
		defer clientConn.Close()
		_, _ = io.Copy(upstream, clientConn)
	}()
	go func() {
		defer upstream.Close()
		defer clientConn.Close()
		_, _ = io.Copy(clientConn, upstream)
	}()
}

func handleConfig(d ServerDeps, w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, d.Cfg.Get())
		return
	}
	if r.Method == http.MethodPatch {
		var patch config.PatchRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1024*1024)).Decode(&patch); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		updated := d.Cfg.Patch(patch)
		d.CtrlLog.Write(map[string]any{"event": "config_patched"})
		writeJSON(w, http.StatusOK, updated)
		return
	}
	http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
}

func handleLogExport(d ServerDeps, w http.ResponseWriter, r *http.Request) {
	stream := r.URL.Query().Get("stream")
	if stream == "" {
		stream = "api-audit"
	}
	if stream != "api-audit" && stream != "egress" && stream != "control" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid stream"})
		return
	}
	tail, _ := strconv.Atoi(r.URL.Query().Get("tail"))
	binPath := filepath.Join(d.Cfg.Get().LogDir, stream+".bin")
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	if _, err := binlog.ExportJSONL(binPath, w, tail); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func handleGHCI(d ServerDeps, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Only GET is allowed on /gh-ci"})
		return
	}
	ghPath := strings.TrimPrefix(r.URL.RequestURI(), "/gh-ci")
	allowed := false
	for _, re := range ghCIAllowed {
		if re.MatchString(ghPath) {
			allowed = true
			break
		}
	}
	if !allowed || strings.Contains(ghPath, "..") {
		d.APILog.Write(map[string]any{"event": "gh_ci_denied", "url": r.URL.RequestURI()})
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Path not allowed on /gh-ci"})
		return
	}
	job := d.Runner.RunSync(r.Context(), "gh-ci", "gh api "+ghPath, d.Cfg.Get().GHExe, []string{"api", ghPath}, "", 30*time.Second)
	d.APILog.Write(map[string]any{"event": "gh_ci_call", "jobId": job.ID, "url": r.URL.RequestURI(), "exitCode": job.ExitCode, "durationMs": job.DurationMs})
	if job.ExitCode != 0 {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "gh api failed", "jobId": job.ID, "detail": job.Stderr})
		return
	}
	if strings.HasSuffix(ghPath, "/logs") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(job.Stdout))
}

func handleESPIDF(d ServerDeps, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method Not Allowed"})
		return
	}
	c := d.Cfg.Get()
	if c.Workspace == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "PI_WORKSPACE is not set"})
		return
	}
	var body struct {
		Action     string `json:"action"`
		Target     string `json:"target"`
		TimeoutSec int    `json:"timeoutSec"`
		Async      bool   `json:"async"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256*1024)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	var idfCmd string
	timeout := 180 * time.Second
	switch body.Action {
	case "build":
		idfCmd = "idf.py build"
		if body.TimeoutSec >= 10 && body.TimeoutSec <= 300 {
			timeout = time.Duration(body.TimeoutSec) * time.Second
		}
	case "fullclean":
		idfCmd = "idf.py fullclean"
		timeout = 30 * time.Second
	case "set-target":
		if !espValidTargets[body.Target] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid target: " + body.Target})
			return
		}
		idfCmd = "idf.py set-target " + body.Target
		timeout = 120 * time.Second
	case "qemu":
		idfCmd = "idf.py qemu"
		timeout = 15 * time.Second
		if body.TimeoutSec >= 3 && body.TimeoutSec <= 60 {
			timeout = time.Duration(body.TimeoutSec) * time.Second
		}
	default:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Unsupported action: " + body.Action})
		return
	}

	psScript := fmt.Sprintf(". '%s' | Out-Null; Set-Location -LiteralPath '%s'; %s",
		strings.ReplaceAll(c.ESPProfile, "'", "''"),
		strings.ReplaceAll(c.Workspace, "'", "''"),
		idfCmd,
	)
	psArgs := []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", psScript}

	if body.Async {
		job := d.Runner.RunAsync("esp-idf", idfCmd, "powershell.exe", psArgs, c.Workspace, timeout)
		d.APILog.Write(map[string]any{"event": "esp_idf_queued", "action": body.Action, "jobId": job.ID})
		writeJSON(w, http.StatusAccepted, job)
		return
	}

	job := d.Runner.RunSync(r.Context(), "esp-idf", idfCmd, "powershell.exe", psArgs, c.Workspace, timeout)
	d.APILog.Write(map[string]any{"event": "esp_idf_run", "action": body.Action, "jobId": job.ID, "exitCode": job.ExitCode, "durationMs": job.DurationMs})
	writeJSON(w, http.StatusOK, map[string]any{
		"jobId":      job.ID,
		"action":     body.Action,
		"exitCode":   job.ExitCode,
		"timedOut":   job.TimedOut,
		"durationMs": job.DurationMs,
		"stdout":     job.Stdout,
		"stderr":     job.Stderr,
	})
}

func handleStrataProxy(d ServerDeps, rp *httputil.ReverseProxy, w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	bodyBytes, _ := io.ReadAll(io.LimitReader(r.Body, 16*1024*1024))
	if d.Arena != nil && r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/chat/completions") {
		if adv := d.Arena.PopAdvice(); adv != "" {
			if updated, ok := injectAdviceIntoChatBody(bodyBytes, adv); ok {
				bodyBytes = updated
				r.ContentLength = int64(len(bodyBytes))
				r.Header.Del("Content-Length")
				d.CtrlLog.Write(map[string]any{"event": "advice_injected", "len": len(adv), "text": adv})
			} else {
				// Restore if body wasn't a valid chat/completions payload
				d.Arena.QueueAdvice(adv)
			}
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	r.Header.Del("Authorization")
	r.Header.Del("X-Api-Key")
	// Request uncompressed local upstream response so statusRecorder can meter SSE chunks & usage
	r.Header.Set("Accept-Encoding", "identity")

	// Concurrency gate for Sync-Strata (queues safely instead of overloading single-request engine)
	if d.StrataQueue != nil {
		release, err := d.StrataQueue.Acquire(r.Context(), "pi", "")
		if err != nil {
			http.Error(w, "Client cancelled while waiting for Strata slot", http.StatusRequestTimeout)
			return
		}
		defer release()
	}

	sw := &statusRecorder{
		ResponseWriter: w,
		status:         200,
		started:        started,
		onProgress: func(tokPerSec float64, toks int, elapsedMs int64) {
			if d.speed != nil {
				d.speed.set(tokPerSec)
			}
			if d.Hub != nil {
				d.Hub.Publish("strata_speed", map[string]any{
					"tokPerSec":  tokPerSec,
					"tokens":     toks,
					"durationMs": elapsedMs,
				})
			}
		},
	}
	rp.ServeHTTP(sw, r)

	elapsedMs := time.Since(started).Milliseconds()
	if strings.HasPrefix(r.URL.Path, "/v1/") && sw.status < 400 && elapsedMs > 0 {
		if tokPerSec, toks := sw.finalSpeed(elapsedMs); tokPerSec > 0 {
			if d.speed != nil {
				d.speed.set(tokPerSec)
			}
			if d.Hub != nil {
				d.Hub.Publish("strata_speed", map[string]any{
					"tokPerSec":  tokPerSec,
					"tokens":     toks,
					"durationMs": elapsedMs,
				})
			}
		}
	}

	d.APILog.Write(map[string]any{
		"event":      "api_call",
		"remoteIp":   r.RemoteAddr,
		"method":     r.Method,
		"url":        r.URL.RequestURI(),
		"status":     sw.status,
		"durationMs": elapsedMs,
		"reqBytes":   len(bodyBytes),
		"resBytes":   sw.bytes,
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status       int
	bytes        int
	started      time.Time
	firstChunkAt time.Time
	lastEmitAt   time.Time
	sseChunks    int
	lastUsage    int
	tailBuf      []byte
	onProgress   func(tokPerSec float64, toks int, elapsedMs int64)
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	n, err := s.ResponseWriter.Write(p)
	s.bytes += n
	if n > 0 {
		chunks := bytes.Count(p[:n], []byte("data:"))
		if chunks > 0 {
			now := time.Now()
			if s.firstChunkAt.IsZero() {
				s.firstChunkAt = now
			}
			s.sseChunks += chunks
			if s.onProgress != nil && s.sseChunks >= 3 && now.Sub(s.lastEmitAt) >= 500*time.Millisecond {
				s.lastEmitAt = now
				genDur := now.Sub(s.firstChunkAt)
				if genDur <= 50*time.Millisecond {
					genDur = now.Sub(s.started)
				}
				if genDur > 0 {
					rate := float64(s.sseChunks) / genDur.Seconds()
					go s.onProgress(rate, s.sseChunks, genDur.Milliseconds())
				}
			}
		}
		if len(p[:n]) <= 4096 {
			s.tailBuf = append(s.tailBuf, p[:n]...)
			if len(s.tailBuf) > 8192 {
				s.tailBuf = s.tailBuf[len(s.tailBuf)-8192:]
			}
		} else {
			s.tailBuf = append([]byte(nil), p[n-4096:n]...)
		}
	}
	return n, err
}

func (s *statusRecorder) tokens() int {
	// Try to parse completion_tokens from trailing JSON / SSE chunk
	if v := extractJSONNumber(s.tailBuf, `"completion_tokens"`); v > 0 {
		return int(v)
	}
	return s.sseChunks
}

func (s *statusRecorder) finalSpeed(totalElapsedMs int64) (float64, int) {
	toks := s.tokens()
	if pps := extractJSONNumber(s.tailBuf, `"predicted_per_second"`); pps > 0 {
		return pps, toks
	}
	if toks <= 0 {
		return 0, 0
	}
	decodeMs := totalElapsedMs
	if !s.firstChunkAt.IsZero() {
		if d := time.Since(s.firstChunkAt).Milliseconds(); d > 50 {
			decodeMs = d
		}
	}
	return float64(toks) / (float64(decodeMs) / 1000.0), toks
}

func extractJSONNumber(buf []byte, key string) float64 {
	idx := bytes.LastIndex(buf, []byte(key))
	if idx < 0 {
		return 0
	}
	sub := buf[idx+len(key):]
	colon := bytes.IndexByte(sub, ':')
	if colon < 0 {
		return 0
	}
	var numStr strings.Builder
	for _, b := range sub[colon+1:] {
		if (b >= '0' && b <= '9') || b == '.' {
			numStr.WriteByte(b)
		} else if numStr.Len() > 0 {
			break
		}
	}
	v, err := strconv.ParseFloat(numStr.String(), 64)
	if err != nil {
		return 0
	}
	return v
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func checkAuth(cfg *config.Store, r *http.Request) bool {
	tok := cfg.Get().ProxyToken
	if tok == "" {
		return true
	}
	if r.Header.Get("Authorization") == "Bearer "+tok {
		return true
	}
	if r.Header.Get("X-Api-Key") == tok {
		return true
	}
	if strings.HasPrefix(r.URL.Path, "/admin/") && r.URL.Query().Get("token") == tok {
		return true
	}
	return false
}

func handleSSE(d ServerDeps, w http.ResponseWriter, r *http.Request) {
	if d.Hub == nil {
		http.Error(w, "SSE unavailable", http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, cancel := d.Hub.Subscribe()
	defer cancel()

	// Send initial connected event (with last known strataSpeed if available)
	var spd float64
	if d.speed != nil {
		spd = d.speed.get()
	}
	if spd > 0 {
		_, _ = fmt.Fprintf(w, "event: ready\ndata: {\"ok\":true,\"tokPerSec\":%.2f}\n\n", spd)
	} else {
		_, _ = w.Write([]byte("event: ready\ndata: {\"ok\":true}\n\n"))
	}
	flusher.Flush()

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		case ev, open := <-ch:
			if !open {
				return
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b)
			flusher.Flush()
		}
	}
}

func handleLogsRecent(d ServerDeps, w http.ResponseWriter, r *http.Request) {
	stream := r.URL.Query().Get("stream")
	if stream == "" {
		stream = "all"
	}
	tail := 80
	if t, err := strconv.Atoi(r.URL.Query().Get("tail")); err == nil && t > 0 && t <= 500 {
		tail = t
	}
	c := d.Cfg.Get()
	streams := []string{"api-audit", "egress", "control"}
	if stream != "all" {
		streams = []string{stream}
	}
	type logEntry struct {
		Stream string         `json:"stream"`
		Record map[string]any `json:"record"`
	}
	var items []logEntry
	for _, sName := range streams {
		recs, err := binlog.ReadTail(filepath.Join(c.LogDir, sName+".bin"), tail)
		if err != nil {
			continue
		}
		for _, rec := range recs {
			items = append(items, logEntry{Stream: sName, Record: rec})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func handlePiSessions(d ServerDeps, w http.ResponseWriter, _ *http.Request) {
	c := d.Cfg.Get()
	list, err := sessions.List(c.SessionsDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
}

func handlePiSessionRead(d ServerDeps, w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}
	offset := int64(-1)
	if v := r.URL.Query().Get("offset"); v != "" {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			offset = parsed
		}
	}
	before := int64(-1)
	if v := r.URL.Query().Get("before"); v != "" {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			before = parsed
		}
	}
	limit := 40
	if v := r.URL.Query().Get("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	c := d.Cfg.Get()
	res, err := sessions.Read(c.SessionsDir, id, offset, before, limit)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func handleFSTree(d ServerDeps, w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	hidden := r.URL.Query().Get("hidden") == "1"
	entries, err := fsview.ListDir(d.Cfg.Get().Workspace, dir, hidden)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dir": dir, "entries": entries})
}

func handleFSRaw(d ServerDeps, w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path is required"})
		return
	}
	if r.URL.Query().Get("download") == "1" {
		target, norm, err := fsview.ResolveSafe(d.Cfg.Get().Workspace, p)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(norm)))
		http.ServeFile(w, r, target)
		return
	}
	data, size, truncated, err := fsview.ReadFilePreview(d.Cfg.Get().Workspace, p)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":      p,
		"size":      size,
		"truncated": truncated,
		"content":   string(data),
	})
}

func handleLSPHover(d ServerDeps, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || d.LSP == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	var req struct {
		Path      string `json:"path"`
		Line      int    `json:"line"`
		Character int    `json:"character"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	res, err := d.LSP.Hover(r.Context(), d.Cfg.Get().Workspace, req.Path, req.Line, req.Character)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"found": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func handleLSPDefinition(d ServerDeps, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || d.LSP == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	var req struct {
		Path      string `json:"path"`
		Line      int    `json:"line"`
		Character int    `json:"character"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	locs, err := d.LSP.Definition(r.Context(), d.Cfg.Get().Workspace, req.Path, req.Line, req.Character)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"locations": []any{}, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"locations": locs})
}

func handleMCPServers(d ServerDeps, w http.ResponseWriter, r *http.Request) {
	if d.MCP == nil {
		writeJSON(w, http.StatusOK, map[string]any{"servers": []any{}})
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"servers": d.MCP.List()})
		return
	}
	if r.Method == http.MethodPost {
		var cfg mcp.ServerConfig
		if err := json.NewDecoder(io.LimitReader(r.Body, 256*1024)).Decode(&cfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := d.MCP.Upsert(cfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		d.CtrlLog.Write(map[string]any{"event": "mcp_upsert", "name": cfg.Name, "transport": cfg.Transport})
		writeJSON(w, http.StatusOK, map[string]any{"servers": d.MCP.List()})
		return
	}
	http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
}

func handleMCPServerAction(d ServerDeps, w http.ResponseWriter, r *http.Request) {
	if d.MCP == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "mcp unavailable"})
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/admin/mcp/servers/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "server name required"})
		return
	}
	name := parts[0]
	if len(parts) == 1 && r.Method == http.MethodDelete {
		if err := d.MCP.Delete(name); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		d.CtrlLog.Write(map[string]any{"event": "mcp_delete", "name": name})
		writeJSON(w, http.StatusOK, map[string]any{"servers": d.MCP.List()})
		return
	}
	if len(parts) == 2 && parts[1] == "toggle" && r.Method == http.MethodPost {
		var body struct {
			Enabled bool `json:"enabled"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
		if err := d.MCP.SetEnabled(name, body.Enabled); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"servers": d.MCP.List()})
		return
	}
	if len(parts) == 2 && parts[1] == "probe" && r.Method == http.MethodPost {
		tools, err := d.MCP.Probe(r.Context(), name)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "servers": d.MCP.List()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"tools": tools, "servers": d.MCP.List()})
		return
	}
	http.Error(w, "Not Found", http.StatusNotFound)
}

func handleMCPGateway(d ServerDeps, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || d.MCP == nil {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/mcp/"), "/")
	rawBody, err := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	started := time.Now()
	respRaw, err := d.MCP.ProxyJSONRPC(r.Context(), name, rawBody)
	elapsedMs := time.Since(started).Milliseconds()
	if err != nil {
		d.APILog.Write(map[string]any{"event": "mcp_error", "server": name, "error": err.Error(), "durationMs": elapsedMs})
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	d.APILog.Write(map[string]any{"event": "mcp_call", "server": name, "durationMs": elapsedMs, "bytes": len(respRaw)})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respRaw)
}

func handleSaaSPresets(d ServerDeps, w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label string `json:"label"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 16*1024)).Decode(&body)
	if r.Method == http.MethodPost {
		updated := d.Cfg.SavePreset(body.Label)
		d.CtrlLog.Write(map[string]any{"event": "saas_preset_saved", "label": updated.SaaSLabel})
		writeJSON(w, http.StatusOK, d.Cfg.Get())
		return
	}
	if r.Method == http.MethodDelete {
		lbl := body.Label
		if lbl == "" {
			lbl = r.URL.Query().Get("label")
		}
		d.Cfg.DeletePreset(lbl)
		d.CtrlLog.Write(map[string]any{"event": "saas_preset_deleted", "label": lbl})
		writeJSON(w, http.StatusOK, d.Cfg.Get())
		return
	}
	http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
}

func handleSaaSPresetActivate(d ServerDeps, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Label string `json:"label"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16*1024)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if _, ok := d.Cfg.ActivatePreset(body.Label); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "preset not found"})
		return
	}
	d.CtrlLog.Write(map[string]any{"event": "saas_preset_activated", "label": body.Label})
	writeJSON(w, http.StatusOK, d.Cfg.Get())
}

func handleArenaContext(d ServerDeps, w http.ResponseWriter, _ *http.Request) {
	c := d.Cfg.Get()
	files, _ := fsview.ListVisibleFiles(c.Workspace)
	initialPrompt := sessions.LatestInitialPrompt(c.SessionsDir)
	writeJSON(w, http.StatusOK, map[string]any{
		"initialPrompt": initialPrompt,
		"files":         files,
	})
}

func handleGHCreateRepo(d ServerDeps, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name        string `json:"name"`
		Visibility  string `json:"visibility"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c := d.Cfg.Get()
	out, err := github.CreateRepo(r.Context(), c.Workspace, c.GHExe, req.Name, req.Visibility, req.Description)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	d.CtrlLog.Write(map[string]any{"event": "gh_repo_created", "name": req.Name, "visibility": req.Visibility})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out, "status": github.Status(r.Context(), c.Workspace, c.GHExe)})
}

func handleGHPush(d ServerDeps, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	c := d.Cfg.Get()
	out, err := github.Push(r.Context(), c.Workspace)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	d.CtrlLog.Write(map[string]any{"event": "gh_pushed"})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out, "status": github.Status(r.Context(), c.Workspace, c.GHExe)})
}

func handleGHCreatePR(d ServerDeps, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Base  string `json:"base"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	c := d.Cfg.Get()
	out, err := github.CreatePR(r.Context(), c.Workspace, c.GHExe, req.Title, req.Body, req.Base)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	d.CtrlLog.Write(map[string]any{"event": "gh_pr_created", "title": req.Title})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func injectAdviceIntoChatBody(body []byte, advice string) ([]byte, bool) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false
	}
	msgs, ok := payload["messages"].([]any)
	if !ok {
		return nil, false
	}
	msgs = append(msgs, map[string]any{
		"role":    "user",
		"content": "[External AI Advice — consider this guidance in your next step]\n" + advice,
	})
	payload["messages"] = msgs
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, false
	}
	return out, true
}
