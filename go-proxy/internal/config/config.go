package config

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

type RuntimeConfig struct {
	BindHost           string   `json:"bindHost"`
	APIPort            int      `json:"apiPort"`
	EgressPort         int      `json:"egressPort"`
	StrataHost         string   `json:"strataHost"`
	StrataPort         int      `json:"strataPort"`
	StrataConcurrency  int      `json:"strataConcurrency"`
	ProxyToken         string   `json:"-"`
	Workspace          string   `json:"workspace"`
	LogDir             string   `json:"logDir"`
	MaxLogBytes        int64    `json:"maxLogBytes"`
	MaxLogFiles        int      `json:"maxLogFiles"`
	AllowlistPath      string   `json:"allowlistPath"`
	AllowedHosts       []string `json:"allowedHosts"`
	AutoHealEnabled    bool     `json:"autoHealEnabled"`
	HealthIntervalSec  int      `json:"healthIntervalSec"`
	MaxCommandWorkers  int      `json:"maxCommandWorkers"`
	GHExe              string   `json:"ghExe"`
	ESPProfile         string   `json:"espProfile"`
	AWSEmuPort         int      `json:"awsEmuPort"`
	AWSEmuTargetPort   int      `json:"awsEmuTargetPort"`
	AWSEmuCAPath       string   `json:"awsEmuCaPath"`
	AWSEmuContainer    string   `json:"awsEmuContainer"`
	SaaSBaseURL        string   `json:"saasBaseUrl"`
	SaaSModel          string   `json:"saasModel"`
	SaaSAPIKey         string   `json:"-"`
}

type PatchRequest struct {
	AllowedHosts      *[]string `json:"allowedHosts"`
	StrataConcurrency *int      `json:"strataConcurrency"`
	AutoHealEnabled   *bool     `json:"autoHealEnabled"`
	HealthIntervalSec *int      `json:"healthIntervalSec"`
	SaaSBaseURL       *string   `json:"saasBaseUrl"`
	SaaSModel         *string   `json:"saasModel"`
	SaaSAPIKey        *string   `json:"saasApiKey"`
}

type Store struct {
	mu           sync.RWMutex
	cfg          RuntimeConfig
	allowMtime   time.Time
}

func NewStore(initial RuntimeConfig) *Store {
	s := &Store{cfg: initial}
	s.ReloadAllowlist()
	return s
}

func (s *Store) Get() RuntimeConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := s.cfg
	cp.AllowedHosts = append([]string(nil), s.cfg.AllowedHosts...)
	return cp
}

func (s *Store) Patch(p PatchRequest) RuntimeConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.AllowedHosts != nil {
		norm := make([]string, 0, len(*p.AllowedHosts))
		for _, h := range *p.AllowedHosts {
			if t := strings.ToLower(strings.TrimSpace(h)); t != "" {
				norm = append(norm, t)
			}
		}
		s.cfg.AllowedHosts = norm
	}
	if p.StrataConcurrency != nil && *p.StrataConcurrency >= 1 && *p.StrataConcurrency <= 16 {
		s.cfg.StrataConcurrency = *p.StrataConcurrency
	}
	if p.AutoHealEnabled != nil {
		s.cfg.AutoHealEnabled = *p.AutoHealEnabled
	}
	if p.HealthIntervalSec != nil && *p.HealthIntervalSec >= 2 && *p.HealthIntervalSec <= 300 {
		s.cfg.HealthIntervalSec = *p.HealthIntervalSec
	}
	if p.SaaSBaseURL != nil {
		s.cfg.SaaSBaseURL = strings.TrimRight(strings.TrimSpace(*p.SaaSBaseURL), "/")
	}
	if p.SaaSModel != nil {
		s.cfg.SaaSModel = strings.TrimSpace(*p.SaaSModel)
	}
	if p.SaaSAPIKey != nil {
		s.cfg.SaaSAPIKey = strings.TrimSpace(*p.SaaSAPIKey)
	}
	return s.cfg
}

func (s *Store) ReloadAllowlist() {
	s.mu.RLock()
	path := s.cfg.AllowlistPath
	prevMtime := s.allowMtime
	s.mu.RUnlock()
	if path == "" {
		return
	}
	st, err := os.Stat(path)
	if err != nil || !st.ModTime().After(prevMtime) {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var parsed struct {
		AllowedHosts []string `json:"allowedHosts"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return
	}
	norm := make([]string, 0, len(parsed.AllowedHosts))
	for _, h := range parsed.AllowedHosts {
		if t := strings.ToLower(strings.TrimSpace(h)); t != "" {
			norm = append(norm, t)
		}
	}
	s.mu.Lock()
	s.cfg.AllowedHosts = norm
	s.allowMtime = st.ModTime()
	s.mu.Unlock()
}

func (s *Store) IsHostAllowed(hostname string) bool {
	s.ReloadAllowlist()
	host := strings.ToLower(strings.Trim(hostname, "[]"))
	if host == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, pat := range s.cfg.AllowedHosts {
		if strings.HasPrefix(pat, "*.") {
			suffix := pat[1:]
			if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
				return true
			}
		} else if host == pat {
			return true
		}
	}
	return false
}
