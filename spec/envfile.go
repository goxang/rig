package spec

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// readEnvFiles puts the env_file entries under the service's own env.
func (s *Service) readEnvFiles(dir string) error {
	if len(s.EnvFile) == 0 {
		return nil
	}
	env := map[string]string{}
	for _, f := range s.EnvFile {
		if !filepath.IsAbs(f) {
			f = filepath.Join(dir, f)
		}
		m, err := ReadEnvFile(f)
		if err != nil {
			return err
		}
		for k, v := range m {
			env[k] = v
		}
	}
	for k, v := range s.Env {
		env[k] = v
	}
	s.Env = env
	return nil
}

// ReadEnvFile reads a dotenv file: KEY=VALUE lines, optional export, # comments, quoted values.
func ReadEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, sc.Err()
}
