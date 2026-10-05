package engine

import (
	"bufio"
	"context"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/goxang/rig/internal/sh"
	"github.com/goxang/rig/spec"
)

// runCommand runs a command suite: its output streams into one row named after the suite, and the
// JUnit XML it leaves in $RIG_JUNIT becomes a row per test. $RIG_RUN is the regex of the tests to
// run again (rig test --failed), ".*" for all: jest -t, node --test-name-pattern, go -run take it.
func runCommand(ctx context.Context, s *spec.TestSuite, run *TestRun, dir string, env []string) error {
	f, err := os.CreateTemp("", "rig-junit-*.xml")
	if err != nil {
		return err
	}
	junit := f.Name()
	f.Close()
	os.Remove(junit) // a stale empty file must not read as "no tests"
	defer os.Remove(junit)

	cmd := exec.Command("sh", "-c", s.Command)
	sh.OwnGroup(cmd)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), env...), "RIG_JUNIT="+junit)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	run.mu.Lock()
	run.Commands = append(run.Commands, []string{"sh", "-c", s.Command})
	whole := run.get(s.Name, "", time.Now())
	run.mu.Unlock()
	if err := cmd.Start(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { sh.KillGroup(cmd) })
	defer stop()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); pw.Close() }()
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		run.mu.Lock()
		whole.Output = append(whole.Output, sc.Text())
		if len(whole.Output) > maxOutput {
			whole.Output = whole.Output[len(whole.Output)-maxOutput:]
		}
		run.mu.Unlock()
	}
	err = <-done
	run.mu.Lock()
	defer run.mu.Unlock()
	whole.Elapsed = time.Since(whole.Started).Seconds()
	whole.Status = TestPass
	if err != nil {
		whole.Status = TestFail
	}
	if raw, rerr := os.ReadFile(junit); rerr == nil && run.applyJUnit(raw, whole.Started) > 0 {
		whole.Status = TestPass // the failed tests carry the failure
	}
	if _, failed := err.(*exec.ExitError); failed {
		return nil
	}
	return err
}

// applyJUnit adds a row per test case of a JUnit report, under a row per class (or suite), and
// returns how many failed.
func (r *TestRun) applyJUnit(raw []byte, at time.Time) (failed int) {
	var doc junitSuites
	if xml.Unmarshal(raw, &doc) != nil || len(doc.Suites) == 0 {
		var one junitSuite
		if xml.Unmarshal(raw, &one) != nil {
			return 0
		}
		doc.Suites = []junitSuite{one}
	}
	for _, su := range doc.Suites {
		for _, tc := range su.Cases {
			pkg := tc.Classname
			if pkg == "" {
				pkg = su.Name
			}
			pkg = strings.TrimSuffix(filepath.ToSlash(pkg), ".py")
			parent := r.get(pkg, "", at)
			if parent.Status == TestRunning {
				parent.Status = TestPass
			}
			c := r.get(pkg, strings.ReplaceAll(tc.Name, "/", "∕"), at)
			c.Elapsed, _ = strconv.ParseFloat(tc.Time, 64)
			parent.Elapsed += c.Elapsed
			c.Status = TestPass
			for _, m := range []*junitMessage{tc.Failure, tc.Error} {
				if m != nil {
					c.Status, parent.Status = TestFail, TestFail
					failed++
					c.Output = append(c.Output, strings.Split(strings.TrimSpace(m.Message+"\n"+m.Body), "\n")...)
				}
			}
			if tc.Skipped != nil {
				c.Status = TestSkip
				if tc.Skipped.Message != "" {
					c.Output = append(c.Output, tc.Skipped.Message)
				}
			}
			if out := strings.TrimSpace(tc.SystemOut); out != "" {
				c.Output = append(c.Output, strings.Split(out, "\n")...)
			}
		}
	}
	return failed
}
