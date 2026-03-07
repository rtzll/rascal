#!/usr/bin/env bash
set -euo pipefail

exec /usr/local/bin/rascal-runner >>/rascal-meta/runner.log 2>&1
