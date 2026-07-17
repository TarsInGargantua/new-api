# Request Log Independent Database Deployment

This deployment splits request-log storage from the main application database:

- `logs` remains in the existing main/log database.
- `api_request_logs` and `api_request_log_items` use `REQUEST_LOG_SQL_DSN`.
- The standalone viewer reads turn data and has narrowly scoped write access to persistent export batch tables.

## Main Service Env

Set these on the main new-api deployment:

```env
API_REQUEST_LOG_ENABLED=true
API_REQUEST_LOG_CAPTURE_RESPONSE=true
API_REQUEST_LOG_REDACT_SECRETS=true
API_REQUEST_LOG_ASYNC_WRITE=true
API_REQUEST_LOG_QUEUE_SIZE=128
API_REQUEST_LOG_WORKERS=2
API_REQUEST_LOG_MAX_BODY_BYTES=4194304
API_REQUEST_LOG_MAX_ITEM_BYTES=1048576
API_REQUEST_LOG_MAX_QUEUE_BYTES=67108864
CALL_LOG_EXCLUDED_USERNAMES=ryan
REQUEST_LOG_SQL_DSN=newapi_request_log_app:<password>@tcp(<REMOTE_PUBLIC_HOST>:3306)/newapi_request_logs?charset=utf8mb4&parseTime=true&loc=Local
```

If the public router maps MySQL to a non-3306 external port, use that external host and port in `REQUEST_LOG_SQL_DSN`, for example `@tcp(<PUBLIC_MYSQL_HOST>:9008)`.

Do not change `SQL_DSN` or `LOG_SQL_DSN` for this migration unless the main application database is also being moved.

For high-concurrency or very large requests:

- `API_REQUEST_LOG_ASYNC_WRITE=true` writes parent rows synchronously and queues item inserts in background workers.
- `API_REQUEST_LOG_MAX_BODY_BYTES` limits captured request/response bytes before parsing. `0` disables body capture.
- `API_REQUEST_LOG_MAX_ITEM_BYTES` truncates each parsed item before database insert. `0` disables item truncation.
- `API_REQUEST_LOG_MAX_QUEUE_BYTES` caps queued item payload bytes in process memory. `0` disables this byte cap.
- Increase `API_REQUEST_LOG_WORKERS` only if MySQL can handle the extra insert concurrency.
- `CALL_LOG_EXCLUDED_USERNAMES` is a case-insensitive comma-separated list. Matching users do not create consume/error/task rows in `logs` or rows in `api_request_logs`; existing rows and non-call management/top-up logs are unchanged.

## Schema

The request-log database contains:

- `api_request_logs`: one row per gateway request, with envelope, index, usage, status, size, parse status, and migration source fields.
- `api_request_log_items`: ordered training items for system/user/assistant messages, reasoning, tool specs, tool calls, tool results, errors, and raw unparsed fallback content.
- `api_request_log_turns`: one materialized row per session turn, including exact/inferred/unknown attribution and completion state.
- `api_request_log_turn_requests`: request-to-turn membership and per-request usage data.
- `api_request_log_turn_items`: canonical turn-item mappings that reference `api_request_log_items`; item content is not copied.
- `api_request_log_export_batches` and `api_request_log_export_members`: immutable, globally deduplicated export membership and artifact state.
- `api_request_log_organizer_states`: persistent high-water marks for resumable historical turn organization.

The parent table intentionally does not store `request_body`, `response_body`, or `metadata`. Raw bodies are only used as parser input. If parsing fails, the unparsed text is written once as an item with `item_type=raw_unparsed`.

## Initialize Remote Database

On the remote Ubuntu host:

```bash
sudo apt-get update
sudo apt-get install -y mysql-server
sudo systemctl enable --now mysql
```

Create the database and users:

```sql
CREATE DATABASE IF NOT EXISTS newapi_request_logs CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS 'newapi_request_log_app'@'%' IDENTIFIED BY '<strong-password>';
CREATE USER IF NOT EXISTS 'request_log_viewer'@'localhost' IDENTIFIED BY '<strong-password>';
GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER, INDEX, REFERENCES ON newapi_request_logs.* TO 'newapi_request_log_app'@'%';
REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'request_log_viewer'@'localhost';
GRANT SELECT ON newapi_request_logs.api_request_log_turns TO 'request_log_viewer'@'localhost';
GRANT SELECT ON newapi_request_logs.api_request_log_turn_requests TO 'request_log_viewer'@'localhost';
GRANT SELECT ON newapi_request_logs.api_request_log_turn_items TO 'request_log_viewer'@'localhost';
GRANT SELECT ON newapi_request_logs.api_request_log_items TO 'request_log_viewer'@'localhost';
GRANT SELECT, INSERT, UPDATE ON newapi_request_logs.api_request_log_export_batches TO 'request_log_viewer'@'localhost';
GRANT SELECT, INSERT, UPDATE ON newapi_request_logs.api_request_log_export_members TO 'request_log_viewer'@'localhost';
FLUSH PRIVILEGES;
```

Initialize tables:

```bash
REQUEST_LOG_SQL_DSN='newapi_request_log_app:<password>@tcp(127.0.0.1:3306)/newapi_request_logs?charset=utf8mb4&parseTime=true&loc=Local' \
  ./request-log-migrate -init-only
```

## Historical Migration

Run a dry run first:

```bash
SOURCE_REQUEST_LOG_SQL_DSN='<old-zeabur-dsn>' \
REQUEST_LOG_SQL_DSN='<new-request-log-dsn>' \
  ./request-log-migrate -dry-run -batch-size 500
```

