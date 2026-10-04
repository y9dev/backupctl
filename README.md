# backupctl

Утилита резервного копирования для Linux-серверов. Ставится **только на backup-сервер** и забирает данные с production-машин по SSH либо делает бэкап непосредственно на своей машине (`local: true`): файлы, PostgreSQL (`pg_dump`, в том числе из Docker-контейнера) и вывод произвольных команд. Расписание, хранение истории, ротация старых копий и восстановление — из коробки, с TUI и CLI.

Требования: Go 1.24+ для сборки (или готовый бинарь), для SSH-режима — SSH-доступ с backup-сервера на production, `pg_dump`/`pg_restore` там, где бэкапится PostgreSQL (в официальном Docker-образе postgres уже есть); для локального режима — те же бинари (`tar`, `pg_dump`, `docker`) на машине с `backupctl`.

## Быстрый старт

```bash
# 1. Сборка и установка
go build -o backupctl ./cmd/backupctl
sudo install -m 755 backupctl /usr/local/bin/backupctl

# 2. Первый запуск — setup wizard создаст конфиг
backupctl
# На Linux defaults: storage /var/backups, БД /var/lib/backupctl/backup.db
# На Windows: .\backups\storage, .\backups\backup.db

# 3. Проверка SSH и доверия host key
backupctl server test prod-1
# При неизвестном ключе покажется fingerprint и вопрос Trust this host? [y/N]

# 4. Первый бэкап вручную
backupctl job run prod-1 postgres

# 5. Проверка и восстановление
backupctl backup list
backupctl verify 3
backupctl restore 3 --database mydb_copy
```

## Установка

```bash
go install ./cmd/backupctl

# Кросс-сборка:
GOOS=linux GOARCH=amd64 go build -o backupctl ./cmd/backupctl
GOOS=linux GOARCH=arm64 go build -o backupctl ./cmd/backupctl

# Версия в бинаре:
go build -ldflags "-X backupctl/internal/version.Version=1.0.0" -o backupctl ./cmd/backupctl
backupctl version
```

Фоновая работа через systemd (`systemd/backupctl.service`):

```bash
sudo cp systemd/backupctl.service /etc/systemd/system/
sudo systemctl enable --now backupctl
```

`backupctl daemon` — то же вручную. Второй экземпляр не запустится (PID-файл).

## Файлы и переменные окружения

| Что | По умолчанию | Переменная |
|---|---|---|
| Конфиг | `/etc/backupctl/config.yaml` | `BACKUPCTL_CONFIG` |
| Секреты (0600) | `/etc/backupctl/secrets.yaml` | `BACKUPCTL_SECRETS` |
| known_hosts | `~/.ssh/known_hosts` | `BACKUPCTL_KNOWN_HOSTS` |
| PID-файл daemon'а | `/var/run/backupctl.pid` | `BACKUPCTL_PID` |

Пример конфига: `configs/example.yaml`. Wizard (`backupctl`, первый запуск без конфига) проведёт по шагам: storage → БД → retention → сервер → проверка SSH → первый job → расписание.

## Конфигурация

### Сервер

```yaml
servers:
  - name: prod-1
    host: 192.168.1.10
    port: 22
    username: backup
    ssh_key: /root/.ssh/id_ed25519   # файл должен существовать; ~ раскрывается
    enabled: true
    jobs: [...]
```

### Локальный сервер (без SSH)

Бэкап выполняется непосредственно на машине с `backupctl`, все типы job'ов (`directory`, `postgresql`, `command`) сохраняются:

```yaml
servers:
  - name: local
    local: true
    enabled: true
    jobs: [...]   # тот же формат, что и для SSH-серверов
```

При `local: true` поля `host`/`username`/`ssh_key` не требуются и игнорируются. Команды выполняются локально: нужен установленный `tar` (директории), `pg_dump`/`pg_restore` (PostgreSQL, в т.ч. `docker exec` для `container:`) и сама команда для `command`-job'ов. `server test local` всегда успешен (SSH не проверяется), `server list` показывает такого сервера как `local`.

SSH: только key auth. Подготовка:

```bash
ssh-keygen -t ed25519 -f /root/.ssh/id_ed25519
ssh-copy-id backup@prod-1
backupctl server test prod-1   # y на вопрос про fingerprint
```

### Job'ы

Каждый сервер имеет независимые job'ы. Общие поля:

```yaml
jobs:
  - name: app
    type: directory              # directory | postgresql | command
    enabled: true
    schedule: "0 * * * *"        # cron; в TUI есть пресеты
    timeout: 2h                  # по истечении — отмена
    config: {...}                # настройки провайдера
```

Пресеты расписания в TUI: каждые 15/30 минут, час, 2/6/12 часов, день, свой cron.

**Директория** — tar на удалённом хосте стримится по SSH и жмётся в `tar.zst` локально:

```yaml
config:
  source: /opt/app
  compression: {type: zstd, level: 3}
  exclude: [/opt/app/node_modules, /opt/app/.cache]
  # follow_symlinks: false
```

**PostgreSQL** — `pg_dump -Fc`, stdout по SSH в `.dump`. Пароль только через секреты:

```yaml
config:
  host: 127.0.0.1
  port: 5432
  database: production
  username: backup
  password_secret: postgres_prod   # ключ из secrets.yaml, не сам пароль!
```

```yaml
# secrets.yaml (0600)
postgres_prod: "s3cret"
```

**PostgreSQL в Docker-контейнере** — добавьте `container`, и `pg_dump` выполнится внутри контейнера через `docker exec`:

