package config

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	Workspace          string            `json:"workspace"`
	LogDir             string            `json:"logDir"`
	SessionsDir        string            `json:"sessionsDir"`
	MaxLogBytes        int64             `json:"maxLogBytes"`
	MaxLogFiles        int               `json:"maxLogFiles"`
	AllowlistPath      string            `json:"allowlistPath"`
	AllowedHosts       []string          `json:"allowedHosts"`
	AutoHealEnabled    bool              `json:"autoHealEnabled"`
	HealthIntervalSec  int               `json:"healthIntervalSec"`
	MaxCommandWorkers  int               `json:"maxCommandWorkers"`
	GHExe              string            `json:"ghExe"`
	ESPProfile         string            `json:"espProfile"`
	AWSEmuPort         int               `json:"awsEmuPort"`
	AWSEmuTargetPort   int               `json:"awsEmuTargetPort"`
	AWSEmuCAPath       string            `json:"awsEmuCaPath"`
	AWSEmuContainer    string            `json:"awsEmuContainer"`
	SaaSConfigPath     string            `json:"-"`
	SaaSLabel          string            `json:"saasLabel"`
	SaaSAPIFormat      string            `json:"saasApiFormat"`
	SaaSAuthType       string            `json:"saasAuthType"`
	SaaSCustomHeader   string            `json:"saasCustomHeader"`
	SaaSExtraHeaders   map[string]string `json:"saasExtraHeaders"`
	SaaSBaseURL        string            `json:"saasBaseUrl"`
	SaaSModel          string            `json:"saasModel"`
	SaaSAPIKey         string            `json:"-"`
	SaaSKeySet         bool              `json:"saasKeySet"`
	StrataSystem       string            `json:"strataSystem"`
	SaaSSystem         string            `json:"saasSystem"`
	SaaSPresets        []SaaSPreset      `json:"saasPresets"`
}

type SaaSPreset struct {
	Label        string            `json:"label"`
	APIFormat    string            `json:"saasApiFormat"`
	AuthType     string            `json:"saasAuthType"`
	CustomHeader string            `json:"saasCustomHeader,omitempty"`
	ExtraHeaders map[string]string `json:"saasExtraHeaders,omitempty"`
	BaseURL      string            `json:"saasBaseUrl"`
	Model        string            `json:"saasModel"`
	APIKey       string            `json:"-"`
	KeySet       bool              `json:"saasKeySet"`
	StrataSystem string            `json:"strataSystem,omitempty"`
	SaaSSystem   string            `json:"saasSystem,omitempty"`
}

type PatchRequest struct {
	AllowedHosts      *[]string          `json:"allowedHosts"`
	StrataConcurrency *int               `json:"strataConcurrency"`
	AutoHealEnabled   *bool              `json:"autoHealEnabled"`
	HealthIntervalSec *int               `json:"healthIntervalSec"`
	SaaSLabel         *string            `json:"saasLabel"`
	SaaSAPIFormat     *string            `json:"saasApiFormat"`
	SaaSAuthType      *string            `json:"saasAuthType"`
	SaaSCustomHeader  *string            `json:"saasCustomHeader"`
	SaaSExtraHeaders  *map[string]string `json:"saasExtraHeaders"`
	SaaSBaseURL       *string            `json:"saasBaseUrl"`
	SaaSModel         *string            `json:"saasModel"`
	SaaSAPIKey        *string            `json:"saasApiKey"`
	StrataSystem      *string            `json:"strataSystem"`
	SaaSSystem        *string            `json:"saasSystem"`
}

type Store struct {
	mu           sync.RWMutex
	cfg          RuntimeConfig
	allowMtime   time.Time
}

func NewStore(initial RuntimeConfig) *Store {
	if initial.SaaSAPIFormat == "" {
		initial.SaaSAPIFormat = "openai"
	}
	if initial.SaaSAuthType == "" {
		initial.SaaSAuthType = "bearer"
	}
	s := &Store{cfg: initial}
	s.loadSaaSConfig()
	s.ReloadAllowlist()
	return s
}

