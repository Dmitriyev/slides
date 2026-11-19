import io
import json
from unittest.mock import AsyncMock
import pytest
from fastapi.testclient import TestClient
from app import main

@pytest.fixture
def client(monkeypatch):
    monkeypatch.setenv('SLIDES_API_TOKEN', 'test-secret')
    return TestClient(main.app, headers={'Authorization': 'Bearer test-secret'})

def content(title='Generated', count=1):
    return main.Content(title='Deck', slides=[main.Slide(title=title, paragraphs=['Changed text']) for _ in range(count)])

def test_provider_schema_drops_limits_gemini_rejects():
    schema = json.dumps(main.provider_schema(main.Content))
    for limit in ("minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems"):
        assert f'"{limit}"' not in schema
    assert "title" in schema and "slides" in schema

def test_unauthorized():
    with TestClient(main.app) as c:
        assert c.post('/api/v1/presentations/rewrite', json={}).status_code in (401, 503)

def test_empty_upload(client):
    assert client.post('/api/v1/presentations/generate').status_code == 422

def test_upload_and_generate(client, monkeypatch):
    mock = AsyncMock(return_value=content())
    monkeypatch.setattr(main, 'generate', mock)
    r = client.post('/api/v1/presentations/generate', data={'slide_count': '1'}, files={'files': ('source.txt', b'Revenue 42', 'text/plain')})
    assert r.status_code == 200
    assert 'Revenue 42' in r.json()['source']
    assert mock.call_args.args[0]['source'].endswith('Revenue 42')

def test_bad_file_and_settings(client):
    assert client.post('/api/v1/presentations/generate', files={'files': ('bad.exe', b'data')}).status_code == 415
    assert client.post('/api/v1/presentations/generate', data={'description': 'Hello', 'settings': '{"detail":99}'}).status_code == 422

def test_chart_requires_exact_source_number():
    chart = main.Chart(title='Data', unit='USD', data=[main.Datum(label='A', value=42, evidence='A: 42'), main.Datum(label='B', value=44, evidence='B: 440')])
    s = main.Slide(title='Test', chart=chart)
    main.clean_chart(s, 'A: 42; B: 440', main.Settings())
    assert s.chart is None
    chart.data[1].value = 440
    s.chart = chart
    main.clean_chart(s, 'A: 42; B: 440', main.Settings())
    assert s.chart is not None
    main.clean_chart(s, 'A: 42; B: 440', main.Settings(charts=0))
    assert s.chart is None

def test_scope_lock_and_manual_edits(client, monkeypatch):
    a = main.Slide(id='a', title='Manual title', paragraphs=['Exactly this'], locked=True)
    b = main.Slide(id='b', title='Untouched')
    deck = main.Deck(title='Deck', source='Material', slides=[a,b])
    monkeypatch.setattr(main, 'generate', AsyncMock(return_value=content()))
    r = client.post('/api/v1/presentations/rewrite', json={'deck':deck.model_dump(),'scope':'slide','slide_id':'a','settings': {'detail':2}})
    assert r.status_code == 200
    result = r.json()['slides']
    assert result[0]['title'] == 'Manual title'
    assert result[0]['paragraphs'] == ['Exactly this']
    assert result[0]['settings']['detail'] == 1
    assert result[1] == b.model_dump()

def test_deck_scope_and_failure_atomicity(client, monkeypatch):
    deck = main.Deck(title='Deck', source='Material', slides=[main.Slide(title='One'),main.Slide(title='Two')])
    mock = AsyncMock(return_value=content(count=2))
    monkeypatch.setattr(main, 'generate', mock)
    payload = {'deck':deck.model_dump(),'scope':'deck','settings': {'detail':2}}
    r = client.post('/api/v1/presentations/rewrite', json=payload)
    assert r.status_code == 200
    assert [s['id'] for s in r.json()['slides']] == [s.id for s in deck.slides]
    assert all(s['settings']['detail'] == 2 for s in r.json()['slides'])
    mock.return_value = content(count=1)
    assert client.post('/api/v1/presentations/rewrite', json=payload).status_code == 502
    assert deck.slides[0].title == 'One'

def test_invalid_target(client):
    deck = main.Deck(title='Deck', source='Material', slides=[main.Slide(title='One')])
    assert client.post('/api/v1/presentations/rewrite', json={'deck':deck.model_dump(),'scope':'slide','slide_id':'missing','settings':{}}).status_code == 422
