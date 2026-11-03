package host

import (
	"os"

	"github.com/landlock-lsm/go-landlock/landlock"
)

// systemRO is what any program needs to start: binaries, libraries, certificates, locale.
var systemRO = []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc", "/opt", "/nix", "/proc", "/sys", "/snap"}

func confine(s sandboxSpec) error {
	roDirs, roFiles := splitFiles(append(systemRO, s.ro...))
	rwDirs, rwFiles := splitFiles(append([]string{"/tmp", "/var/tmp", "/dev/shm"}, s.rw...))
	rwFiles = append(rwFiles, "/dev/null", "/dev/zero", "/dev/urandom", "/dev/random", "/dev/tty")
	rules := []landlock.Rule{
		landlock.RODirs(roDirs...).IgnoreIfMissing(),
		landlock.ROFiles(roFiles...).IgnoreIfMissing(),
		landlock.RWDirs(rwDirs...).IgnoreIfMissing(),
		landlock.RWFiles(rwFiles...).IgnoreIfMissing(),
	}
	cfg := landlock.V5.BestEffort()
	if len(s.ports) == 0 && len(s.listen) == 0 {
		return cfg.RestrictPaths(rules...)
	}
	for _, p := range s.ports {
		rules = append(rules, landlock.ConnectTCP(p))
	}
	for _, p := range s.listen {
		rules = append(rules, landlock.BindTCP(p))
	}
	return cfg.Restrict(rules...)
}

// splitFiles separates regular files, which Landlock grants only file rights on.
func splitFiles(paths []string) (dirs, files []string) {
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			files = append(files, p)
		} else {
			dirs = append(dirs, p)
		}
	}
	return dirs, files
}

const sandboxed = true
