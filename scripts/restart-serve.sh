#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

echo "building..."
go build ./cmd/moxie/

echo "installing..."
go install ./cmd/moxie/

echo "restarting moxie-serve..."
systemctl --user restart moxie-serve.service

sleep 1
systemctl --user status moxie-serve.service --no-pager
