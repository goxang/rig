// Package transcript records and replays rig/1 sessions as JSONL: one {"dir":">","msg":{...}} per
// message, ">" from the host to the plugin and "<" back. A replay sends the host's messages and checks
// the plugin answers with the recorded ones. Record sessions that do not depend on time: a stream's
// batching follows the clock.
package transcript

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

type Line struct {
	Dir string          `json:"dir"`
	Msg json.RawMessage `json:"msg"`
}

type Recorder struct {
	mu sync.Mutex
	w  io.Writer
}

func NewRecorder(w io.Writer) *Recorder { return &Recorder{w: w} }

// Tap is rpc.Options.Tap.
func (r *Recorder) Tap(dir byte, line []byte) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if e.Encode(Line{Dir: string(dir), Msg: bytes.TrimSpace(line)}) != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = r.w.Write(b.Bytes())
}

func Parse(r io.Reader) ([]Line, error) {
	var out []Line
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 64<<20)
	for n := 1; sc.Scan(); n++ {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var l Line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil || l.Dir != ">" && l.Dir != "<" {
			return nil, fmt.Errorf("line %d: not a transcript line", n)
		}
		out = append(out, l)
	}
	return out, sc.Err()
}

func canonical(raw []byte) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// Replay runs cmd and plays the transcript to it. Each run of plugin lines between two host lines is
// compared as a set, so concurrent answers may come in any order.
func Replay(ctx context.Context, cmd *exec.Cmd, lines []Line, wait time.Duration) error {
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		_ = in.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(wait):
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	got := make(chan []byte)
	go func() {
		br := bufio.NewReader(out)
		for {
			l, err := br.ReadBytes('\n')
			if len(bytes.TrimSpace(l)) > 0 {
				got <- l
			}
			if err != nil {
				close(got)
				return
			}
		}
	}()
	for i := 0; i < len(lines); {
		if lines[i].Dir == ">" {
			if _, err := in.Write(append(append([]byte{}, lines[i].Msg...), '\n')); err != nil {
				return fmt.Errorf("message %d: %w", i+1, err)
			}
			i++
			continue
		}
		var want, have []string
		start := i
		for ; i < len(lines) && lines[i].Dir == "<"; i++ {
			want = append(want, canonical(lines[i].Msg))
		}
		for range want {
			select {
			case l, ok := <-got:
				if !ok {
					return fmt.Errorf("message %d: the plugin closed its output", start+len(have)+1)
				}
				have = append(have, canonical(l))
			case <-time.After(wait):
				return fmt.Errorf("message %d: no answer within %s; want %s", start+len(have)+1, wait, want[len(have)])
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		sort.Strings(want)
		sort.Strings(have)
		if strings.Join(want, "\n") != strings.Join(have, "\n") {
			return fmt.Errorf("messages %d-%d differ:\nwant\n  %s\ngot\n  %s", start+1, i, strings.Join(want, "\n  "), strings.Join(have, "\n  "))
		}
	}
	return nil
}
