#!/usr/bin/env bash
# Executed over SSH by deploy.py. No credentials in command-line arguments.
set -euo pipefail
release=$1
skip_build=$2
[[ "$release" =~ ^[0-9]{14}-[a-f0-9]{8}$ ]] || exit 2
[[ "$skip_build" == 0 || "$skip_build" == 1 ]] || exit 2
exec 9>/opt/slides/deploy.lock
flock -n 9 || { echo 'Another Slides deploy is running' >&2; exit 1; }
umask 077
incoming="/opt/slides/incoming/$release"
release_dir="/opt/slides/releases/$release"
cleanup() { rm -f "$incoming.tgz" "$incoming.env"; }
trap cleanup EXIT
mkdir -p "$release_dir"
tar xzf "$incoming.tgz" -C "$release_dir"
cd "$release_dir"
export SLIDES_RELEASE="$release"
compose=(docker compose -f backend/deploy/compose.yml)
# Build before replacing the running service; no local Docker/QEMU needed on Mac.
if [[ "$skip_build" == 1 ]]; then
  docker image inspect slides-api:latest >/dev/null
  docker tag slides-api:latest "slides-api:$release"
else
  SLIDES_ENV_FILE="$incoming.env" "${compose[@]}" build api
fi
# Keep previous env/release for rollback; server env stays outside source trees.
previous=$(readlink /opt/slides/app 2>/dev/null || true)
# Initial versions used a real directory; keep it available until health passes.
if [[ -d /opt/slides/app && ! -L /opt/slides/app ]]; then
  previous=/opt/slides/app
fi
had_env=0
if [[ -f /opt/slides/slides.env ]]; then
  cp /opt/slides/slides.env "$release_dir/previous.env"
  had_env=1
fi
install -m 600 "$incoming.env" /opt/slides/slides.env
rollback() {
  echo 'Deploy unhealthy; restoring previous configuration' >&2
  if [[ "$had_env" == 1 ]]; then
    cp "$release_dir/previous.env" /opt/slides/slides.env
    chmod 600 /opt/slides/slides.env
  fi
  if [[ -n "$previous" && -f "$previous/backend/deploy/compose.yml" ]]; then
    old_release=$(basename "$previous")
    SLIDES_RELEASE="$old_release" docker compose -f "$previous/backend/deploy/compose.yml" up -d --no-build --wait --wait-timeout 90 || true
  fi
}
if ! "${compose[@]}" up -d --no-build --wait --wait-timeout 120; then
  rollback
  exit 1
fi
if ! python3 - <<'PY'
import json, urllib.request
with urllib.request.urlopen('http://127.0.0.1:8280/healthz', timeout=10) as response:
    data=json.load(response)
assert data.get('status')=='ok' and data.get('generation_configured'), 'generation is not configured'
PY
then
  rollback
  exit 1
fi
docker tag "slides-api:$release" slides-api:latest
if [[ -d /opt/slides/app && ! -L /opt/slides/app ]]; then
  mv /opt/slides/app "/opt/slides/legacy-$release"
fi
ln -s "$release_dir" "/opt/slides/app-$release"
mv -Tf "/opt/slides/app-$release" /opt/slides/app
rm -f "$release_dir/previous.env"
echo 'Local health OK; checking public access from the deploying machine next'
