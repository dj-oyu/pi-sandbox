package health

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"pi-proxy/internal/binlog"
	"pi-proxy/internal/config"
)

type Target struct {
	Name          string   `json:"name"`
	ContainerName string   `json:"containerName"`
	HealthURL     string   `json:"healthUrl"`
	CAPath        string   `json:"-"`
	RestartArgs   []string `json:"-"`
	Healthy       bool     `json:"healthy"`
	Failures      int      `json:"consecutiveFailures"`
	HealCount     int      `json:"healCount"`
	LastCheck     string   `json:"lastCheck"`
	LastError     string   `json:"lastError,omitempty"`
}

type Monitor struct {
	mu       sync.RWMutex
	targets  map[string]*Target
	cfg      *config.Store
	log      *binlog.Logger
	onChange func([]Target)
}

func (m *Monitor) SetOnChange(fn func([]Target)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onChange = fn
}

func (m *Monitor) notify() {
	m.mu.RLock()
	cb := m.onChange
	m.mu.RUnlock()
	if cb != nil {
		cb(m.Snapshot())
	}
}

func NewMonitor(cfg *config.Store, log *binlog.Logger) *Monitor {
	return &Monitor{
		targets: make(map[string]*Target),
		cfg:     cfg,
		log:     log,
	}
}

func (m *Monitor) Register(t Target) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := t
	cp.Healthy = true
	m.targets[t.Name] = &cp
}

func (m *Monitor) Start(ctx context.Context) {
	go func() {
		for {
			interval := time.Duration(m.cfg.Get().HealthIntervalSec) * time.Second
			if interval < 2*time.Second {
				interval = 10 * time.Second
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
				m.checkAll()
			}
		}
	}()
}

func (m *Monitor) checkAll() {
	m.mu.RLock()
	names := make([]string, 0, len(m.targets))
	for n := range m.targets {
		names = append(names, n)
	}
	m.mu.RUnlock()

	for _, name := range names {
		m.checkOne(name)
	}
}

func (m *Monitor) checkOne(name string) {
	m.mu.RLock()
	t, ok := m.targets[name]
	if !ok {
		m.mu.RUnlock()
		return
	}
	healthURL := t.HealthURL
	caPath := t.CAPath
	container := t.ContainerName
	restartArgs := append([]string(nil), t.RestartArgs...)
	m.mu.RUnlock()

	err := probeHealth(healthURL, caPath)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	m.mu.Lock()
	t, ok = m.targets[name]
	if !ok {
		m.mu.Unlock()
		return
	}
	t.LastCheck = now
	if err == nil {
		t.Healthy = true
		t.Failures = 0
		t.LastError = ""
		m.mu.Unlock()
		m.notify()
		return
	}
	t.Healthy = false
	t.Failures++
	t.LastError = err.Error()
	shouldHeal := m.cfg.Get().AutoHealEnabled && t.Failures >= 3 && len(restartArgs) > 0
	m.mu.Unlock()
	m.notify()

	m.log.Write(map[string]any{
		"event":     "container_unhealthy",
		"target":    name,
		"container": container,
		"error":     err.Error(),
	})

	if shouldHeal {
		m.heal(name, container, restartArgs)
	}
}

func (m *Monitor) heal(name, container string, restartArgs []string) {
	if container != "" {
		_ = exec.Command("wslc", "container", "stop", container).Run()
		_ = exec.Command("wslc", "container", "rm", "-f", container).Run()
	}
	err := exec.Command("wslc", restartArgs...).Run()

	m.mu.Lock()
	if t, ok := m.targets[name]; ok {
		t.HealCount++
		t.Failures = 0
		if err == nil {
			t.LastError = ""
		} else {
			t.LastError = "heal failed: " + err.Error()
		}
	}
	m.mu.Unlock()

	m.log.Write(map[string]any{
		"event":     "container_auto_heal",
		"target":    name,
		"container": container,
		"ok":        err == nil,
	})
}

func (m *Monitor) Snapshot() []Target {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Target, 0, len(m.targets))
	for _, t := range m.targets {
		out = append(out, *t)
	}
	return out
}

func probeHealth(healthURL, caPath string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	if caPath != "" {
		if pem, err := os.ReadFile(caPath); err == nil {
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(pem)
			client.Transport = &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: pool},
			}
		}
	}
	resp, err := client.Get(healthURL)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return http.ErrNotSupported
	}
	return nil
}
