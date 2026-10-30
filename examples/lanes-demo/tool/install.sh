#!/usr/bin/env bash
set -euo pipefail

mkdir -p .demo
echo "$(date -u +%FT%TZ) ${DEMO_DATABASE_URL:-}" >>.demo/installs
