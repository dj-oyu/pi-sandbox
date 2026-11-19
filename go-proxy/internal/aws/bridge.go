package aws

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"pi-proxy/internal/binlog"
	"pi-proxy/internal/config"
)

var (
	reCreate = regexp.MustCompile(`^/2015-03-31/functions/?(\?.*)?$`)
	reCode   = regexp.MustCompile(`^/2015-03-31/functions/([A-Za-z0-9_.:-]+)/code/?(\?.*)?$`)
	reItem   = regexp.MustCompile(`^/2015-03-31/functions/([A-Za-z0-9_.:-]+)/?(\?.*)?$`)
	reInvoke = regexp.MustCompile(`^/2015-03-31/functions/([A-Za-z0-9_.:-]+)/invocations(\?.*)?$`)
	reEnvKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type LambdaEntry struct {
	Dir     string            `json:"dir"`
	Handler string            `json:"handler"`
	Runtime string            `json:"runtime"`
	Env     map[string]string `json:"env"`
}

type Bridge struct {
	cfg        *config.Store
	log        *binlog.Logger
	codeRoot   string
	mu         sync.RWMutex
	registry   map[string]LambdaEntry
	emuProxy   *httputil.ReverseProxy
}

func NewBridge(cfg *config.Store, log *binlog.Logger, codeRoot string) *Bridge {
	c := cfg.Get()
	targetURL, _ := url.Parse(fmt.Sprintf("https://127.0.0.1:%d", c.AWSEmuTargetPort))
	rp := httputil.NewSingleHostReverseProxy(targetURL)

	if c.AWSEmuCAPath != "" {
		if pem, err := os.ReadFile(c.AWSEmuCAPath); err == nil {
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(pem)
			rp.Transport = &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: pool},
			}
		}
	}
	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":  "aws-emulator upstream error",
			"detail": err.Error(),
		})
	}

	return &Bridge{
		cfg:      cfg,
		log:      log,
		codeRoot: codeRoot,
		registry: make(map[string]LambdaEntry),
		emuProxy: rp,
	}
}

func (b *Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reqPath := r.URL.RequestURI()

	if r.Method == http.MethodPost && reCreate.MatchString(reqPath) {
		b.handleCreate(w, r)
		return
	}
	if r.Method == http.MethodPut {
		if m := reCode.FindStringSubmatch(reqPath); m != nil {
			b.handleUpdateCode(w, r, m[1])
			return
		}
	}
	if r.Method == http.MethodGet || r.Method == http.MethodDelete {
		if m := reItem.FindStringSubmatch(reqPath); m != nil {
			b.mu.RLock()
			entry, ok := b.registry[m[1]]
			b.mu.RUnlock()
			if ok {
				if r.Method == http.MethodDelete {
					b.mu.Lock()
					delete(b.registry, m[1])
					b.mu.Unlock()
					w.WriteHeader(http.StatusNoContent)
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"Configuration": map[string]any{
						"FunctionName": m[1],
						"FunctionArn":  "arn:aws:lambda:us-east-1:000000000000:function:" + m[1],
						"Runtime":      entry.Runtime,
						"Handler":      entry.Handler,
						"State":        "Active",
					},
				})
				return
			}
		}
	}
	if r.Method == http.MethodPost {
		if m := reInvoke.FindStringSubmatch(reqPath); m != nil {
			b.handleInvoke(w, r, m[1])
			return
		}
	}

	b.emuProxy.ServeHTTP(w, r)
}

