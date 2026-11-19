#!/usr/bin/env bash
# Same entry point as Screenshot AI; works with macOS Bash 3.2.
set -euo pipefail
exec python3 "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/deploy.py" "$@"
