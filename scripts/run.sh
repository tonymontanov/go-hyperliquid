#!/usr/bin/env bash
# Runs an example with the variables of .env exported.
#   ./scripts/run.sh ./examples/simple-trade
# .env is git-ignored; start from .env.example. Never commit real keys.
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ ! -f .env ]]; then
  echo "scripts/run.sh: .env is missing (cp .env.example .env and fill it in)" >&2
  exit 1
fi
set -a
# shellcheck disable=SC1091
source .env
set +a
exec go run "$@"