func (b *Bridge) handleCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FunctionName string `json:"FunctionName"`
		Handler      string `json:"Handler"`
		Runtime      string `json:"Runtime"`
		Code         struct {
			ZipFile string `json:"ZipFile"`
		} `json:"Code"`
		Environment struct {
			Variables map[string]string `json:"Variables"`
		} `json:"Environment"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 32*1024*1024)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
		return
	}
	if body.FunctionName == "" || body.Code.ZipFile == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "FunctionName and Code.ZipFile are required"})
		return
	}
	dir, err := b.extractBase64Zip(body.FunctionName, body.Code.ZipFile)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
		return
	}
	if body.Handler == "" {
		body.Handler = "index.handler"
	}
	if body.Runtime == "" {
		body.Runtime = "nodejs22.x"
	}
	entry := LambdaEntry{
		Dir:     dir,
		Handler: body.Handler,
		Runtime: body.Runtime,
		Env:     body.Environment.Variables,
	}
	b.mu.Lock()
	b.registry[body.FunctionName] = entry
	b.mu.Unlock()

	b.log.Write(map[string]any{"event": "lambda_create", "funcName": body.FunctionName, "handler": entry.Handler})
	writeJSON(w, http.StatusCreated, map[string]any{
		"FunctionName":     body.FunctionName,
		"FunctionArn":      "arn:aws:lambda:us-east-1:000000000000:function:" + body.FunctionName,
		"Runtime":          entry.Runtime,
		"Handler":          entry.Handler,
		"State":            "Active",
		"LastUpdateStatus": "Successful",
	})
}

func (b *Bridge) handleUpdateCode(w http.ResponseWriter, r *http.Request, funcName string) {
	var body struct {
		ZipFile string `json:"ZipFile"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 32*1024*1024)).Decode(&body); err != nil || body.ZipFile == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "ZipFile (base64) is required"})
		return
	}
	dir, err := b.extractBase64Zip(funcName, body.ZipFile)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
		return
	}
	b.mu.Lock()
	entry, ok := b.registry[funcName]
	if !ok {
		entry = LambdaEntry{Handler: "index.handler", Runtime: "nodejs22.x"}
	}
	entry.Dir = dir
	b.registry[funcName] = entry
	b.mu.Unlock()

	b.log.Write(map[string]any{"event": "lambda_update_code", "funcName": funcName})
	writeJSON(w, http.StatusOK, map[string]any{
		"FunctionName":     funcName,
		"FunctionArn":      "arn:aws:lambda:us-east-1:000000000000:function:" + funcName,
		"Runtime":          entry.Runtime,
		"Handler":          entry.Handler,
		"State":            "Active",
		"LastUpdateStatus": "Successful",
	})
}

func (b *Bridge) handleInvoke(w http.ResponseWriter, r *http.Request, funcName string) {
	started := time.Now()
	invType := strings.ToLower(r.Header.Get("x-amz-invocation-type"))
	if invType == "" {
		invType = "requestresponse"
	}
	if invType == "dryrun" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	target, ok := b.resolveTarget(funcName)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Function not found: " + funcName})
		return
	}
	eventBytes, err := io.ReadAll(io.LimitReader(r.Body, 8*1024*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
		return
	}

	port, err := freePort()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"message": err.Error()})
		return
	}

	c := b.cfg.Get()
	hostOnly := strings.Split(r.Host, ":")[0]
	if hostOnly == "" {
		hostOnly = "127.0.0.1"
	}
	emuURL := fmt.Sprintf("http://%s:%d", hostOnly, c.AWSEmuPort)
	containerName := fmt.Sprintf("pi-lambda-%d", time.Now().UnixNano())

	args := []string{
		"run", "--rm", "-d",
		"--name", containerName,
		"--cap-drop=ALL",
		"-m", "256M",
		"--cpus", "1",
		"--tmpfs", "/tmp:rw,size=128m",
		"-p", fmt.Sprintf("127.0.0.1:%d:8080", port),
		"-v", fmt.Sprintf("%s:/var/task:ro", target.Dir),
		"-e", "AWS_ENDPOINT_URL=" + emuURL,
		"-e", "AWS_ACCESS_KEY_ID=test",
		"-e", "AWS_SECRET_ACCESS_KEY=test",
		"-e", "AWS_DEFAULT_REGION=us-east-1",
	}
	for k, v := range target.Env {
		if reEnvKey.MatchString(k) {
			args = append(args, "-e", k+"="+v)
		}
	}
	args = append(args, "pi-lambda-rie:latest", target.Handler)

	if out, err := exec.Command("wslc", args...).CombinedOutput(); err != nil {
		b.log.Write(map[string]any{"event": "lambda_invoke_error", "funcName": funcName, "error": err.Error()})
		writeJSON(w, http.StatusBadGateway, map[string]string{"message": fmt.Sprintf("%v: %s", err, string(out))})
		return
	}
	defer func() {
		_ = exec.Command("wslc", "container", "stop", containerName).Run()
	}()

	if err := waitPort(r.Context(), port, 6*time.Second); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"message": err.Error()})
		return
	}

	rieURL := fmt.Sprintf("http://127.0.0.1:%d/2015-03-31/functions/function/invocations", port)
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, rieURL, bytes.NewReader(eventBytes))
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"message": err.Error()})
		return
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	fnErr := resp.Header.Get("x-amz-function-error")
	if fnErr == "" && invType != "event" && len(respBytes) > 0 {
		var probe map[string]any
		if json.Unmarshal(respBytes, &probe) == nil {
			if _, hasType := probe["errorType"].(string); hasType {
				if _, hasMsg := probe["errorMessage"].(string); hasMsg {
					fnErr = "Unhandled"
				}
			}
		}
	}

	status := resp.StatusCode
	if invType == "event" {
		status = http.StatusAccepted
		respBytes = nil
	}
	b.log.Write(map[string]any{
		"event":          "lambda_invoke",
		"funcName":       funcName,
		"invocationType": invType,
		"status":         status,
		"functionError":  fnErr,
		"durationMs":     time.Since(started).Milliseconds(),
	})

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	if fnErr != "" {
		w.Header().Set("x-amz-function-error", fnErr)
	}
	w.WriteHeader(status)
	_, _ = w.Write(respBytes)
}

