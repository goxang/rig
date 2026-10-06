// Package ai puts an assistant inside rig without being one: every turn runs the user's own
// opencode or Claude Code, with rig's MCP server as its tools, the project directory as its only
// workspace, and rig's guard rails between it and the environments.
package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	BackendOpencode = "opencode"
	BackendClaude   = "claude"
)

// Providers an API key can be set for; "own" leaves the backend on its own login and models.
var Providers = map[string]string{
	"own":       "the backend's own setup (your opencode config or Claude Code login)",
	"opencode":  "opencode's free models (opencode/big-pickle and the other -free ones)",
	"openai":    "any OpenAI-compatible endpoint: url, api_key, model",
	"9router":   "a 9router instance (OpenAI-compatible, default http://localhost:20128/v1)",
	"deepseek":  "DeepSeek's API: api_key (model deepseek-chat unless set)",
	"anthropic": "an Anthropic-compatible endpoint for Claude Code: url, api_key",
}

const DefaultFreeModel = "opencode/big-pickle"

// Config is the user's AI setup, kept in ~/.config/rig/ai.json (it may hold an API key: mode 0600).
type Config struct {
	Backend   string `json:"backend,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	FastModel string `json:"fast_model,omitempty"`
	// Effort is the reasoning level of chat turns: Claude Code's --effort, opencode's --variant.
	Effort string `json:"effort,omitempty"`
	// FastURL and FastAPIKey point completions at an OpenAI-compatible endpoint of their own,
	// one direct request instead of a backend run, whatever the chat goes through.
	FastURL    string `json:"fast_url,omitempty"`
	FastAPIKey string `json:"fast_api_key,omitempty"`
	URL        string `json:"url,omitempty"`
	APIKey     string `json:"api_key,omitempty"`
	Proxy      string `json:"proxy,omitempty"`
	// Autocomplete suggests the rest of a query while it is typed; off by default only when set false.
	Autocomplete *bool `json:"autocomplete,omitempty"`
	Disabled     bool  `json:"disabled,omitempty"`
	// Redact replaces secrets with <secret:NAME> in everything sent to the model; on unless set false.
	Redact *bool `json:"redact,omitempty"`
}

// Keys are the settings `rig ai config key=value` takes, in the order it prints them.
var Keys = []string{"backend", "provider", "model", "effort", "fast_model", "fast_url", "fast_api_key", "url", "api_key", "proxy", "autocomplete", "disabled", "redact"}

func ConfigFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "rig", "ai.json"), nil
}

// LoadConfig reads the saved setup; RIG_AI_* variables override it for one run.
func LoadConfig() (Config, error) {
	c, err := readConfig()
	if err != nil {
		return c, err
	}
	for k, p := range map[string]*string{"BACKEND": &c.Backend, "PROVIDER": &c.Provider, "MODEL": &c.Model, "EFFORT": &c.Effort,
		"FAST_MODEL": &c.FastModel, "FAST_URL": &c.FastURL, "FAST_API_KEY": &c.FastAPIKey, "URL": &c.URL, "API_KEY": &c.APIKey, "PROXY": &c.Proxy} {
		if v := os.Getenv("RIG_AI_" + k); v != "" {
			*p = v
		}
	}
	return c, nil
}

func readConfig() (Config, error) {
	var c Config
	f, err := ConfigFile()
	if err != nil {
		return c, err
	}
	raw, err := os.ReadFile(f)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return c, err
	default:
		if err := json.Unmarshal(raw, &c); err != nil {
			return c, fmt.Errorf("%s: %w", f, err)
		}
	}
	return c, nil
}

// ChangeConfig sets one key in the saved setup, leaving RIG_AI_* overrides out of the file.
func ChangeConfig(key, value string) error {
	c, err := readConfig()
	if err != nil {
		return err
	}
	if err := c.Set(key, value); err != nil {
		return err
	}
	return SaveConfig(c)
}

func SaveConfig(c Config) error {
	f, err := ConfigFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(f, append(raw, '\n'), 0o600)
}

// Set changes one setting by its key; an empty value clears it.
func (c *Config) Set(key, value string) error {
	value = strings.TrimSpace(value)
	switch key {
	case "backend":
		if value != "" && value != BackendOpencode && value != BackendClaude {
			return fmt.Errorf("backend is %s or %s", BackendOpencode, BackendClaude)
		}
		c.Backend = value
	case "provider":
		if _, ok := Providers[value]; value != "" && !ok {
			return fmt.Errorf("provider is one of %s", strings.Join(providerNames(), ", "))
		}
		c.Provider = value
	case "model":
		c.Model = value
	case "fast_model":
		c.FastModel = value
	case "effort":
		c.Effort = value
	case "fast_url":
		c.FastURL = value
	case "fast_api_key":
		c.FastAPIKey = value
	case "url":
		c.URL = value
	case "api_key":
		c.APIKey = value
	case "proxy":
		c.Proxy = value
	case "autocomplete":
		if value == "" {
			c.Autocomplete = nil
			return nil
		}
		b := value == "true" || value == "on" || value == "1"
		c.Autocomplete = &b
	case "disabled":
		c.Disabled = value == "true" || value == "on" || value == "1"
	case "redact":
		if value == "" {
			c.Redact = nil
			return nil
		}
		b := value == "true" || value == "on" || value == "1"
		c.Redact = &b
	default:
		return fmt.Errorf("unknown setting %q (have %s)", key, strings.Join(Keys, ", "))
	}
	return nil
}

// Get is a setting's value for display, the API key masked.
func (c Config) Get(key string) string {
	switch key {
	case "backend":
		return c.Backend
	case "provider":
		return c.Provider
	case "model":
		return c.Model
	case "fast_model":
		return c.FastModel
	case "effort":
		return c.Effort
	case "url":
		return c.URL
	case "fast_url":
		return c.FastURL
	case "api_key":
		return mask(c.APIKey)
	case "fast_api_key":
		return mask(c.FastAPIKey)
	case "proxy":
		return c.Proxy
	case "autocomplete":
		if c.Autocomplete != nil {
			return fmt.Sprint(*c.Autocomplete)
		}
	case "disabled":
		if c.Disabled {
			return "true"
		}
	case "redact":
		return fmt.Sprint(c.RedactOn())
	}
	return ""
}

// RedactOn is whether secrets are redacted before text reaches the model (the default).
func (c Config) RedactOn() bool { return c.Redact == nil || *c.Redact }

func mask(key string) string {
	if len(key) > 8 {
		return key[:4] + "…" + key[len(key)-4:]
	}
	if key != "" {
		return "set"
	}
	return ""
}

func providerNames() []string {
	var out []string
	for p := range Providers {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Setup is a Config resolved against this machine: which binary runs, with which model and endpoint.
type Setup struct {
	Config
	Bin string
	// Why says why AI is off, when Bin is empty.
	Why string
}

func (s Setup) Enabled() bool { return s.Bin != "" }

// Efforts are the levels Effort takes on this backend.
func (s Setup) Efforts() []string {
	if s.Backend == BackendClaude {
		return []string{"low", "medium", "high", "xhigh", "max"}
	}
	return []string{"minimal", "low", "medium", "high", "max"}
}

func (s Setup) AutocompleteOn() bool {
	return s.Enabled() && (s.Autocomplete == nil || *s.Autocomplete)
}

// Resolve picks the backend (the configured one, else opencode, else Claude Code) and fills the
// provider's defaults. A provider with its own key needs opencode, except anthropic, which is Claude Code's.
func Resolve(c Config) Setup {
	s := Setup{Config: c}
	if c.Disabled {
		s.Why = "AI is disabled (rig ai config disabled=false)"
		return s
	}
	switch c.Provider {
	case "openai", "9router", "deepseek":
		if s.Backend == "" {
			s.Backend = BackendOpencode
		}
		if s.Backend != BackendOpencode {
			s.Why = "provider " + c.Provider + " runs through opencode (backend=opencode)"
			return s
		}
	case "anthropic":
		if s.Backend == "" {
			s.Backend = BackendClaude
		}
	}
	if s.Backend == "" {
		for _, b := range []string{BackendOpencode, BackendClaude} {
			if find(b) != "" {
				s.Backend = b
				break
			}
		}
	}
	if s.Backend == "" {
		s.Why = "neither opencode nor claude is installed: install one (https://opencode.ai, https://claude.com/claude-code)"
		return s
	}
	if s.Bin = find(s.Backend); s.Bin == "" {
		s.Why = s.Backend + " is not installed"
		return s
	}
	if s.Provider == "" {
		s.Provider = "own"
		if s.Backend == BackendOpencode && !opencodeHasModel() {
			s.Provider = "opencode"
		}
	}
	switch s.Provider {
	case "opencode":
		if s.Model == "" {
			s.Model = DefaultFreeModel
		}
	case "9router":
		if s.URL == "" {
			s.URL = "http://localhost:20128/v1"
		}
	case "deepseek":
		if s.URL == "" {
			s.URL = "https://api.deepseek.com/v1"
		}
		if s.Model == "" {
			s.Model = "deepseek-chat"
		}
	}
	switch {
	case (s.Provider == "openai" || s.Provider == "anthropic") && s.URL == "":
		s.Bin, s.Why = "", "provider "+s.Provider+" needs url (rig ai config url=https://…)"
	case (s.Provider == "openai" || s.Provider == "9router") && s.Model == "":
		s.Bin, s.Why = "", "provider "+s.Provider+" needs model (rig ai config model=…)"
	case (s.Provider == "deepseek" || s.Provider == "anthropic") && s.APIKey == "":
		s.Bin, s.Why = "", "provider "+s.Provider+" needs api_key (rig ai config api_key=…)"
	}
	return s
}

// find looks the backend up on PATH, then where its installers put it.
func find(backend string) string {
	if p, err := exec.LookPath(backend); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, p := range map[string][]string{
		BackendOpencode: {filepath.Join(home, ".opencode", "bin", "opencode")},
		BackendClaude:   {filepath.Join(home, ".local", "bin", "claude"), filepath.Join(home, ".claude", "local", "claude")},
	}[backend] {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// opencodeHasModel tells whether the user's opencode config picks a model, i.e. opencode is set up.
func opencodeHasModel() bool {
	dir, err := os.UserConfigDir()
	if err != nil {
		return false
	}
	for _, f := range []string{"opencode.json", "opencode.jsonc", "config.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, "opencode", f))
		if err == nil && strings.Contains(string(raw), `"model"`) {
			return true
		}
	}
	return false
}

// Describe is one line about the setup, for headers and `rig ai config`.
func (s Setup) Describe() string {
	if !s.Enabled() {
		return "off: " + s.Why
	}
	d := s.Backend + " · " + s.Provider
	if s.Model != "" {
		d += " · " + s.Model
	}
	if s.Effort != "" {
		d += " · " + s.Effort
	}
	if s.Proxy != "" {
		d += " · proxy " + s.Proxy
	}
	return d
}
