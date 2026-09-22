#!/bin/sh
# Compatibility wrapper. Configuration and defaults are resolved by the application.
# This command reads local files only; it never dispatches models or providers.
set -eu
cd "$(dirname "$0")/../.."
if [ "$#" -gt 1 ]; then
  echo "usage: $0 [indices]" >&2
  exit 2
fi
if [ "$#" -eq 1 ]; then
  exec go run ./cmd/cfr acceptance-manifest --indices "$1"
fi
exec go run ./cmd/cfr acceptance-manifest
