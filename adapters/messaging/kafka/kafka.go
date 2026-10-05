// Package kafka reads and manages Kafka through its own protocol: topics with their sizes and the lag
// of the groups reading them, purge, publish, and queries for groups and the latest records.
package kafka

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"

	"github.com/goxang/rig/core"
	"github.com/goxang/rig/plugin"
	"github.com/goxang/rig/spec"
)

func init() {
	plugin.Register(core.KindMessaging, "kafka", "Kafka: topics, consumer group lag, purge, publish, latest records", New)
}

type Options struct {
	// Addr is one broker or several, comma separated (svc://kafka:9092).
	Addr     string `yaml:"addr"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	// SASL is plain (default with a user), scram-sha-256 or scram-sha-512.
	SASL string `yaml:"sasl"`
	TLS  bool   `yaml:"tls"`
}

type Kafka struct {
	opt Options
	env core.Env

	mu   sync.Mutex
	cl   *kgo.Client
	last map[string]sample // per topic, for rates
}

type sample struct {
	at       time.Time
	produced int64
	consumed int64
}

func New(env core.Env, c *spec.Component) (any, error) {
	k := &Kafka{env: env, last: map[string]sample{}}
	if err := c.Decode(&k.opt); err != nil {
		return nil, err
	}
	if k.opt.Addr == "" {
		return nil, fmt.Errorf("%s: addr is required (svc://kafka:9092)", c.Name)
	}
	return k, nil
}

func (k *Kafka) opts(ctx context.Context) ([]kgo.Opt, error) {
	var seeds []string
	for _, a := range strings.Split(k.opt.Addr, ",") {
		addr, err := k.env.Resolve(ctx, strings.TrimSpace(a))
		if err != nil {
			return nil, err
		}
		seeds = append(seeds, addr)
	}
	d := &net.Dialer{Timeout: 5 * time.Second}
	opts := []kgo.Opt{kgo.SeedBrokers(seeds...), kgo.Dialer(func(ctx context.Context, network, host string) (net.Conn, error) {
		// brokers advertise addresses of their own network (kafka:9092); reach them through the runtime
		if h, p, err := net.SplitHostPort(host); err == nil {
			if addr, err := k.env.Resolve(ctx, "svc://"+strings.Split(h, ".")[0]+":"+p); err == nil {
				host = addr
			}
		}
		c, err := d.DialContext(ctx, network, host)
		if err != nil && len(seeds) == 1 && host != seeds[0] {
			return d.DialContext(ctx, network, seeds[0])
		}
		return c, err
	})}
	if k.opt.TLS {
		opts = append(opts, kgo.DialTLSConfig(&tls.Config{}))
	}
	switch s := strings.ToLower(k.opt.SASL); {
	case k.opt.User == "":
	case s == "" || s == "plain":
		opts = append(opts, kgo.SASL(plain.Auth{User: k.opt.User, Pass: k.opt.Password}.AsMechanism()))
	case s == "scram-sha-256":
		opts = append(opts, kgo.SASL(scram.Auth{User: k.opt.User, Pass: k.opt.Password}.AsSha256Mechanism()))
	case s == "scram-sha-512":
		opts = append(opts, kgo.SASL(scram.Auth{User: k.opt.User, Pass: k.opt.Password}.AsSha512Mechanism()))
	default:
		return nil, fmt.Errorf("sasl %q: have plain, scram-sha-256, scram-sha-512", k.opt.SASL)
	}
	return opts, nil
}

func (k *Kafka) client(ctx context.Context) (*kgo.Client, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.cl != nil {
		return k.cl, nil
	}
	opts, err := k.opts(ctx)
	if err != nil {
		return nil, err
	}
	cl, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, err
	}
	k.cl = cl
	return cl, nil
}

func (k *Kafka) admin(ctx context.Context) (*kadm.Client, error) {
	cl, err := k.client(ctx)
	if err != nil {
		return nil, err
	}
	return kadm.NewClient(cl), nil
}

func (k *Kafka) Ping(ctx context.Context) error {
	cl, err := k.client(ctx)
	if err != nil {
		return err
	}
	return cl.Ping(ctx)
}

// Queues are the topics: Messages is what they keep, Ready the lag of every group reading them,
// Consumers those groups, and the rates come from the offsets since the last call.
func (k *Kafka) Queues(ctx context.Context) ([]core.Queue, error) {
	adm, err := k.admin(ctx)
	if err != nil {
		return nil, err
	}
	topics, err := adm.ListTopics(ctx)
	if err != nil {
		return nil, err
	}
	names := topics.Names()
	starts, err := adm.ListStartOffsets(ctx, names...)
	if err != nil {
		return nil, err
	}
	ends, err := adm.ListEndOffsets(ctx, names...)
	if err != nil {
		return nil, err
	}
	size, produced := map[string]int64{}, map[string]int64{}
	ends.Each(func(o kadm.ListedOffset) {
		produced[o.Topic] += o.Offset
		size[o.Topic] += o.Offset
	})
	starts.Each(func(o kadm.ListedOffset) { size[o.Topic] -= o.Offset })

	lag, groups, consumed := map[string]int64{}, map[string]int{}, map[string]int64{}
	if gs, err := adm.ListGroups(ctx); err == nil && len(gs) > 0 {
		lags, _ := adm.Lag(ctx, gs.Groups()...)
		for _, g := range lags {
			for t, tl := range g.Lag.TotalByTopic() {
				lag[t] += tl.Lag
				groups[t]++
			}
			for _, ml := range g.Lag.Sorted() {
				consumed[ml.Topic] += ml.Commit.At
			}
		}
	}

	now := time.Now()
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]core.Queue, 0, len(names))
	for _, t := range names {
		q := core.Queue{Name: t, Messages: int(size[t]), Ready: int(lag[t]), Consumers: groups[t]}
		if s, ok := k.last[t]; ok {
			if dt := now.Sub(s.at).Seconds(); dt > 0 {
				q.InRate = float64(produced[t]-s.produced) / dt
				q.OutRate = max(0, float64(consumed[t]-s.consumed)/dt)
			}
		}
		k.last[t] = sample{at: now, produced: produced[t], consumed: consumed[t]}
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Purge deletes every record the topic holds.
func (k *Kafka) Purge(ctx context.Context, topic string) error {
	adm, err := k.admin(ctx)
	if err != nil {
		return err
	}
	ends, err := adm.ListEndOffsets(ctx, topic)
	if err != nil {
		return err
	}
	var os kadm.Offsets
	ends.Each(func(o kadm.ListedOffset) { os.AddOffset(o.Topic, o.Partition, o.Offset, -1) })
	res, err := adm.DeleteRecords(ctx, os)
	if err != nil {
		return err
	}
	return res.Error()
}

// Publish sends body to target: "topic", or "topic/key".
func (k *Kafka) Publish(ctx context.Context, target string, body []byte) error {
	cl, err := k.client(ctx)
	if err != nil {
		return err
	}
	topic, key, _ := strings.Cut(target, "/")
	r := &kgo.Record{Topic: topic, Value: body}
	if key != "" {
		r.Key = []byte(key)
	}
	return cl.ProduceSync(ctx, r).FirstErr()
}

func (k *Kafka) QueryLanguage() string { return "kafka" }

// RunQuery: "topics", "groups", "lag <group>", "tail <topic> [n]" (the latest n records of each
// partition, default 20).
func (k *Kafka) RunQuery(ctx context.Context, q string) (core.Table, error) {
	f := strings.Fields(q)
	if len(f) == 0 {
		f = []string{"topics"}
	}
	adm, err := k.admin(ctx)
	if err != nil {
		return core.Table{}, err
	}
	switch f[0] {
	case "topics":
		qs, err := k.Queues(ctx)
		if err != nil {
			return core.Table{}, err
		}
		t := core.Table{Columns: []string{"topic", "messages", "lag", "groups", "in/s", "out/s"}}
		for _, q := range qs {
			t.Rows = append(t.Rows, []string{q.Name, strconv.Itoa(q.Messages), strconv.Itoa(q.Ready), strconv.Itoa(q.Consumers),
				strconv.FormatFloat(q.InRate, 'f', 1, 64), strconv.FormatFloat(q.OutRate, 'f', 1, 64)})
		}
		return t, nil
	case "groups":
		gs, err := adm.ListGroups(ctx)
		if err != nil {
			return core.Table{}, err
		}
		lags, err := adm.Lag(ctx, gs.Groups()...)
		if err != nil {
			return core.Table{}, err
		}
		t := core.Table{Columns: []string{"group", "state", "members", "lag", "topics"}}
		for _, g := range lags.Sorted() {
			var ts []string
			for tp := range g.Lag {
				ts = append(ts, tp)
			}
			sort.Strings(ts)
			t.Rows = append(t.Rows, []string{g.Group, g.State, strconv.Itoa(len(g.Members)), strconv.FormatInt(g.Lag.Total(), 10), strings.Join(ts, ",")})
		}
		return t, nil
	case "lag":
		if len(f) < 2 {
			return core.Table{}, fmt.Errorf("lag <group>")
		}
		lags, err := adm.Lag(ctx, f[1])
		if err != nil {
			return core.Table{}, err
		}
		t := core.Table{Columns: []string{"topic", "partition", "committed", "end", "lag", "member"}}
		for _, ml := range lags[f[1]].Lag.Sorted() {
			member := ""
			if ml.Member != nil {
				member = ml.Member.ClientID + "@" + ml.Member.ClientHost
			}
			t.Rows = append(t.Rows, []string{ml.Topic, strconv.Itoa(int(ml.Partition)), strconv.FormatInt(ml.Commit.At, 10),
				strconv.FormatInt(ml.End.Offset, 10), strconv.FormatInt(ml.Lag, 10), member})
		}
		return t, nil
	case "tail":
		if len(f) < 2 {
			return core.Table{}, fmt.Errorf("tail <topic> [n]")
		}
		n := int64(20)
		if len(f) > 2 {
			n, _ = strconv.ParseInt(f[2], 10, 64)
		}
		return k.tail(ctx, adm, f[1], max(1, n))
	}
	return core.Table{}, fmt.Errorf("%q: have topics, groups, lag <group>, tail <topic> [n]", f[0])
}

func (k *Kafka) tail(ctx context.Context, adm *kadm.Client, topic string, n int64) (core.Table, error) {
	starts, err := adm.ListStartOffsets(ctx, topic)
	if err != nil {
		return core.Table{}, err
	}
	ends, err := adm.ListEndOffsets(ctx, topic)
	if err != nil {
		return core.Table{}, err
	}
	from, want := map[int32]kgo.Offset{}, 0
	ends.Each(func(o kadm.ListedOffset) {
		start := o.Offset - n
		if s, ok := starts.Lookup(o.Topic, o.Partition); ok {
			start = max(start, s.Offset)
		}
		if start < o.Offset {
			from[o.Partition] = kgo.NewOffset().At(start)
			want += int(o.Offset - start)
		}
	})
	t := core.Table{Columns: []string{"partition", "offset", "time", "key", "value"}}
	if want == 0 {
		t.Note = "the topic is empty"
		return t, nil
	}
	opts, err := k.opts(ctx)
	if err != nil {
		return t, err
	}
	cl, err := kgo.NewClient(append(opts, kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: from}))...)
	if err != nil {
		return t, err
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for got := 0; got < want; {
		fs := cl.PollFetches(ctx)
		if ctx.Err() != nil {
			break
		}
		fs.EachRecord(func(r *kgo.Record) {
			got++
			t.Rows = append(t.Rows, []string{strconv.Itoa(int(r.Partition)), strconv.FormatInt(r.Offset, 10),
				r.Timestamp.Format("2006-01-02 15:04:05.000"), string(r.Key), string(r.Value)})
		})
	}
	sort.SliceStable(t.Rows, func(i, j int) bool { return t.Rows[i][2] > t.Rows[j][2] })
	return t, nil
}
