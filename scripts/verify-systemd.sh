#!/bin/sh

set -eu

unit_path=${1:-packaging/systemd/microhook.service}

if [ "$(uname -s)" != "Linux" ]; then
  echo "systemd verification skipped: Linux only" >&2
  exit 0
fi

if ! command -v systemd-analyze >/dev/null 2>&1; then
  echo "systemd verification skipped: systemd-analyze not available" >&2
  exit 0
fi

systemd-analyze verify "${unit_path}"
