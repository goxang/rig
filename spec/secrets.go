package spec

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
