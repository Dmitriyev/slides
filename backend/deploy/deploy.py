#!/usr/bin/env python3
"""Deploy slides using Screenshot AI's SSH inventory. Requires only Python stdlib."""
import argparse
import getpass
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[2]


def read_env(path):
    result = {}
    if path.exists():
        for line in path.read_text().splitlines():
            if not line.strip() or line.lstrip().startswith('#'):
                continue
            key, sep, value = line.partition('=')
            if not sep or not re.fullmatch(r'[A-Z][A-Z0-9_]*', key.strip()):
                raise ValueError(f'{path}: expected KEY=value')
            tokens = shlex.split(value, comments=True)
            if len(tokens) > 1:
                raise ValueError(f'{path}: quote values containing spaces')
            result[key.strip()] = tokens[0] if tokens else ''
    return result


def dotenv_value(value):
    # Compose env_file and Vite both support literal single-quoted values.
    if any(c in value for c in "'\r\n"):
        raise ValueError('Config values cannot contain quotes or newlines')
    return "'" + value + "'"


def write_private(path, text):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(dir=path.parent, prefix='.slides-')
    try:
        with os.fdopen(fd, 'w') as stream:
            stream.write(text)
        os.replace(name, path)
        path.chmod(0o600)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def inventory(path, pod, all_pods):
    config = json.loads(path.read_text())
    key = config.get('ssh_key') or ''
    if key:
        key = os.path.expandvars(os.path.expanduser(key))
        key = str((path.parent / key).resolve()) if not os.path.isabs(key) else key
        if not Path(key).is_file():
            raise ValueError(f'SSH key not found: {key}')
    rows = []
    seen = set()
    for item in config.get('pods', []):
        name = item.get('name', '')
        if not all_pods and name != pod:
            continue
        host = item.get('host', '')
        user = item.get('ssh_user') or config.get('ssh_user') or 'root'
        # IPv4 / DNS names only: avoid shell and scp option injection.
        if not re.fullmatch(r'[a-zA-Z0-9][a-zA-Z0-9.-]*', host) or host.startswith('CHANGE_ME'):
            raise ValueError(f'Invalid host for {name}')
        if not re.fullmatch(r'[a-zA-Z_][a-zA-Z0-9_-]*', user):
            raise ValueError(f'Invalid SSH user for {name}')
        if name in seen or host in {r['host'] for r in rows}:
            raise ValueError('Duplicate pod name or host')
        seen.add(name)
        rows.append({'name': name, 'host': host, 'user': user, 'key': key})
    if not rows:
        raise ValueError(f'No matching pods in {path}')
    return rows


def configuration(path, screenshot_config, interactive):
    config = read_env(path)
    config['OPENROUTER_API_KEY'] = os.getenv('OPENROUTER_API_KEY') or config.get('OPENROUTER_API_KEY', '')
    if not config['OPENROUTER_API_KEY'] and screenshot_config.exists():
        old = json.loads(screenshot_config.read_text())
        keys = (old.get('openrouter') or {}).get('api_keys') or []
        if keys:
            config['OPENROUTER_API_KEY'] = keys[0]
            print('==> OpenRouter key: existing Screenshot AI config (value hidden)')
    if not config['OPENROUTER_API_KEY'] and interactive:
        config['OPENROUTER_API_KEY'] = getpass.getpass('OpenRouter API key (hidden): ').strip()
    if not config['OPENROUTER_API_KEY'] or 'CHANGE_ME' in config['OPENROUTER_API_KEY']:
        raise ValueError('Set OPENROUTER_API_KEY, fill --env, or provide --screenshot-config')
    config['SLIDES_API_TOKEN'] = os.getenv('SLIDES_API_TOKEN') or config.get('SLIDES_API_TOKEN') or secrets.token_hex(32)
    if 'CHANGE_ME' in config['SLIDES_API_TOKEN'] or len(config['SLIDES_API_TOKEN']) < 24:
        raise ValueError('SLIDES_API_TOKEN must be at least 24 characters, without placeholders')
    config['SLIDES_MODEL'] = config.get('SLIDES_MODEL') or 'google/gemini-3.8-flash'
    config['CORS_ORIGINS'] = config.get('CORS_ORIGINS') or 'http://localhost:5173,http://127.0.0.1:5173'
    # Upload only Slides settings; do not copy unrelated application secrets.
    allowed = ('OPENROUTER_API_KEY', 'SLIDES_API_TOKEN', 'SLIDES_MODEL', 'CORS_ORIGINS')
    text = ''.join(f'{key}={dotenv_value(config[key])}\n' for key in allowed)
    write_private(path, text)
    return config, text


def frontend_config(path, host, token):
    lines = path.read_text().splitlines() if path.exists() else []
    values = {'SLIDES_API_URL': f'http://{host}:8280', 'SLIDES_API_TOKEN': token}
    output = []
    for line in lines:
        key = line.partition('=')[0].strip()
        if key not in values:
            output.append(line)
    output.extend(f'{key}={dotenv_value(value)}' for key, value in values.items())
    write_private(path, '\n'.join(output) + '\n')


