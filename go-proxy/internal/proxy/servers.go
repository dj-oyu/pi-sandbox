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
	"time"

	"pi-proxy/internal/arena"
	"pi-proxy/internal/binlog"
	"pi-proxy/internal/config"
	"pi-proxy/internal/health"
	"pi-proxy/internal/jobs"
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

type ServerDeps struct {
	Cfg       *config.Store
	APILog    *binlog.Logger
	EgressLog *binlog.Logger
	CtrlLog   *binlog.Logger
	Runner    *jobs.Runner
	Monitor   *health.Monitor
	Arena     *arena.Service
	StrataSem chan struct{}
}

func NewAPIHandler(d ServerDeps) http.Handler {
	c := d.Cfg.Get()
	strataURL, _ := url.Parse(fmt.Sprintf("http://%s:%d", c.StrataHost, c.StrataPort))
	strataRP := httputil.NewSingleHostReverseProxy(strataURL)
	strataRP.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		d.APILog.Write(map[string]any{"event": "upstream_error", "url": r.URL.RequestURI(), "error": err.Error()})
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Bad Gateway", "detail": err.Error()})
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/ui" {
			d.Arena.HandleUI(w, r)
			return
		}
		if !checkAuth(d.Cfg, r) {
			d.APILog.Write(map[string]any{"event": "auth_denied", "remoteIp": r.RemoteAddr, "method": r.Method, "url": r.URL.RequestURI()})
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
			return
		}

		switch {
		case r.URL.Path == "/admin/config":
			handleConfig(d, w, r)
		case r.URL.Path == "/admin/health":
			writeJSON(w, http.StatusOK, map[string]any{"targets": d.Monitor.Snapshot()})
		case r.URL.Path == "/admin/jobs":
			writeJSON(w, http.StatusOK, map[string]any{"jobs": d.Runner.List()})
		case strings.HasPrefix(r.URL.Path, "/admin/jobs/"):
			id := strings.TrimPrefix(r.URL.Path, "/admin/jobs/")
			if j, ok := d.Runner.Get(id); ok {
				writeJSON(w, http.StatusOK, j)
			} else {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
			}
		case r.URL.Path == "/admin/logs/export":
			handleLogExport(d, w, r)
		case r.URL.Path == "/admin/strata/chat":
			d.Arena.HandleStrataChat(w, r)
		case r.URL.Path == "/admin/arena/run":
			d.Arena.HandleArenaRun(w, r)
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
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	r.Header.Del("Authorization")
	r.Header.Del("X-Api-Key")

	// Concurrency gate for Sync-Strata (queues safely instead of overloading single-request engine)
	select {
	case d.StrataSem <- struct{}{}:
		defer func() { <-d.StrataSem }()
	case <-r.Context().Done():
		http.Error(w, "Client cancelled while waiting for Strata slot", http.StatusRequestTimeout)
		return
	}

	sw := &statusRecorder{ResponseWriter: w, status: 200}
	rp.ServeHTTP(sw, r)

	d.APILog.Write(map[string]any{
		"event":      "api_call",
		"remoteIp":   r.RemoteAddr,
		"method":     r.Method,
		"url":        r.URL.RequestURI(),
		"status":     sw.status,
		"durationMs": time.Since(started).Milliseconds(),
		"reqBytes":   len(bodyBytes),
		"resBytes":   sw.bytes,
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	n, err := s.ResponseWriter.Write(p)
	s.bytes += n
	return n, err
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
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