For large legacy tables, count first and start with a bounded sample:

```bash
SOURCE_REQUEST_LOG_SQL_DSN='<old-zeabur-dsn>' \
REQUEST_LOG_SQL_DSN='<new-request-log-dsn>' \
  ./request-log-migrate -count-only

SOURCE_REQUEST_LOG_SQL_DSN='<old-zeabur-dsn>' \
REQUEST_LOG_SQL_DSN='<new-request-log-dsn>' \
  ./request-log-migrate -dry-run -batch-size 1 -max-rows 20
```

Then migrate:

```bash
SOURCE_REQUEST_LOG_SQL_DSN='<old-zeabur-dsn>' \
REQUEST_LOG_SQL_DSN='<new-request-log-dsn>' \
  ./request-log-migrate -batch-size 500
```

Use `-after-id <legacy id>` to resume from a known legacy id. The migrator uses `source=legacy_api_request_logs` and `source_id=<old id>` for idempotent updates. It also hydrates usage fields from the old `logs` table by `usage_log_id` when available.

## Standalone Viewer

The viewer serves both static UI and APIs on `:3001`. It can stay private on the request-log host or an internal network; a public HTTP endpoint is not required for the main service to write request-log data.

Required env:

```env
REQUEST_LOG_VIEWER_ADDR=:3001
REQUEST_LOG_VIEWER_USERNAME=admin
REQUEST_LOG_VIEWER_PASSWORD=<strong-password>
REQUEST_LOG_VIEWER_SQL_DSN=request_log_viewer:<password>@tcp(127.0.0.1:3306)/newapi_request_logs?charset=utf8mb4&parseTime=true&loc=Local
REQUEST_LOG_VIEWER_EXPORT_DIR=/home/rwkv/request-log/exports
```

Example systemd unit:

```ini
[Unit]
Description=Request Log Viewer
After=network.target mysql.service

[Service]
WorkingDirectory=/home/rwkv/request-log
EnvironmentFile=/home/rwkv/request-log/.env
ExecStart=/home/rwkv/request-log/bin/request-log-viewer
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

Viewer endpoints:

- `GET /api/status`
- `GET /api/turns?p=1&page_size=100`
- `GET /api/turns/:id`
- `GET /api/export-preview`
- `POST /api/export-batches`
- `GET /api/export-batches`
- `GET /api/export-batches/:tag/download`
- `POST /api/export-batches/:tag/retry`

Turn time filters use `completed_at >= start_timestamp AND completed_at < end_timestamp`. Export batches ignore list pagination, default to exact completed turns, and globally exclude turns claimed by earlier batches. Add `include_inferred=true` only when inferred completed turns are intentionally included. Encrypted reasoning is never exported. The legacy `/api/export.jsonl` endpoint returns `410 Gone`.

## Historical Turn Organizer

Initialize the materialized tables with the migration-capable application account:

```bash
REQUEST_LOG_SQL_DSN='<request-log-app-dsn>' ./request-log-organize -init-only
```

Run a bounded dry run before processing the full table:

```bash
REQUEST_LOG_SQL_DSN='<request-log-app-dsn>' \
  ./request-log-organize -dry-run -batch-size 100 -max-rows 1000 -lag-seconds 300
```

Then run the idempotent organizer in small keyset batches. It reads child items with one `log_id IN (...)` query per batch. Each committed batch advances `api_request_log_organizer_states.last_log_id` in the same transaction as its turn mappings, so a later run resumes automatically:

```bash
REQUEST_LOG_SQL_DSN='<request-log-app-dsn>' \
  ./request-log-organize -batch-size 100 -lag-seconds 300 -sleep 100ms
```

Historical data without recoverable session metadata is grouped into deterministic synthetic sessions using user, token, model, and a sliding 30-minute inactivity window. Those turns remain `inferred`; the latest inferred turn stays open until a later turn is observed. Ambiguous records remain `unknown` and are never exportable.

Dry runs ignore saved progress and never update it. Use `-after-id <id>` to override the saved starting point for one run, or `-ignore-progress` to perform an idempotent rescan from the beginning. Persisted progress only moves forward, including during manual rescans.

## Firewall

Only enable restrictive UFW rules after the Zeabur egress CIDR is confirmed. Open `3001` only to the internal/admin network if the viewer must be reached from another host.

```bash
sudo ufw default deny incoming
sudo ufw allow OpenSSH
sudo ufw allow from <ZEABUR_EGRESS_CIDR> to any port 3306 proto tcp
sudo ufw allow from <ADMIN_OR_INTERNAL_CIDR> to any port 3001 proto tcp
sudo ufw enable
sudo ufw status verbose
```

Validation:

```bash
mysql --protocol=tcp -h 127.0.0.1 -P 3306 -u newapi_request_log_app -p newapi_request_logs -e 'SHOW TABLES;'
mysql --protocol=tcp -h <PUBLIC_MYSQL_HOST> -P <PUBLIC_MYSQL_PORT> -u newapi_request_log_app -p newapi_request_logs -e 'SHOW TABLES;'
curl -u "$REQUEST_LOG_VIEWER_USERNAME:$REQUEST_LOG_VIEWER_PASSWORD" http://127.0.0.1:3001/api/status
```

External validation is only required for the MySQL endpoint used by Zeabur, such as a public router mapping to the remote MySQL service. If TCP connects but returns zero bytes, fix the cloud firewall, router NAT, or port forwarding before configuring Zeabur with the public DSN.
