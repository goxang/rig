#!/bin/sh
# Arguments arrive as JSON in $RIG_ARGS and on stdin.
name=$(printf '%s' "$RIG_ARGS" | sed -n 's/.*"name": *"\([^"]*\)".*/\1/p')
echo "hello $name"