func (b *Bridge) resolveTarget(funcName string) (LambdaEntry, bool) {
	if strings.Contains(funcName, "..") {
		return LambdaEntry{}, false
	}
	b.mu.RLock()
	entry, ok := b.registry[funcName]
	b.mu.RUnlock()
	if ok {
		return entry, true
	}

	ws := b.cfg.Get().Workspace
	if ws == "" {
		return LambdaEntry{}, false
	}
	baseWs := filepath.Clean(ws)
	routesPath := filepath.Join(baseWs, ".pi", "lambda-routes.json")
	if raw, err := os.ReadFile(routesPath); err == nil {
		var routes map[string]struct {
			Dir     string `json:"dir"`
			Handler string `json:"handler"`
		}
		if json.Unmarshal(raw, &routes) == nil {
			if r, exists := routes[funcName]; exists && r.Dir != "" {
				resolved := filepath.Clean(filepath.Join(baseWs, r.Dir))
				if strings.HasPrefix(resolved, baseWs) {
					if _, err := os.Stat(resolved); err == nil {
						h := r.Handler
						if h == "" {
							h = "index.handler"
						}
						return LambdaEntry{Dir: resolved, Handler: h, Runtime: "nodejs22.x"}, true
					}
				}
			}
		}
	}

	convDir := filepath.Clean(filepath.Join(baseWs, "functions", funcName))
	if strings.HasPrefix(convDir, baseWs) {
		if _, err := os.Stat(convDir); err == nil {
			return LambdaEntry{Dir: convDir, Handler: "index.handler", Runtime: "nodejs22.x"}, true
		}
	}
	return LambdaEntry{}, false
}

func (b *Bridge) extractBase64Zip(funcName, b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", err
	}
	safe := regexp.MustCompile(`[^A-Za-z0-9_.-]`).ReplaceAllString(funcName, "_")
	destDir := filepath.Join(b.codeRoot, safe)
	_ = os.RemoveAll(destDir)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}

	cleanDest := filepath.Clean(destDir) + string(os.PathSeparator)
	for _, f := range zr.File {
		target := filepath.Clean(filepath.Join(destDir, f.Name))
		if !strings.HasPrefix(target, cleanDest) && target != filepath.Clean(destDir) {
			return "", fmt.Errorf("zip slip rejected: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(target, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return "", err
		}
		_, err = io.Copy(out, io.LimitReader(rc, 64*1024*1024))
		out.Close()
		rc.Close()
		if err != nil {
			return "", err
		}
	}
	return destDir, nil
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func waitPort(ctx context.Context, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for {
		c, err := net.DialTimeout("tcp", addr, 150*time.Millisecond)
		if err == nil {
			c.Close()
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("lambda RIE port %d timeout", port)
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
