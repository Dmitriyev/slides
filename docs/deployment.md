# Развёртывание рядом со Screenshot AI

Целевой первый узел: `sai-api-a`, `128.140.108.182` (private `10.0.0.3`). Второй: `sai-api-b`, `128.140.108.247`. Отдельный Compose project `slides`, каталог `/opt/slides`, порт `8280`. Существующие Screenshot AI / Recipe порты и контейнеры не изменяются.

## Статус

Код и deploy-конфигурация подготовлены. Развёртывание и внешний доступ ещё не выполнены: в рабочей среде нет SSH-ключа и gitignored inventory Screenshot AI. Адрес `http://128.140.108.182:8280` — целевой, не подтверждённый production endpoint.

## Подготовка на сервере

Подключиться обычным SSH-доступом Screenshot AI. Убедиться, что 8280 свободен (`ss -ltn 'sport = :8280'`). Создать `/opt/slides/slides.env` с правами 600, используя `backend/.env.example`. Заполнить `OPENROUTER_API_KEY` и новый `SLIDES_API_TOKEN` (например, `openssl rand -hex 32`). Ключи не коммитить. Не копировать весь env другого сервиса.

На Mac, где есть существующий Screenshot AI SSH inventory:

```bash
cd slides
export SCREENSHOT_AI_HOSTS=../screenshot_ai/backend/deploy/hosts.json
bash backend/deploy/deploy.sh
```

Скрипт использует inventory, ключ и пользователя Screenshot AI, переносит только закоммиченный backend, собирает отдельный контейнер и проверяет его health. Требуются Docker Compose v2+ и права на `/opt/slides` / Docker. По умолчанию один узел. Для второго `SLIDES_NODE=sai-api-b`.

## Внешний порт

В Hetzner Cloud Firewall, привязанном к выбранному API VM, добавить inbound TCP 8280 (для разработки можно ограничить домашним публичным IP). Проверить host firewall; при использовании Docker учитывать Docker forwarding: одного правила UFW недостаточно для ограничения опубликованного Docker-порта. Ограничение лучше задать в cloud firewall или DOCKER-USER chain. Не менять существующие правила 8080/8180/9090/9190.

Compose публикует `0.0.0.0:8280`; cloud firewall должен пропустить трафик. Это не выполняется скриптом автоматически, поскольку доступ к Hetzner API не настроен.

С локального Mac:

```bash
curl --fail http://128.140.108.182:8280/healthz
```

Проверить реальную генерацию через локальный фронт, а не только healthcheck. В `frontend/.env` указать серверный `SLIDES_API_TOKEN`. Он передаётся локальным Vite proxy, а не в браузерном bundle. HTTP по IP — временная схема: токен и материалы передаются до сервера без TLS. Для публичного запуска нужен HTTPS.

## Эксплуатация

```bash
docker compose -f /opt/slides/app/backend/deploy/compose.yml logs --tail=100 api
docker compose -f /opt/slides/app/backend/deploy/compose.yml ps
```

Перезапуск после изменения env: `docker compose -f /opt/slides/app/backend/deploy/compose.yml up -d --force-recreate --wait`.
Контейнер ограничен 768 МБ / 1 CPU и работает без root. API закрыт bearer-токеном; максимум 3 одновременных вызова провайдера на процесс. Полноценные пользовательские аккаунты и billing пока не реализованы.
