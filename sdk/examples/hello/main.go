// hello is the example plugin each SDK implements the same way; conformance and transcript tests run it.
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/goxang/rig/sdk"
)

type greetArgs struct {
	Name string `json:"name" desc:"who to greet" rig:"required"`
}

type greetingsArgs struct {
	Name string `json:"name" desc:"who to greet; world by default"`
}

type ticksArgs struct {
	N int `json:"n" desc:"how many ticks; 0 ticks until cancelled"`
}

func main() {
	p := sdk.New("hello").Version("0.1.0")
	p.Source("greetings", func(c *sdk.Ctx, a greetingsArgs) (*sdk.Frame, error) {
		if a.Name == "" {
			a.Name = "world"
		}
		return sdk.NewFrame(sdk.Col("name", sdk.TypeString), sdk.Col("count", sdk.TypeInt)).Append(a.Name, 1), nil
	}, sdk.Describe("one greeting"))
	p.Stream("ticks", func(c *sdk.Ctx, a ticksArgs, out *sdk.Out) error {
		for i := 1; a.N == 0 || i <= a.N; i++ {
			select {
			case <-c.Done():
				return c.Err()
			case <-time.After(10 * time.Millisecond):
			}
			if err := out.Line(fmt.Sprintf("tick %d", i)); err != nil {
				return err
			}
		}
		return nil
	}, sdk.Describe("a line every 10ms"))
	p.Action("greet", sdk.Write, func(c *sdk.Ctx, a greetArgs) (*sdk.Result, error) {
		c.Progress(1, 1, "greeting "+a.Name)
		return &sdk.Result{Message: "hello " + a.Name}, nil
	}, sdk.Describe("greet someone"), sdk.Keys("G"))
	p.Plan("greet", func(c *sdk.Ctx, a greetArgs) (*sdk.Result, error) {
		return &sdk.Result{Changes: []sdk.Change{{Op: sdk.OpRun, Target: "hello://" + a.Name}}}, nil
	})
	p.Resolver([]string{"hello"}, func(c *sdk.Ctx, ref sdk.Ref) (*sdk.Resolved, error) {
		name, ok := strings.CutPrefix(ref, "hello://")
		if !ok {
			return nil, sdk.Errorf(sdk.CodeNotFound, "not a hello ref: %s", ref)
		}
		return &sdk.Resolved{Ref: ref, Kind: "greeting", Title: "hello " + name}, nil
	})
	p.Searcher(func(c *sdk.Ctx, q sdk.SearchParams) ([]sdk.Hit, error) {
		if !strings.Contains("world", q.Query) {
			return nil, nil
		}
		return []sdk.Hit{{Ref: "hello://world", Title: "hello world"}}, nil
	})
	p.Doctor(func(c *sdk.Ctx) []sdk.Check { return []sdk.Check{{Name: "hello", Ok: true}} })
	p.Serve()
}