```yaml
config:
  container: pg-production   # имя контейнера на удалённом хосте
  # docker: /usr/bin/docker  # по умолчанию "docker"
  database: production
  username: backup
  password_secret: postgres_prod
```

Требования к хосту: установленный `docker`, SSH-пользователь с доступом к `docker exec`.

**Произвольная команда** — stdout команды на удалённом хосте пишется в файл:

```yaml
config:
  command: [/usr/local/bin/create-backup.sh]
  output: {type: stdout, filename: redis.rdb}
```

### Retention (ротация)

```yaml
retention:
  hourly: {count: 10, window: 12h}
  weekly: {count: 1, window: 168h}
  monthly: {count: 2, window: 720h}
```

Категории объединяются (GFS): копия, нужная сразу нескольким правилам, удаляется только когда не нужна ни одному. `hourly` хранит Count newest внутри окна; `weekly` — newest по одному на каждый календарный день (UTC) внутри своего окна, пропуская hourly; `monthly` — newest по одному на календарный месяц, пропуская hourly и weekly. Работает по фактическим датам файлов, простой daemon'а не страшен. Применяется автоматически после каждого успешного бэкапа; вручную — `backupctl retention run`. Бэкапы удалённых job'ов хранятся.

Хранилище:

```yaml
storage:
  path: /var/backups
  min_free_space: 10GB   # при меньшем остатке job'ы не стартуют
```

Файлы лежат как `<storage>/<server>/<job>/YYYY-MM-DD_HH-mm-ss.<ext>`, пишутся атомарно (`.tmp` + rename) с SHA-256 в SQLite.

## Команды

```bash
backupctl                  # TUI (то же: backupctl tui)
backupctl daemon           # scheduler
backupctl version

backupctl server list
backupctl server test prod-1 [--trust]   # --trust — без вопроса (автоматизация)
backupctl server remove prod-1           # бэкапы НЕ удаляются

backupctl job list
backupctl job run prod-1 postgres        # один job
backupctl run prod-1 postgres            # алиас
backupctl run --server prod-1            # все job'ы сервера

backupctl backup list
backupctl backup verify 3                # алиас: backupctl verify 3
backupctl backup restore 3 --dest /opt/app --database mydb
backupctl restore 3 --database mydb --yes  # алиасы + пропуск подтверждения

backupctl retention run
backupctl config validate
```

Коды выхода: `0` успех, `1` общая ошибка, `2` конфиг, `3` соединение, `4` бэкап, `5` restore, `6` verify, `7` уже запущен.

## Восстановление

```bash
backupctl backup list      # найти ID
backupctl verify 3         # сверить SHA-256 (для postgres ещё и pg_restore --list)
backupctl restore 3 --database mydb_copy   # сначала на копию!
```

`restore` спросит подтверждение (кроме `--yes`). Для директорий укажите `--dest` (по умолчанию — исходный `source`, перезапись только с вашего согласия). PostgreSQL льётся через `pg_restore --clean --if-exists` — целевая БД перезаписывается. В TUI: `Backup History` → запись → `Restore`.

## Daemon и логи

```bash
backupctl daemon
sudo journalctl -u backupctl -f
```

Логи — структурированный slog в stderr (`--log-level DEBUG|INFO|WARN|ERROR`). Scheduler:cron, не более `scheduler.max_parallel_jobs` одновременно, один job дважды не стартует, retry transient-ошибок (`retry: {attempts: 3, delay: 30s}`), SIGINT/SIGTERM — graceful shutdown. После падения зависшие `running` помечаются `failed`, `.tmp` чистятся.

Timestamps в SQLite — UTC, в TUI — локальное время. Часовой пояс cron: системный или `timezone: Asia/Almaty`.

## Проверка конфига

```bash
backupctl config validate
```

Проверяет YAML, обязательные поля, существование SSH-ключей, cron, retention, настройки провайдеров, дубли серверов/job'ов:

```text
Configuration valid.

Servers: 3
Jobs: 8
Storage: /var/backups
Database: /var/lib/backupctl/backup.db
```

## Для разработчиков: новый провайдер

```go
type MyProvider struct{}
func (MyProvider) Name() string { return "mysql" }
func (MyProvider) Backup(ctx, server, job, dest) (BackupResult, error) { ... }
func (MyProvider) Restore(ctx, server, job, b, opts) error { ... }
func init() { backup.Register(MyProvider{}) }
```

Scheduler находит провайдер через `backup.Get(job.Type)`. Тесты: `go test ./...`. Интеграционные (postgres + sshd): `docker compose up -d`, `go test -tags integration ./internal/backup/`.

## Troubleshooting

| Симптом | Что делать |
|---|---|
| `No configuration found` | Запустить wizard (`backupctl`) или задать `BACKUPCTL_CONFIG` |
| `ssh_key ...: not found` | Указать существующий файл; `~` раскрывается, на Windows — полный путь |
| `key is unknown` | Ответить `y` на вопрос fingerprint в `server test` |
| `unable to authenticate` | Публичный ключ не в `authorized_keys` пользователя на сервере |
| `pg_dump failed`, код 1 | Хост/user/`password_secret`, `pg_hba`; в Docker — имя контейнера (`docker ps`) |
| Пустой бэкап | Нет прав на чтение source / пустая БД |
| `insufficient disk space` | Чистить storage или уменьшить `min_free_space` |
| `already running` | Daemon уже запущен (PID-файл) |
| `checksum mismatch` при verify | Файл повреждён — восстанавливать из другой копии |