func (s *Store) Get() RuntimeConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := s.cfg
	cp.AllowedHosts = append([]string(nil), s.cfg.AllowedHosts...)
	if s.cfg.SaaSExtraHeaders != nil {
		cp.SaaSExtraHeaders = make(map[string]string, len(s.cfg.SaaSExtraHeaders))
		for k, v := range s.cfg.SaaSExtraHeaders {
			cp.SaaSExtraHeaders[k] = v
		}
	}
	cp.SaaSKeySet = s.cfg.SaaSAPIKey != ""
	if len(s.cfg.SaaSPresets) > 0 {
		cp.SaaSPresets = make([]SaaSPreset, len(s.cfg.SaaSPresets))
		for i, pr := range s.cfg.SaaSPresets {
			pr.KeySet = pr.APIKey != ""
			cp.SaaSPresets[i] = pr
		}
	}
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
	if p.SaaSLabel != nil {
		s.cfg.SaaSLabel = strings.TrimSpace(*p.SaaSLabel)
	}
	if p.SaaSAPIFormat != nil {
		f := strings.ToLower(strings.TrimSpace(*p.SaaSAPIFormat))
		if f == "anthropic" || f == "openai" {
			s.cfg.SaaSAPIFormat = f
		}
	}
	if p.SaaSAuthType != nil {
		a := strings.ToLower(strings.TrimSpace(*p.SaaSAuthType))
		if a == "x-api-key" || a == "bearer" || a == "custom" || a == "none" {
			s.cfg.SaaSAuthType = a
		}
	}
	if p.SaaSCustomHeader != nil {
		s.cfg.SaaSCustomHeader = strings.TrimSpace(*p.SaaSCustomHeader)
	}
	if p.SaaSExtraHeaders != nil {
		cp := make(map[string]string, len(*p.SaaSExtraHeaders))
		for k, v := range *p.SaaSExtraHeaders {
			if tk := strings.TrimSpace(k); tk != "" {
				cp[tk] = v
			}
		}
		s.cfg.SaaSExtraHeaders = cp
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
	if p.StrataSystem != nil {
		s.cfg.StrataSystem = *p.StrataSystem
	}
	if p.SaaSSystem != nil {
		s.cfg.SaaSSystem = *p.SaaSSystem
	}
	s.cfg.SaaSKeySet = s.cfg.SaaSAPIKey != ""
	s.saveSaaSConfigLocked()
	return s.cfg
}

type persistedPreset struct {
	Label        string            `json:"label"`
	APIFormat    string            `json:"saasApiFormat,omitempty"`
	AuthType     string            `json:"saasAuthType,omitempty"`
	CustomHeader string            `json:"saasCustomHeader,omitempty"`
	ExtraHeaders map[string]string `json:"saasExtraHeaders,omitempty"`
	BaseURL      string            `json:"saasBaseUrl,omitempty"`
	Model        string            `json:"saasModel,omitempty"`
	APIKeyEnc    string            `json:"saasApiKeyEnc,omitempty"`
	StrataSystem string            `json:"strataSystem,omitempty"`
	SaaSSystem   string            `json:"saasSystem,omitempty"`
}

type persistedSaaS struct {
	SaaSLabel        string            `json:"saasLabel,omitempty"`
	SaaSAPIFormat    string            `json:"saasApiFormat,omitempty"`
	SaaSAuthType     string            `json:"saasAuthType,omitempty"`
	SaaSCustomHeader string            `json:"saasCustomHeader,omitempty"`
	SaaSExtraHeaders map[string]string `json:"saasExtraHeaders,omitempty"`
	SaaSBaseURL      string            `json:"saasBaseUrl,omitempty"`
	SaaSModel        string            `json:"saasModel,omitempty"`
	SaaSAPIKeyEnc    string            `json:"saasApiKeyEnc,omitempty"`
	StrataSystem     string            `json:"strataSystem,omitempty"`
	SaaSSystem       string            `json:"saasSystem,omitempty"`
	Presets          []persistedPreset `json:"presets,omitempty"`
}

func (s *Store) SavePreset(label string) RuntimeConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	lbl := strings.TrimSpace(label)
	if lbl == "" {
		lbl = s.cfg.SaaSLabel
	}
	if lbl == "" {
		lbl = s.cfg.SaaSModel
	}
	if lbl == "" {
		lbl = "default"
	}
	s.cfg.SaaSLabel = lbl
	pr := SaaSPreset{
		Label:        lbl,
		APIFormat:    s.cfg.SaaSAPIFormat,
		AuthType:     s.cfg.SaaSAuthType,
		CustomHeader: s.cfg.SaaSCustomHeader,
		ExtraHeaders: s.cfg.SaaSExtraHeaders,
		BaseURL:      s.cfg.SaaSBaseURL,
		Model:        s.cfg.SaaSModel,
		APIKey:       s.cfg.SaaSAPIKey,
		KeySet:       s.cfg.SaaSAPIKey != "",
		StrataSystem: s.cfg.StrataSystem,
		SaaSSystem:   s.cfg.SaaSSystem,
	}
	replaced := false
	for i := range s.cfg.SaaSPresets {
		if s.cfg.SaaSPresets[i].Label == lbl {
			if pr.APIKey == "" {
				pr.APIKey = s.cfg.SaaSPresets[i].APIKey
				pr.KeySet = pr.APIKey != ""
			}
			s.cfg.SaaSPresets[i] = pr
			replaced = true
			break
		}
	}
	if !replaced {
		s.cfg.SaaSPresets = append(s.cfg.SaaSPresets, pr)
	}
	s.saveSaaSConfigLocked()
	return s.cfg
}

func (s *Store) ActivatePreset(label string) (RuntimeConfig, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pr := range s.cfg.SaaSPresets {
		if pr.Label == label {
			s.cfg.SaaSLabel = pr.Label
			if pr.APIFormat != "" {
				s.cfg.SaaSAPIFormat = pr.APIFormat
			}
			if pr.AuthType != "" {
				s.cfg.SaaSAuthType = pr.AuthType
			}
			s.cfg.SaaSCustomHeader = pr.CustomHeader
			s.cfg.SaaSExtraHeaders = pr.ExtraHeaders
			s.cfg.SaaSBaseURL = pr.BaseURL
			s.cfg.SaaSModel = pr.Model
			if pr.APIKey != "" {
				s.cfg.SaaSAPIKey = pr.APIKey
			}
			s.cfg.SaaSKeySet = s.cfg.SaaSAPIKey != ""
			s.cfg.StrataSystem = pr.StrataSystem
			s.cfg.SaaSSystem = pr.SaaSSystem
			s.saveSaaSConfigLocked()
			return s.cfg, true
		}
	}
	return s.cfg, false
}

