#!/usr/bin/env bash
set -euo pipefail

postgres_url="${TEAMVA_TEST_DATABASE_URL:-}"

printf 'go=%s\n' "$(go version)"
printf 'node=%s\n' "$(node --version)"
printf 'pnpm=%s\n' "$(pnpm --version)"

if command -v codex >/dev/null 2>&1; then
  printf 'codex=%s\n' "$(codex --version)"
else
  printf 'codex=missing\n'
fi

if command -v opencode >/dev/null 2>&1; then
  printf 'opencode=%s\n' "$(opencode --version)"
else
  printf 'opencode=missing\n'
fi

if command -v claude >/dev/null 2>&1; then
  printf 'claude=%s\n' "$(claude --version)"
else
  printf 'claude=missing\n'
fi

if [[ -n "${postgres_url}" ]]; then
  psql "${postgres_url}" -v ON_ERROR_STOP=1 -Atc 'SELECT current_database(), version()' >/dev/null
  printf 'postgres=reachable\n'
fi
