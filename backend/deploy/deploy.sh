#!/usr/bin/env bash
set -euo pipefail
# Run on the Mac that already has the Screenshot AI inventory and SSH key.
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
inventory=${SCREENSHOT_AI_HOSTS:-"$repo/../screenshot_ai/backend/deploy/hosts.json"}
[[ -f "$inventory" ]] || { echo "Set SCREENSHOT_AI_HOSTS to Screenshot AI hosts.json" >&2; exit 1; }
node=${SLIDES_NODE:-sai-api-a}
target=()
while IFS= read -r line; do target+=("$line"); done < <(python3 - "$inventory" "$node" <<'PY'
import json,sys,os
c=json.load(open(sys.argv[1]))
p=next((p for p in c.get('pods',[]) if p.get('name')==sys.argv[2]),None)
if not p: raise SystemExit('Node missing from inventory')
print(p['host']);print(p.get('ssh_user') or c.get('ssh_user') or 'root');print(os.path.expanduser(c.get('ssh_key') or ''))
PY
)
[[ ${#target[@]} == 3 && -n "${target[0]}" ]] || exit 1
host=${target[0]}; user=${target[1]}; key=${target[2]}
opts=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15)
[[ -z "$key" ]] || opts+=(-o IdentitiesOnly=yes -i "$key")
# Only tracked sources are shipped. Credentials stay on the host.
bundle=$(mktemp)
trap 'rm -f "$bundle"' EXIT
git -C "$repo" archive HEAD backend | gzip > "$bundle"
ssh "${opts[@]}" "$user@$host" 'test -s /opt/slides/slides.env || { echo "Create /opt/slides/slides.env first (see docs/deployment.md)" >&2; exit 1; }; mkdir -p /opt/slides/app'
scp "${opts[@]}" "$bundle" "$user@$host:/opt/slides/release.tgz"
ssh "${opts[@]}" "$user@$host" 'set -eu; tar xzf /opt/slides/release.tgz -C /opt/slides/app; cd /opt/slides/app; docker compose -f backend/deploy/compose.yml up -d --build --wait; rm /opt/slides/release.tgz; curl --fail http://127.0.0.1:8280/healthz'
echo "API deployed. Verify external firewall, then: curl --fail http://$host:8280/healthz"
