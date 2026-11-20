import importlib.util
import json
import os
from pathlib import Path
import tarfile

import pytest

spec = importlib.util.spec_from_file_location('slides_deploy', Path(__file__).parents[1] / 'deploy/deploy.py')
deploy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(deploy)


def test_inventory_selects_pod_and_relative_key(tmp_path):
    (tmp_path / 'key').write_text('not a real key')
    hosts = tmp_path / 'hosts.json'
    hosts.write_text(json.dumps({'ssh_user':'root','ssh_key':'key','pods':[
        {'name':'sai-api-a','host':'192.0.2.1'},
        {'name':'sai-api-b','host':'192.0.2.2','ssh_user':'deployer'}]}))
    assert deploy.inventory(hosts, 'sai-api-b', False)[0]['user'] == 'deployer'
    assert deploy.inventory(hosts, '', True)[0]['key'] == str(tmp_path / 'key')
    assert len(deploy.inventory(hosts, '', True)) == 2
    with pytest.raises(ValueError):
        deploy.inventory(hosts, 'missing', False)


def test_invalid_host_rejected(tmp_path):
    hosts = tmp_path / 'hosts.json'
    hosts.write_text(json.dumps({'pods':[{'name':'sai-api-a','host':'host; touch /tmp/unsafe'}]}))
    with pytest.raises(ValueError):
        deploy.inventory(hosts, 'sai-api-a', False)


def test_configuration_reuses_key_and_persists_token(tmp_path, monkeypatch):
    monkeypatch.delenv('OPENROUTER_API_KEY', raising=False)
    monkeypatch.delenv('SLIDES_API_TOKEN', raising=False)
    old = tmp_path / 'config.prod.json'
    old.write_text(json.dumps({'openrouter':{'api_keys':['private-test-key']}, 'jwt_secret':'do-not-copy'}))
    env = tmp_path / 'slides.env'
    first, text = deploy.configuration(env, old, False)
    second, _ = deploy.configuration(env, old, False)
    assert first['SLIDES_API_TOKEN'] == second['SLIDES_API_TOKEN']
    assert first['OPENROUTER_API_KEY'] == 'private-test-key'
    assert 'do-not-copy' not in text
    assert env.stat().st_mode & 0o777 == 0o600
    assert deploy.read_env(env)['OPENROUTER_API_KEY'] == 'private-test-key'


def test_missing_key_fails_before_upload(tmp_path, monkeypatch):
    monkeypatch.delenv('OPENROUTER_API_KEY', raising=False)
    with pytest.raises(ValueError, match='OPENROUTER_API_KEY'):
        deploy.configuration(tmp_path/'slides.env', tmp_path/'missing.json', False)
    assert not (tmp_path/'slides.env').exists()


def test_archive_excludes_credentials(tmp_path):
    root = tmp_path/'repo'
    for name in ('backend/Dockerfile','backend/go.mod','backend/schemas.json','backend/prompt.txt','backend/deploy/compose.yml','backend/main.go','backend/deploy/slides.env','backend/.env','backend/untracked/secret.go','backend/hosts.json'):
        p=root/name; p.parent.mkdir(parents=True, exist_ok=True); p.write_text(name)
    archive = tmp_path/'sources.tgz'
    deploy.archive_sources(root,archive)
    with tarfile.open(archive) as tar:
        assert set(tar.getnames()) == {'backend/Dockerfile','backend/go.mod','backend/schemas.json','backend/prompt.txt','backend/deploy/compose.yml','backend/main.go'}


def test_frontend_config_preserves_other_values(tmp_path):
    path=tmp_path/'.env'
    path.write_text('# A comment\nOTHER=value\nSLIDES_API_URL=old\nSLIDES_API_TOKEN=old-token\n')
    deploy.frontend_config(path, '192.0.2.1', 'new-token')
    parsed=deploy.read_env(path)
    assert parsed['OTHER']=='value'
    assert parsed['SLIDES_API_TOKEN']=='new-token'
    assert parsed['SLIDES_API_URL']=='http://192.0.2.1:8280'
    assert path.read_text().count('SLIDES_API_TOKEN=') == 1


def test_uploads_and_build_run_without_secret_arguments(tmp_path, monkeypatch):
    calls=[]
    monkeypatch.setattr(deploy,'run',lambda args,**kwargs:calls.append((args,kwargs)))
    target={'name':'sai-api-a','host':'192.0.2.1','user':'root','key':''}
    deploy.deploy(target,tmp_path/'sources.tgz',tmp_path/'private.env','20261119170000-abcdef12',False)
    assert [args[0] for args,_ in calls] == ['ssh','scp','scp','ssh']
    assert '20261119170000-abcdef12 0' in calls[-1][0][-1]
    assert 'SLIDES_ENV_FILE="$incoming.env"' in calls[-1][1]['input']
    assert 'flock -n 9' in calls[-1][1]['input']


def test_check_does_not_write_or_connect(tmp_path, monkeypatch):
    hosts=tmp_path/'hosts.json'
    hosts.write_text(json.dumps({'pods':[{'name':'sai-api-a','host':'192.0.2.1'}]}))
    monkeypatch.setattr(deploy,'run',lambda *a,**kw:pytest.fail('No SSH in check mode'))
    env=tmp_path/'slides.env'
    deploy.main(['--hosts',str(hosts),'--env',str(env),'--check'])
    assert not env.exists()
