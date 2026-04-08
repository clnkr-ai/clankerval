#!/bin/sh
actual="$(sh ./app.sh)"
if [ "$actual" = "hello, world" ]; then
  printf 'PASS\n'
else
  printf 'FAIL: got %s\n' "$actual" >&2
  exit 1
fi
