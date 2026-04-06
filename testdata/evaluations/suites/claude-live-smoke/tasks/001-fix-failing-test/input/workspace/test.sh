#!/bin/sh
set -eu

actual="$(sh ./app.sh)"
if [ "$actual" != "hello, world" ]; then
  printf 'FAIL: expected hello, world but got %s\n' "$actual" >&2
  exit 1
fi

printf 'PASS\n'
