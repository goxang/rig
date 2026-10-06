package spec

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// SecretsFile is where `rig secret set` keeps a project's secrets: the user's config directory,
// readable by the user only, never inside the repository.
func SecretsFile(project string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "rig", "secrets", project+".json"), nil
}

// DataDir is where rig keeps what it writes for a project checkout outside the repository (saved
// sessions): the user's config directory, one folder per project path.
func DataDir(projectDir string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "rig", "projects", strings.NewReplacer("/", "-", `\`, "-", ":", "").Replace(abs)), nil
}

func LoadSecrets(project string) (map[string]string, error) {
	f, err := SecretsFile(project)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(f)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	return m, json.Unmarshal(raw, &m)
}

// SetSecret stores (or with value "" removes) one secret of a project.
func SetSecret(project, name, value string) error {
	m, err := LoadSecrets(project)
	if err != nil {
		return err
	}
	if value == "" {
		delete(m, name)
	} else {
		m[name] = value
	}
	f, _ := SecretsFile(project)
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(m, "", "  ")
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f)
}

// SecretValues are the values the project's secrets resolve to, as Load expands them: the
// environment, then `rig secret set`, then the default. Empty ones are left out.
func (p *Project) SecretValues() map[string]string {
	stored, _ := LoadSecrets(p.Name)
	out := map[string]string{}
	for n, s := range p.Secrets {
		v, ok := os.LookupEnv(n)
		if !ok {
			v, ok = stored[n]
		}
		if !ok {
			v = s.Default
		}
		if v != "" {
			out[n] = v
		}
	}
	return out
}