def archive_sources(root, destination):
    # Explicit allowlist: local .env, hosts.json, .git, caches never enter the archive.
    paths = [root / 'backend' / p for p in ('Dockerfile', 'go.mod', 'schemas.json', 'prompt.txt', 'deploy/compose.yml')]
    paths += sorted((root / 'backend').glob('*.go'))
    with tarfile.open(destination, 'w:gz') as archive:
        for path in paths:
            if '__pycache__' in path.parts or path.is_symlink() or not path.is_file():
                if path in paths[:5]:
                    raise ValueError(f'Missing required source: {path}')
                continue
            archive.add(path, arcname=str(path.relative_to(root)), recursive=False)


def ssh_options(target):
    options = ['-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=accept-new', '-o', 'ConnectTimeout=15']
    if target['key']:
        options += ['-o', 'IdentitiesOnly=yes', '-i', target['key']]
    return options


def run(args, **kwargs):
    # Never print arguments/stdin: they may contain deployment configuration.
    subprocess.run(args, check=True, **kwargs)


def remote(target, script, *arguments):
    command = shlex.join(['bash', '-s', '--', *arguments])
    run(['ssh', *ssh_options(target), f"{target['user']}@{target['host']}", command], input=script, text=True)


def health(url, token=None):
    headers = {'Authorization': 'Bearer ' + token} if token else {}
    request = urllib.request.Request(url, headers=headers)
    with urllib.request.urlopen(request, timeout=8) as response:
        result = json.load(response)
    if result.get('status') != 'ok' or not result.get('generation_configured'):
        raise ValueError('API health failed or generation key is not configured')


def wait_health(host, timeout):
    deadline = time.monotonic() + timeout
    url = f'http://{host}:8280/healthz'
    last = None
    while time.monotonic() < deadline:
        try:
            health(url)
            print(f'OK {url}')
            return
        except (OSError, ValueError) as exc:
            last = exc
            time.sleep(2)
    raise ValueError(f'Public health check failed for {url}: {last}. Check TCP 8280 in Hetzner Cloud Firewall; deployment is not considered successful.')


def deploy(target, bundle, env_path, release, skip_build):
    print(f"==> {target['name']}: {target['user']}@{target['host']}", flush=True)
    remote(target, '''set -euo pipefail
command -v docker >/dev/null
docker compose version >/dev/null
mkdir -p /opt/slides/incoming
chmod 700 /opt/slides /opt/slides/incoming
''')
    incoming = f'/opt/slides/incoming/{release}'
    # Unique filenames prevent two uploads overwriting one another before the remote lock.
    run(['scp', *ssh_options(target), str(bundle), f"{target['user']}@{target['host']}:{incoming}.tgz"])
    run(['scp', *ssh_options(target), str(env_path), f"{target['user']}@{target['host']}:{incoming}.env"])
    remote(target, (ROOT / 'backend/deploy/remote.sh').read_text(), release, '1' if skip_build else '0')


def parser():
    result = argparse.ArgumentParser(description='Deploy Slides beside Screenshot AI; build remotely, configure and check public API.')
    result.add_argument('--hosts', type=Path, default=Path(os.getenv('SCREENSHOT_AI_HOSTS', str(ROOT.parent / 'screenshot_ai/backend/deploy/hosts.json'))))
    scope = result.add_mutually_exclusive_group()
    scope.add_argument('--pod', default=os.getenv('SLIDES_NODE', 'sai-api-a'))
    scope.add_argument('--all', action='store_true', help='Deploy all nodes from the existing inventory')
    result.add_argument('--env', type=Path, default=ROOT / 'backend/deploy/slides.env', help='Local private Slides env; created automatically')
    result.add_argument('--screenshot-config', type=Path, default=ROOT.parent / 'screenshot_ai/backend/deploy/config.prod.json')
    result.add_argument('--skip-build', action='store_true', help='Reuse the existing slides-api:latest image on each selected host')
    result.add_argument('--check', action='store_true', help='Validate inventory/key paths and list targets without SSH or writes')
    result.add_argument('--health-timeout', type=int, default=60)
    return result


def main(argv=None):
    args = parser().parse_args(argv)
    if not 1 <= args.health_timeout <= 600:
        raise ValueError('--health-timeout must be between 1 and 600 seconds')
    targets = inventory(args.hosts.expanduser().resolve(), args.pod, args.all)
    for target in targets:
        print(f"Target: {target['name']} ({target['user']}@{target['host']}:8280)")
    if args.check:
        print('Inventory OK. No changes made.')
        return
    for program in ('ssh', 'scp'):
        if not shutil.which(program):
            raise ValueError(f'{program} is required on PATH')
    config, text = configuration(args.env.expanduser(), args.screenshot_config.expanduser(), sys.stdin.isatty())
    release = time.strftime('%Y%m%d%H%M%S') + '-' + secrets.token_hex(4)
    with tempfile.TemporaryDirectory(prefix='slides-deploy-') as temp:
        bundle = Path(temp) / 'sources.tgz'
        env_path = Path(temp) / 'slides.env'
        write_private(env_path, text)
        archive_sources(ROOT, bundle)
        for target in targets:
            deploy(target, bundle, env_path, release, args.skip_build)
            wait_health(target['host'], args.health_timeout)
    frontend_config(ROOT / 'frontend/.env', targets[0]['host'], config['SLIDES_API_TOKEN'])
    print('==> deploy OK; frontend/.env configured. Start/restart: cd frontend && npm run dev')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as exc:
        # Do not print CalledProcessError args/stdin: keep secrets out of logs.
        print(f'Deploy failed: {exc if not isinstance(exc, subprocess.CalledProcessError) else "SSH/SCP command failed; see output above"}', file=sys.stderr)
        sys.exit(1)
