# Shopee QRIS gateway

A Go Fiber v3 API around [PayGateMe](https://github.com/hirotomasato/paygateme), with GORM-backed PostgreSQL payment tracking and signed website webhooks.

## Run

Requires Go 1.27.3 and PostgreSQL. For a local database:

```sh
docker compose up -d postgres
cp .env.example .env
```

Edit `.env`: set independent random `API_KEY` and `WEBHOOK_SECRET` values (at least 32 characters each), your merchant/store IDs, and your website's exact webhook origin. Generate secrets with `openssl rand -hex 32`. The compose database credentials are for local development; use your deployed database credentials and TLS configuration in production.

Place a PayGateMe Shopee session in `session.json` (or create one with the [Telegram bot](#telegram-session-bot)) and the matching store's **static QRIS payload string** in `qris.txt`. Configure different paths with `SHOPEE_SESSION_FILE` and `SHOPEE_QRIS_FILE`. Keep these files private (`chmod 600 session.json qris.txt`); both are ignored by Git. `SHOPEE_MERCHANT_ID` must match `session.merchant.id`; `SHOPEE_STORE_ID` must match `session.storeId`. The operator must ensure the static QRIS belongs to that store.

```sh
set -a
source .env
set +a
go run ./cmd/api
```

Environment files are not loaded automatically. The PostgreSQL adapter uses GORM models, queries, upserts, and transactions. The existing SQL schema is applied through GORM on startup under a database lock to preserve its constraints and partial indexes; it does not use AutoMigrate. PostgreSQL-specific allocation queries and advisory locks also run through GORM. An unavailable database or invalid static QRIS stops startup. A missing, invalid, or expired session leaves the API running with degraded login status. New payments are accepted only after a successful complete Shopee feed scan. The HTTP API exposes no login or OTP endpoint; logins go through the optional Telegram bot.

Replace `session.json` atomically when logging in again; the gateway reloads it without a restart. Without the Telegram bot, the gateway never refreshes or overwrites the session. Merchant/store configuration and QRIS changes require a restart. Use one configured merchant/store per deployment. API requests should reach the service over HTTPS through your reverse proxy.

## Telegram session bot

Shopee's merchant token cookie lasts about 24 hours. The optional Telegram bot keeps the session alive and runs the OTP login from chat when it cannot. It runs inside the API process and is enabled by setting `TELEGRAM_BOT_TOKEN`.

1. Create a bot with [@BotFather](https://t.me/BotFather) and copy its token.
2. Send `/start` to your bot from each admin account. The bot replies to unknown users with their Telegram user ID.
3. Set `TELEGRAM_BOT_TOKEN` and `TELEGRAM_ADMIN_IDS` (comma-separated user IDs), then restart the API.

| Variable | Default | Purpose |
| --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | unset | Enables the bot |
| `TELEGRAM_ADMIN_IDS` | required with the token | Telegram user IDs allowed to use the bot; alerts go to each one |
| `SHOPEE_LOGIN_PHONE` | unset | Use this phone number during `/login` instead of asking |
| `SHOPEE_LOGIN_PASSWORD` | unset | Use this account password during `/login` instead of asking |
| `SESSION_REFRESH_BEFORE` | `2h` | Renew the token this long before it expires (1m to 23h) |
| `SESSION_CHECK_INTERVAL` | `1m` | How often the session is checked (10s to 1h) |

Commands, accepted only in private chats with an admin:

| Command | Action |
| --- | --- |
| `/status` | Gateway login state, merchant, store, token expiry, and whether renewal without OTP is possible |
| `/login` | Ask for the phone number and password (unless configured), request a Shopee OTP, then wait for the code |
| `/skip` | Continue `/login` for an account without a password |
| `/refresh` | Renew the token now without an OTP |
| `/cancel` | Abort the login in progress |

Session handling:

- When the token is within `SESSION_REFRESH_BEFORE` of expiry, or Shopee rejects it, the bot renews it without an OTP while Shopee still recognizes the account login. Renewals are attempted at most every 10 minutes and are announced silently.
- If renewal is impossible (Shopee ended the account login, the session has no renewal credential, or the file is missing, invalid, or for another merchant/store), the bot alerts every admin and repeats the alert hourly until the session works again. Reply `/login` to log in with an OTP.
- Transient renewal failures are retried quietly while the current token still works, and alert once it has expired.
- `/login` uses `SHOPEE_MERCHANT_ID` and `SHOPEE_STORE_ID`; a login that cannot reach them is rejected. The OTP is valid for 10 minutes and allows 5 attempts.
- The bot deletes messages containing the phone number, password, and OTP after reading them. Telegram bot chats are not end-to-end encrypted; set `SHOPEE_LOGIN_PASSWORD` instead if you would rather not send the password through Telegram.
- Messages older than five minutes, for example queued while the API was down, are ignored.

The bot writes the session file atomically (temporary file, `fsync`, rename) with mode `0600`, so the API needs write access to the session file's directory. Renewal results are discarded if the file changed during the renewal. After a new session is written, the gateway reports `unverified` until its next feed scan (within `POLL_INTERVAL`) and returns `503` for new payments in the meantime. Only one API instance should run the bot for a given bot token, since Telegram delivers updates to a single poller.

## Docker development with hot reload

The development setup follows [backend-template-go](https://github.com/Reynadi531/backend-template-go)'s Air image, source bind mount, and Make commands. Prepare `.env`, `qris.txt`, and your session as described above, then run:

```sh
make docker.dev.build
```

This starts the API and PostgreSQL together. The API is available at `http://localhost:8080`; set `API_PORT=8081` to use another host port. The container overrides `DATABASE_URL` to reach the `postgres` service and listens on `:8080`. Other application settings come from `.env`. Set `GATEWAY_ENV_FILE` to use another environment file.

Air rebuilds and restarts the API when Go source, embedded SQL, or Go module/workspace files change. Build failures stop the previous API process and appear in the container logs. Reloads send SIGINT and allow up to 20 seconds for shutdown. Build output and Go compilation caches use Docker volumes; PostgreSQL uses the existing `gateway_data` volume. Session JSON changes are handled by the gateway itself without an Air rebuild.

```sh
make docker.dev        # Start using the existing development image
make docker.dev.down   # Stop containers; retain database and cache volumes
```

Run `make docker.dev.build` after changing the Dockerfile or Air version. After editing `.env`, rerun `make docker.dev` to recreate the API container with the new environment. With Docker Desktop, if filesystem notifications do not propagate, add `poll = true` under `[build]` in `.air.toml` and restart the API container. This setup is for local development; the repository bind mount exposes local session files to the API container.

## Container image

Build the production image locally:

```sh
docker build -t proxy-gateway-shopee .
```

The image contains a static Go binary on distroless Debian 13 and runs as UID/GID `65532:65532`. Pass configuration with `--env-file` and mount the session and QRIS files at the configured paths. For example, with both files in a dedicated `secrets` directory:

```sh
docker run --rm --name shopee-gateway \
  --env-file .env \
  -e HTTP_ADDRESS=:8080 \
  -e SHOPEE_SESSION_FILE=/run/secrets/session.json \
  -e SHOPEE_QRIS_FILE=/run/secrets/qris.txt \
  --mount type=bind,src="$(pwd)/secrets",dst=/run/secrets \
  -p 127.0.0.1:8080:8080 \
  proxy-gateway-shopee
```

Use a `DATABASE_URL` reachable from the container; `localhost` refers to the container itself. Grant UID `65532` read access to the mounted files and traversal access to their directory, while keeping them private. With the Telegram bot enabled, UID `65532` also needs write access to the directory (for example `chown 65532:65532 secrets && chmod 700 secrets`); without the bot, add `,readonly` to the mount. Mounting the directory allows atomic session replacement on the host to remain visible in the container. Secrets are excluded from the build context by `.dockerignore`.

### Production compose

`compose.prod.yaml` runs the published GHCR image with its own PostgreSQL. Put `session.json` and `qris.txt` in `./secrets` (override with `SECRETS_DIR`); the directory is bind-mounted at `/run/secrets` (writable so the Telegram bot can replace the session; set `read_only: true` on the mount if you do not use the bot), and the compose file points `SHOPEE_SESSION_FILE` and `SHOPEE_QRIS_FILE` there. Set `POSTGRES_PASSWORD` in `.env`; compose builds `DATABASE_URL` from it.

```sh
mkdir -p secrets && cp session.json qris.txt secrets/
sudo chown -R 65532:65532 secrets && chmod 700 secrets && chmod 600 secrets/*
make docker.prod        # docker compose -f compose.prod.yaml up -d
make docker.prod.pull   # pull a newer image, then rerun make docker.prod
make docker.prod.down   # stop; database volume retained
```

`IMAGE_TAG` selects the image tag (default `latest`). The API binds to `127.0.0.1:8080` by default; change with `API_BIND` and `API_PORT`, or put a reverse proxy in front. The compose project is named `shopee-gateway`, so it does not touch the development containers or volume.

The `Publish container image` GitHub Actions workflow builds Linux AMD64 and ARM64 images and pushes them to `ghcr.io/reynadi531/proxy-gateway-shopee`. Pushes to `main` publish `latest`, `main`, and a short commit SHA tag. Tags matching `v*` publish that version tag and a short commit SHA tag. The workflow also supports manual runs and authenticates with the built-in `GITHUB_TOKEN` using `packages: write`; no separate registry secret is needed.

## API

Interactive Swagger documentation is available at `http://localhost:8080/swagger/` (or your configured API port). Click **Authorize**, enter your `API_KEY`, then use **Try it out** to call the protected endpoints. The UI and its assets are served locally by the API; no CDN or separate documentation container is required. API keys are held in page memory and are not persisted across reloads.

The OpenAPI 3.0 specification is available at `/swagger/openapi.json`, with its source in `docs/openapi.json`. It documents authentication, request and response schemas, errors, login states, and signed webhook callbacks. The docs are public; API operations still require the API key. Documentation files are embedded in the Go binary, and Air rebuilds the API when they change.

All `/api/v1` endpoints require `X-API-Key`. `GET /healthz` is public process liveness; it does not assert database or Shopee health.

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/api/v1/transactions` | Create or retrieve an idempotent payment |
| GET | `/api/v1/transactions/:transaction_id` | Retrieve its persisted state |
| GET | `/api/v1/shopee/status` | Inspect login and feed health |

```sh
curl http://localhost:8080/api/v1/transactions \
  -H "X-API-Key: $API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"transaction_id":"order-123","amount":50000,"webhook_url":"https://your-website.example/payments/webhook"}'
```

`amount` is an integer in whole IDR, between 1 and 999999999. `transaction_id` is a nonempty string of at most 128 bytes, with no control characters or surrounding whitespace. It is globally unique in this database. `webhook_url` must use an origin listed in `WEBHOOK_ALLOWED_ORIGINS` (comma separated, exact scheme/host/port, no trailing slash). The allowlist is operator controlled: configure only website origins you trust. HTTPS is recommended; explicitly allowlisted HTTP origins are supported for local integration.

Example response (`201` on creation, `200` on an identical retry):

```json
{
  "transaction": {
    "transaction_id": "order-123",
    "amount": 50000,
    "unique_code": 1,
    "total_amount": 50001,
    "qris": "<dynamic EMVCo QRIS payload encoding 50001 IDR>",
    "status": "pending",
    "created_at": "2026-11-10T10:00:00Z",
    "expires_at": "2026-11-10T10:15:00Z",
    "updated_at": "2026-11-10T10:00:00Z"
  },
  "login_state": {
    "authenticated": true,
    "state": "ready",
    "last_checked_at": "2026-11-10T09:59:59Z",
    "last_successful_at": "2026-11-10T09:59:59Z"
  }
}
```

The QRIS field is text to render as a QR code on your website. Pay exactly `total_amount`; `unique_code` is added to the requested amount. Set `UNIQUE_CODE_MIN` and `UNIQUE_CODE_MAX` to choose the inclusive code range (defaults: 1 and 999). Both must be integers between 1 and 999, with the minimum no greater than the maximum. Invalid settings stop startup. Restart the API after changing them; existing payments keep their original codes. Database locking prevents overlapping payable amounts, including collisions between different base amounts and across API instances. Pending payments reserve their amounts. Paid payments release their amounts immediately, allowing their unique codes to be reused; expired payments reserve their amounts until two minutes after their deadline. Exhaustion of the configured range returns `409`.

Repeating an ID with a different amount or webhook URL returns `409`. A retry with the original details retrieves the payment even if Shopee is unavailable. Invalid requests return `400`, missing IDs `404`, invalid/missing API keys `401`, and unavailable sessions `503`. Unknown server errors return a generic `500`; request IDs are included in response headers. GET and POST transaction responses include `login_state`.

`authenticated` reflects local session credential validity, including known API rejection. Only `state: "ready"` also indicates a recent successful complete feed scan. Other states include `unverified`, `session_missing`, `session_invalid`, `scope_mismatch`, `session_expired_or_invalid`, `auth_required`, `unreachable`, `scan_incomplete`, and `stale`. The timestamps show the last provider check and last successful scan; local validity alone does not guarantee Shopee will accept the session.

## Settlement and webhooks

The worker polls Shopee every `POLL_INTERVAL` (default `5s`). Payments default to a `15m` deadline configured by `PAYMENT_EXPIRY`. It matches only Shopee `completed` transactions with the exact payable amount, configured merchant/store scope, and a provider timestamp within the payment window. Shopee timestamps have second precision. Provider transaction IDs can settle only one payment. Failed, unknown, or missing statuses never settle a payment.

A matching payment becomes `paid`. An unmatched payment becomes `expired` after its deadline plus a two minute feed delay allowance, following a successful complete scan. During a provider outage or truncated scan, it remains pending. `expired` means no matching successful payment was found, rather than a failed individual payment attempt. Payments made after the deadline are stored in the merchant feed table but do not settle that order. The generated QRIS itself cannot enforce expiry at the payment provider.

The database holds:

- `gateway_transactions`: API payment requests, amounts, QRIS, deadlines and status.
- `gateway_merchant_transactions`: normalized feed entries and their raw Shopee payloads, including unmatched transactions.
- `gateway_scan`: durable scan checkpoints.
- `gateway_webhooks`: final status events, delivery attempts, errors and delivery timestamps.

The first scan imports the previous 24 hours. Later scans resume from the durable checkpoint with a two minute overlap and also cover the oldest pending payment, including after a restart or outage. This collects transactions exposed by the SDK feed for the configured store, not the account's entire historical ledger. `SHOPEE_MAX_PAGES` defaults to 100. If the feed is truncated, increase that limit; the gateway holds the checkpoint and does not expire payments until the scan completes. Transactions reported later than the overlap after an order is finalized need operator reconciliation.

Each final state creates its webhook event in the same database transaction. A worker POSTs an event such as:

```json
{
  "event_id": "<stable UUID>",
  "type": "transaction.paid",
  "transaction": {
    "transaction_id": "order-123",
    "amount": 50000,
    "unique_code": 1,
    "total_amount": 50001,
    "qris": "<dynamic QRIS payload>",
    "status": "paid",
    "provider_transaction_id": "<Shopee transaction ID>",
    "created_at": "2026-11-10T10:00:00Z",
    "expires_at": "2026-11-10T10:15:00Z",
    "updated_at": "2026-11-10T10:00:05Z"
  }
}
```

An unsuccessful payment window sends `type: "transaction.expired"` and `status: "expired"`. Pending events are not sent. Webhook headers:

| Header | Value |
| --- | --- |
| `X-Webhook-ID` | Stable event UUID |
| `X-Webhook-Timestamp` | Unix timestamp in seconds, refreshed per attempt |
| `X-Webhook-Signature` | `sha256=<hex HMAC-SHA256>` |

Verify the signature with `WEBHOOK_SECRET` over **timestamp + `.` + the exact raw HTTP body bytes**, using a constant time comparison. Reject stale timestamps (for example, outside five minutes) and deduplicate by event ID. Do not reserialize JSON before verifying. The API key is never sent to the webhook.

Return any `2xx` after persisting the event. Non-2xx responses, timeouts, and transport errors retry indefinitely with exponential backoff capped at 2560 seconds. Delivery uses a one minute database lease and a ten second request timeout; redirects are not followed. Retries survive restart. Delivery is at least once: a crash after website acceptance can produce another attempt with the same event ID. Changing the signing secret requires coordinating the receiving website; removing an allowed origin blocks outstanding deliveries to that origin.

## Checks

```sh
make test
make vet
make build
```

PostgreSQL integration tests are enabled with `TEST_DATABASE_URL`. Use a disposable database, separate from your real gateway data:

```sh
TEST_DATABASE_URL='postgres://gateway:gateway@localhost:5432/gateway_test?sslmode=disable' \
  go test -race ./...
```

Tests cover concurrent allocation, idempotency, persistence across repository instances, matching and expiration, outbox leasing/retries, API authentication/validation, and webhook signatures/redirect refusal. Live Shopee settlement requires your actual session, matching static QRIS, and a real payment.
