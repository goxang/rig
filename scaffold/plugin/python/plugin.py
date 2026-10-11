#!/usr/bin/env python3
from rig import Plugin

p = Plugin("{%.Name%}", version="0.1.0")


@p.source("items", description="sample rows: replace with your data")
def items(ctx, args):
    return [{"name": "alpha", "count": 1}, {"name": "beta", "count": 2}]


@p.action("hello", effect="write", description="greet someone",
          args={"name": {"type": "string", "description": "who to greet"}}, required=["name"])
def hello(ctx, args):
    return "hello " + args["name"]


p.serve()
