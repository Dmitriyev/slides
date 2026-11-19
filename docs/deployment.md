# Деплой, как в Screenshot AI

Запускается с Mac, где уже настроен SSH Screenshot AI. Локальный Docker не нужен: образ собирается на сервере без эмуляции Apple Silicon.

```bash
cd slides
bash scripts/deploy-prod.sh
```

По умолчанию скрипт берёт `../screenshot_ai/backend/deploy/hosts.json`, пользователя и SSH-ключ из него и разворачивает Slides на `sai-api-a`. Если репозитории не лежат рядом:

```bash
bash scripts/deploy-prod.sh --hosts ~/Code/screenshot_ai/backend/deploy/hosts.json
```

## Что выполняется автоматически

1. Проверяет inventory и путь к SSH-ключу.
2. Читает OpenRouter key из локального `backend/deploy/slides.env`, переменной `OPENROUTER_API_KEY` или `../screenshot_ai/backend/deploy/config.prod.json` (`openrouter.api_keys[0]`). Если ключа нет, запрашивает скрытым вводом в терминале. Остальные секреты Screenshot AI не копирует.
3. Генерирует отдельный `SLIDES_API_TOKEN`; сохраняет локальную конфигурацию с правами 600. При повторном запуске токен сохраняется.
4. Упаковывает только разрешённые исходники backend. Загружает их и Slides env по SSH; локальные hosts/config и прочие секреты в архив не входят.
5. Собирает образ на сервере до замены работающего контейнера. Хранит релизы в `/opt/slides/releases`, конфигурацию — `/opt/slides/slides.env`; ставит блокировку от параллельного деплоя.
6. Запускает отдельный Compose project `slides`, публикует `0.0.0.0:8280`, проверяет локальный health и наличие ключа генерации. При неудачном запуске пытается восстановить предыдущий релиз и env.
7. Проверяет публичный `http://IP:8280/healthz` с Mac. При недоступном порте завершает работу с ошибкой, а не сообщает об успешном деплое.
8. После успешных проверок записывает IP и токен в `frontend/.env`, сохраняя остальные переменные. Для работы с обновлённой конфигурацией перезапустить Vite.

Конфиг и токен не коммитятся. Сервер должен предоставлять Docker Compose v2 с `--wait`, Python 3, `flock` и права пользователя на Docker и `/opt/slides`. При зашифрованном SSH-ключе сначала загрузить его в macOS ssh-agent (`ssh-add`).

## Параметры

```bash
# Проверка inventory без SSH и изменений
bash scripts/deploy-prod.sh --check

# Выбор узла, как --pod у Screenshot AI
bash scripts/deploy-prod.sh --pod sai-api-b

# Оба узла из inventory
bash scripts/deploy-prod.sh --all

# Своя конфигурация
bash scripts/deploy-prod.sh --env /path/to/slides.env --screenshot-config /path/to/config.prod.json

# Перезапустить существующий образ без пересборки
bash scripts/deploy-prod.sh --skip-build
```

Поддерживаются также `SCREENSHOT_AI_HOSTS` и `SLIDES_NODE`. Старый entry point `bash backend/deploy/deploy.sh` работает с теми же параметрами. `--all` проверяет каждый узел, а локальный фронт направляет на первый узел; балансировщик пока не настраивается.

## Внешний порт

Первый узел: `sai-api-a`, `128.140.108.182` (private `10.0.0.3`). Второй: `sai-api-b`, `128.140.108.247`. Порт Slides — `8280`; Screenshot AI / Recipe контейнеры и порты не меняются.

Docker-порт публикуется скриптом. Если VM защищена Hetzner Cloud Firewall, добавить inbound TCP 8280 для публичного IP своего Mac. Облачный firewall скрипт не меняет: у SSH inventory нет токена Hetzner API. Учитывать Docker forwarding при настройке host firewall; одного UFW может быть недостаточно для ограничения опубликованного порта.

HTTP по IP — временная схема без TLS. Для публичного запуска нужен HTTPS. Healthcheck проверяет конфигурацию и процесс, но не выполняет платный запрос к модели; реальную генерацию проверить через фронт.

## Локальный фронт после деплоя

```bash
cd frontend
npm ci
npm run dev
```

Открыть http://localhost:5173. Адрес и токен уже записаны скриптом. Токен передаёт Vite proxy; он не попадает в браузерный bundle.

## Эксплуатация

```bash
docker compose -f /opt/slides/app/backend/deploy/compose.yml logs --tail=100 api
docker compose -f /opt/slides/app/backend/deploy/compose.yml ps
```

Для ручных команд текущий образ уже указан в работающем контейнере; alias `slides-api:latest` обновляется после успешного локального healthcheck. Старые релизы и образы автоматически не удаляются, чтобы оставалась возможность отката.

## Статус в этой среде

Скрипт проверен локально с изолированными SSH/SCP-заглушками. Реальный SSH-деплой и внешний healthcheck здесь не выполнялись: inventory и SSH-ключ отсутствуют.
