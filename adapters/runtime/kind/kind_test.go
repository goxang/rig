package kind

import "testing"

func TestDockerBytes(t *testing.T) {
	for in, want := range map[string]int64{"1.5GiB": 3 << 29, "512MiB": 512 << 20, "900kB": 900000, "0B": 0} {
		if got := dockerBytes(in); got != want {
			t.Errorf("dockerBytes(%q) = %d, want %d", in, got, want)
		}
	}
}
