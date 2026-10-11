#!/usr/bin/env node
import { Plugin } from "./rig/index.js";

new Plugin("{%.Name%}", "0.1.0")
  .source("items", () => [{ name: "alpha", count: 1 }, { name: "beta", count: 2 }], {
    description: "sample rows: replace with your data",
  })
  .action("hello", "write", (_ctx, a: { name: string }) => "hello " + a.name, {
    description: "greet someone",
    args: { name: { type: "string", description: "who to greet" } },
    required: ["name"],
  })
  .serve();
