# fynanz

Self-hosted transaction dashboard: pulls transactions from **Revolut** and
**Banca Mediolanum** via open banking (Enable Banking), categorizes them
automatically, asks you in real time when it doesn't recognize something, and
shows where your money goes. Ships as a single `docker compose up`.

See [AGENTS.md](AGENTS.md) for the full design — this README is the practical
setup guide.

## Features

- **Bank links** (Revolut, Mediolanum, sandbox mock) — consent flow, sessions
  that expire per PSD2 and reauthorize in one tap
- **Categorization** — rules learned from your answers; anything unrecognized
  lands in the **Inbox** (resolvable one-handed on a phone)
- **Notifications** — ntfy and webhook, per-event routing: new transactions
  to categorize, re-auth needed, sync failed
- **Dashboard** — spend by category, income vs expenses, balance trend;
  `/metrics` for Prometheus/Grafana
- **One service, no dependencies**: Go binary + SQLite file on a volume

## Quick start — sandbox (≈5 minutes)

You need an Enable Banking app registered in **Sandbox** mode
([control panel](https://enablebanking.com/cp), sign in with your email).
Register the app with redirect URL `http://localhost:8080/links/callback`
and keep the browser-generated private key (a `.pem` whose filename is the
application's UUID).

```sh
cp .env.example .env
# edit .env:  ENABLE_BANKING_APP_ID=<the uuid>   ENABLE_BANKING_PRIVATE_KEY=</path/to/<uuid>.pem>
make creds        # optional: verifies the key/id pair against the API
make dev          # runs on http://localhost:8080 with hot reload
```

Open the dashboard, pick **Sandbox (Mock ASPSP) → Connect**, authorize in the
browser — transactions appear and land in the Inbox. Answer a few
categorizations and the rules learn from you.

## Docker

```sh
cp .env.example .env
# edit .env:  ENABLE_BANKING_APP_ID, EB_KEY_HOST_PATH=/path/to/<uuid>.pem
docker compose up --build     # or: make docker-up
```

- Migrations run automatically at startup.
- The SQLite database persists in the `fynanz-data` volume.
- The private key is bind-mounted read-only at `/data/fynanz-key.pem`;
  `EB_KEY_HOST_PATH` is its host-side path, if it lives elsewhere just edit it.
  **Or skip the mount entirely**: paste the key on Settings → Enable Banking,
  where credentials live in the DB (override the env defaults, apply
  immediately — ideal for configuring a production app after deploy).
- `docker compose down` keeps the data volume; add `-v` to wipe it.

Multi-architecture images (the binary is static, no CGO):

```sh
docker buildx build --platform linux/amd64,linux/arm64 -t fynanz:latest .
```

## Deployment behind a reverse proxy

The app trusts the network boundary — there is no login in v1. Put it on a
private network and front it with your own reverse proxy that terminates TLS.

1. Create the shared edge network (once): `docker network create proxy`
2. In `docker-compose.yml`, uncomment the `proxy` network block and the
   reverse-proxy label block under the service.
3. In `.env`: set `TRAEFIK_ENABLE=true` (if using the Traefik labels),
   `DOMAIN=fynanz.example.com` (your hostname),
   `PUBLIC_BASE_URL=https://fynanz.example.com` (used for notification deep
   links), and make the app's `ENABLE_BANKING_REDIRECT_URL` an allowed
   redirect (`https://fynanz.example.com/links/callback` — add it in the
   Enable Banking control panel too). Remove the `ports:` entry so the
   service listens only behind the proxy.
4. The labels assume a `letsencrypt` certificate resolver set up for DNS-01
   and the `websecure` entrypoint. Adjust to match your proxy.

## Using it day-to-day

- **Sync**: automatic on an interval (default 24h, `SYNC_INTERVAL`), at
  startup, or manually — *Sync now* per link / *Sync all*.
- **History depth**: every sync fetches a rolling `SYNC_HISTORY_DAYS` window
  (default 365). If the dashboard shows no data for the earlier months of a
  12M/1y view (e.g. only the last few months exist), the install simply
  started recently or synced with a shorter window — raise
  `SYNC_HISTORY_DAYS` in `.env`, restart, and hit *Sync all*: the re-sync
  pulls deeper history and merges without duplicates (transactions dedupe on
  entry_reference). How far back a bank actually serves its history varies
  under PSD2 — many ASPSPs cap it at ~90 days, so a year of Vint data may
  simply not be available from the bank itself.
- **Inbox**: every unrecognized transaction waits here; picking a category
  writes an exact-merchant rule. Pending items also drive the
  `needs_category` push notification.
- **Notifications**: the Notifications tab on the dashboard configures
  ntfy (topic + optional token) or webhook channels with per-event routing
  (new tx / re-auth / sync failed) AND shows the send history — each
  channel × event attempt, timestamped, with failures and error details.
- **Enable Banking credentials**: also in **Settings** — paste the app id +
  private key for any environment (sandbox/production), click *Validate*
  (live check: identity, environment, callback registration), save. No env
  editing, no restart; the DB value overrides env.
  `make creds` / `make aspsps` remain available for CLI checks.
- **Re-auth**: consent sessions expire (PSD2); the link badge turns red and a
  notification fires. One tap re-opens the consent flow.
- **Metrics**: `<base>/metrics` (Prometheus text format) — sync runs and
  outcomes, uncategorized count, notification send/fail counts, plus a
  scrape-computed finance snapshot: current-month income/expense per
  category (`fynanz_finance_expenses{category,currency}` /
  `fynanz_finance_income{category,currency}`), month net
  (`fynanz_finance_net`), per-account balances
  (`fynanz_finance_balance`), stored transactions per currency and rule
  count. Amounts are **minor units** (EUR: ×0.01); month-over-month
  history stays in the dashboard (SQL), where it belongs — Prometheus is
  for current state, not rolling per-category series. Scrape example:

  ```yaml
  - job_name: fynanz
    static_configs: [{ targets: ["fynanz.example.com"] }]
    scheme: https
  ```

## Data

- File: `/data/fynanz.db` in the container (the `fynanz-data` volume).
- Backup: `docker exec fynanz sqlite3 /data/fynanz.db ".backup '/data/backup.db'"` + copy the file, or stop the service and copy the volume. Restore: stop, replace the file, start (migrations are idempotent).
- First sync pulls a year back by default (`SYNC_HISTORY_DAYS` — the window
  is rolling, so re-syncing with a larger value backfills); the balance trend
  is derived from transactions since we don't snapshot balances yet.

## Troubleshooting

- **Sandbox consent link dead** — sandbox auth sessions are short-lived;
  click *Re-connect* for a fresh one.
- **"error: invalid_grant" on consent** — the session expired; re-connect.
- **Mediolanum credit cards** — not exposed via open banking (current
  accounts are fine).
- Anything else: `docker logs fynanz`, or the link row's error text on the
  dashboard (copyable via click).

## Project layout

```
cmd/server        HTTP server, dashboard pages, metrics
cmd/checkcreds    credential/API verification helper (make creds)
internal/sync     Enable Banking client (JWT), consent + sync flows
internal/categorize  merchant normalization, rules, transfer detection
internal/notify   Notifier interface, ntfy + webhook backends
internal/store    SQLite access layer + embedded migrations
internal/metrics  Prometheus collectors
web/              server-rendered templates (HTMX), static assets
migrations/       golang-migrate SQL files (embedded + CLI-usable)
```

## Development

```sh
make dev          # hot reload (air)
make test         # go test -race ./...
make lint         # golangci-lint
make creds        # verify Enable Banking credentials
make migrate-up   # (CLI migrations, dev/ops)
```