func (s *Store) DeletePreset(label string) RuntimeConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.cfg.SaaSPresets[:0]
	for _, pr := range s.cfg.SaaSPresets {
		if pr.Label != label {
			out = append(out, pr)
		}
	}
	s.cfg.SaaSPresets = out
	s.saveSaaSConfigLocked()
	return s.cfg
}

func (s *Store) loadSaaSConfig() {
	if s.cfg.SaaSConfigPath == "" {
		return
	}
	raw, err := os.ReadFile(s.cfg.SaaSConfigPath)
	if err != nil {
		return
	}
	var p persistedSaaS
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	if p.SaaSLabel != "" {
		s.cfg.SaaSLabel = p.SaaSLabel
	}
	if p.SaaSAPIFormat != "" {
		s.cfg.SaaSAPIFormat = p.SaaSAPIFormat
	}
	if p.SaaSAuthType != "" {
		s.cfg.SaaSAuthType = p.SaaSAuthType
	}
	if p.SaaSCustomHeader != "" {
		s.cfg.SaaSCustomHeader = p.SaaSCustomHeader
	}
	if p.SaaSExtraHeaders != nil {
		s.cfg.SaaSExtraHeaders = p.SaaSExtraHeaders
	}
	if s.cfg.SaaSBaseURL == "" && p.SaaSBaseURL != "" {
		s.cfg.SaaSBaseURL = p.SaaSBaseURL
	}
	if s.cfg.SaaSModel == "" && p.SaaSModel != "" {
		s.cfg.SaaSModel = p.SaaSModel
	}
	if s.cfg.SaaSAPIKey == "" && p.SaaSAPIKeyEnc != "" {
		if dec, err := unprotectSecret(p.SaaSAPIKeyEnc); err == nil {
			s.cfg.SaaSAPIKey = dec
		}
	}
	if p.StrataSystem != "" {
		s.cfg.StrataSystem = p.StrataSystem
	}
	if p.SaaSSystem != "" {
		s.cfg.SaaSSystem = p.SaaSSystem
	}
	for _, pp := range p.Presets {
		key := ""
		if pp.APIKeyEnc != "" {
			if dec, err := unprotectSecret(pp.APIKeyEnc); err == nil {
				key = dec
			}
		}
		s.cfg.SaaSPresets = append(s.cfg.SaaSPresets, SaaSPreset{
			Label:        pp.Label,
			APIFormat:    pp.APIFormat,
			AuthType:     pp.AuthType,
			CustomHeader: pp.CustomHeader,
			ExtraHeaders: pp.ExtraHeaders,
			BaseURL:      pp.BaseURL,
			Model:        pp.Model,
			APIKey:       key,
			KeySet:       key != "",
			StrataSystem: pp.StrataSystem,
			SaaSSystem:   pp.SaaSSystem,
		})
	}
	s.cfg.SaaSKeySet = s.cfg.SaaSAPIKey != ""
}

func (s *Store) saveSaaSConfigLocked() {
	if s.cfg.SaaSConfigPath == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.cfg.SaaSConfigPath), 0o755)
	encKey, _ := protectSecret(s.cfg.SaaSAPIKey)
	presets := make([]persistedPreset, 0, len(s.cfg.SaaSPresets))
	for _, pr := range s.cfg.SaaSPresets {
		pEnc, _ := protectSecret(pr.APIKey)
		presets = append(presets, persistedPreset{
			Label:        pr.Label,
			APIFormat:    pr.APIFormat,
			AuthType:     pr.AuthType,
			CustomHeader: pr.CustomHeader,
			ExtraHeaders: pr.ExtraHeaders,
			BaseURL:      pr.BaseURL,
			Model:        pr.Model,
			APIKeyEnc:    pEnc,
			StrataSystem: pr.StrataSystem,
			SaaSSystem:   pr.SaaSSystem,
		})
	}
	p := persistedSaaS{
		SaaSLabel:        s.cfg.SaaSLabel,
		SaaSAPIFormat:    s.cfg.SaaSAPIFormat,
		SaaSAuthType:     s.cfg.SaaSAuthType,
		SaaSCustomHeader: s.cfg.SaaSCustomHeader,
		SaaSExtraHeaders: s.cfg.SaaSExtraHeaders,
		SaaSBaseURL:      s.cfg.SaaSBaseURL,
		SaaSModel:        s.cfg.SaaSModel,
		SaaSAPIKeyEnc:    encKey,
		StrataSystem:     s.cfg.StrataSystem,
		SaaSSystem:       s.cfg.SaaSSystem,
		Presets:          presets,
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.cfg.SaaSConfigPath, raw, 0o600)
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
