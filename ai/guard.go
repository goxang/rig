package ai

import (
	"path/filepath"
	"regexp"
	"strings"
)

type Risk int

const (
	Read Risk = iota
	Change
	Danger
	Refused
)

func (r Risk) String() string {
	return [...]string{"read", "change", "dangerous change", "refused"}[r]
}

// Classify rates a rig command line an assistant wants to run. Unknown commands count as changes,
// anything that runs arbitrary commands or reaches secrets as dangerous or refused.
func Classify(argv []string) Risk {
	if len(argv) == 0 {
		return Read
	}
	sub := ""
	if len(argv) > 1 {
		sub = argv[1]
	}
	switch argv[0] {
	case "env", "status", "discover", "logs", "traces", "metrics", "alerts", "plugins", "version", "report", "profile", "data", "source":
		return Read
	case "query", "cache":
		if len(argv) <= 2 {
			return Read // a saved query by name, or a listing
		}
		return ClassifyQuery(strings.Join(argv[2:], " "))
	case "db":
		switch sub {
		case "", "ls", "list":
			return Read
		case "query":
			return ClassifyQuery(strings.Join(argv[2:], " "))
		case "drop", "create":
			return Danger
		}
		return Danger
	case "kv":
		switch sub {
		case "ls", "get", "":
			return Read
		case "put":
			return Change
		}
		return Danger
	case "load":
		switch sub {
		case "ls", "":
			return Read
		case "run":
			return Refused // runs until Ctrl-C
		}
		return Change
	case "queue":
		switch sub {
		case "", "ls", "list":
			return Read
		case "publish":
			return Change
		}
		return Danger
	case "task":
		if sub == "" {
			return Read
		}
		return Danger
	case "infra":
		if sub == "" || sub == "status" {
			return Read
		}
		return Danger // infrastructure may run for other environments too
	case "manifests":
		for _, a := range argv[1:] {
			if a == "apply" || a == "delete" || a == "rm" {
				return Danger
			}
		}
		return Read
	case "ns", "vars":
		if sub == "" {
			return Read
		}
		return Change
	case "test":
		return Change
	case "up", "start", "restart", "build", "setenv":
		return Change
	case "scale":
		last := argv[len(argv)-1]
		if strings.HasPrefix(last, "-") || last == "0" {
			return Danger
		}
		return Change
	case "down", "stop", "deploy", "do", "hosts", "exec":
		if argv[0] == "hosts" && sub == "" {
			return Read
		}
		return Danger
	case "secret", "mcp", "debug", "init", "ide", "resume", "ai", "completion", "help":
		return Refused
	}
	return Change
}

var (
	sqlComment = regexp.MustCompile(`(?s)/\*.*?\*/|--[^\n]*`)
	hasWhere   = regexp.MustCompile(`(?i)\bwhere\b`)
	readVerbs  = set("select", "with", "show", "explain", "describe", "desc", "values", "table", "sp_help", "sp_helptext", "sp_who", "sp_who2",
		"get", "mget", "hget", "hgetall", "hmget", "hkeys", "hvals", "hlen", "lrange", "llen", "smembers", "scard", "zrange", "zcard", "zscore",
		"scan", "sscan", "hscan", "zscan", "keys", "type", "ttl", "pttl", "exists", "info", "dbsize", "strlen", "memory", "ping", "time", "object", "slowlog",
		"top", "logs", "api-resources", "version", "queues", "exchanges", "bindings", "connections", "overview", "up", "rate", "sum", "avg", "count", "histogram_quantile", "increase", "max", "min")
	withWrite   = regexp.MustCompile(`(?i)\b(delete|update|insert|merge|drop|truncate)\b`)
	changeVerbs = set("insert", "merge", "exec", "execute", "call", "create", "set", "hset", "hdel", "lpush", "rpush", "sadd", "srem", "zadd", "zrem", "expire", "persist", "incr", "decr", "incrby", "apply", "edit", "patch", "scale", "label", "annotate", "publish")
	dangerVerbs = set("drop", "truncate", "alter", "grant", "revoke", "shutdown", "kill", "flushall", "flushdb", "del", "unlink", "config", "rename",
		"drain", "cordon", "rollout", "purge", "rm", "remove", "debug", "replace")
)

func set(words ...string) map[string]bool {
	m := map[string]bool{}
	for _, w := range words {
		m[w] = true
	}
	return m
}

// ClassifyQuery rates query text by its statements' leading words: SQL, a redis command, kubectl
// arguments, PromQL. UPDATE and DELETE without WHERE are dangerous.
func ClassifyQuery(q string) Risk {
	q = strings.TrimSpace(q)
	if strings.HasPrefix(q, "@") {
		_, q, _ = strings.Cut(q, " ")
	}
	q = sqlComment.ReplaceAllString(q, " ")
	worst := Read
	for _, stmt := range strings.FieldsFunc(q, func(r rune) bool { return r == ';' }) {
		r := classifyStatement(stmt)
		if r > worst {
			worst = r
		}
	}
	return worst
}

func classifyStatement(s string) Risk {
	f := strings.Fields(strings.ToLower(s))
	if len(f) == 0 {
		return Read
	}
	verb := f[0]
	if verb == "go" && len(f) > 1 {
		verb = f[1]
	}
	base := verb
	if i := strings.IndexAny(verb, "{(["); i >= 0 {
		base = verb[:i]
	}
	switch {
	case base == "delete" || base == "update":
		// kubectl delete has no WHERE either: dangerous both ways
		if !hasWhere.MatchString(s) {
			return Danger
		}
		return Change
	case dangerVerbs[base]:
		return Danger
	case base == "with":
		if withWrite.MatchString(s) {
			return Change
		}
		return Read
	case readVerbs[base]:
		return Read
	case changeVerbs[base]:
		return Change
	case base != verb:
		return Read // a PromQL selector or function call
	}
	return Change
}

// Quoted reports whether quote is the user's own words from their message: verbatim (case and
// spacing aside) and long enough not to match by accident.
func Quoted(message, quote string) bool {
	norm := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
	m, q := norm(message), norm(strings.Trim(quote, `"'“”`))
	if q == "" || m == "" {
		return false
	}
	if len(q) < 8 && q != m {
		return false
	}
	return strings.Contains(m, q)
}

// DeniedPath tells whether rel, a slash path inside the project, is one of DefaultDeny or extra
// (globs: * within a name, ** across directories; a pattern without / matches a name at any depth).
func DeniedPath(rel string, extra []string) bool {
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "./")
	for _, p := range append(append([]string{}, DefaultDeny...), extra...) {
		p = strings.TrimPrefix(p, "./")
		if !strings.Contains(p, "/") {
			p = "**/" + p
		}
		// a/** keeps a itself too: deleting or moving the directory would reach what is in it
		if globRe(p).MatchString(rel) || globRe(p+"/**").MatchString(rel) || globRe(strings.TrimSuffix(p, "/**")).MatchString(rel) {
			return true
		}
	}
	return false
}

func globRe(p string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); i++ {
		switch {
		case strings.HasPrefix(p[i:], "**/"):
			b.WriteString("(.*/)?")
			i += 2
		case strings.HasPrefix(p[i:], "**"):
			b.WriteString(".*")
			i++
		case p[i] == '*':
			b.WriteString("[^/]*")
		case p[i] == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(p[i : i+1]))
		}
	}
	return regexp.MustCompile(b.String() + "$")
}
