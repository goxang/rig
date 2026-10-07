package ai

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	r := NewRedactor(map[string]string{"DB_PASSWORD": "s3cr3t-pg-pass", "SHORT": "shop"})
	for _, c := range []struct{ in, want string }{
		{`level=error msg="connect" dsn=postgres://app:s3cr3t-pg-pass@db:5432/app`, `level=error msg="connect" dsn=postgres://app:<secret:DB_PASSWORD>@db:5432/app`},
		{`dial postgres://postgres:shop@postgres:5432/shop: connection refused`, `dial postgres://postgres:<secret:url-password>@postgres:5432/shop: connection refused`},
		{`amqp://guest:guest@rabbitmq:5672/ connected`, `amqp://guest:<secret:url-password>@rabbitmq:5672/ connected`},
		{`sqlserver://SWITCH_V2:SwitchV2@mssql:1433?database=Switch`, `sqlserver://SWITCH_V2:<secret:url-password>@mssql:1433?database=Switch`},
		{`Server=db;User Id=sa;Password=Hunter2!;Database=Switch`, `Server=db;User Id=sa;Password=<secret:value>;Database=Switch`},
		{`{"level":"info","user":"bob","password":"hunter22","ok":true}`, `{"level":"info","user":"bob","password":"<secret:value>","ok":true}`},
		{`GET /api/orders Authorization: Bearer abcdef0123456789.xyz 200`, `GET /api/orders Authorization: Bearer <secret:bearer> 200`},
		{`token=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U`, `token=<secret:value>`},
		{`jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U expired`, `jwt <secret:jwt> expired`},
		{`using AKIAIOSFODNN7EXAMPLE for s3`, `using <secret:aws-access-key> for s3`},
		{`git clone https://ghp_abcdefghijklmnopqrstuvwxyz0123456789@github.com/x/y`, `git clone https://<secret:github-token>@github.com/x/y`},
		{`export GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123456789`, `export GITHUB_TOKEN=<secret:github-token>`},
		{`api_key: sk-proj-abcdefghijklmnopqrstuv`, `api_key: <secret:value>`},
		{"key:\n-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA\n-----END RSA PRIVATE KEY-----", "key:\n<secret:private-key>"},
		{`pan=4111111111111111 pin=1234 ok`, `pan=<secret:pan> pin=<secret:pin> ok`},
		{`already <secret:DB_PASSWORD> and secret: <secret:value>`, `already <secret:DB_PASSWORD> and secret: <secret:value>`},
		{`shop api listening on :8080 (max_tokens=500, token_count=3)`, `shop api listening on :8080 (max_tokens=500, token_count=3)`},
	} {
		if got := r.Redact(c.in); got != c.want {
			t.Errorf("\n in: %s\ngot: %s\nwant %s", c.in, got, c.want)
		}
	}
	if got := (*Redactor)(nil).Redact("password=x"); got != "password=x" {
		t.Errorf("nil redactor changed %q", got)
	}
	if strings.Contains(r.Redact("pw s3cr3t-pg-pass"), "s3cr3t") {
		t.Error("known value left in")
	}
}
