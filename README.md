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
- `docker compose down` keeps the data volume; add `-v` to wipe it.

Multi-architecture images (the binary is static, no CGO):

```sh
docker buildx build --platform linux/amd64,linux/arm64 -t fynanz:latest .
```

## Homelab deployment (Traefik + Tailscale)

Per AGENTS.md the app trusts the network boundary — no login in v1. Put it on
Tailscale and front it with Traefik (DNS-01 TLS).

1. Create the shared edge network (once): `docker network create proxy`
2. In `docker-compose.yml`, uncomment the `proxy` network block and the
   Traefik label block under the service.
3. In `.env`: `TRAEFIK_ENABLE=true`, `DOMAIN=fynanz.example.com` (your real
   hostname), `PUBLIC_BASE_URL=https://fynanz.example.com` (used for
   notification deep links), and make the app's `ENABLE_BANKING_REDIRECT_URL`
   an allowed redirect (`https://fynanz.example.com/links/callback` — add it
   in the Enable Banking control panel too). Remove the `ports:` entry so the
   service listens only behind Traefik.
4. The labels assume a `letsencrypt` certificate resolver set up for DNS-01
   (`--certificatesresolvers.letsencrypt.dnsChallenge.…` on Traefik) and the
   `websecure` entrypoint. Adjust if your Traefik differs.

## Using it day-to-day

- **Sync**: automatic on an interval (default 24h, `SYNC_INTERVAL`), at
  startup, or manually — *Sync now* per link / *Sync all*.
- **Inbox**: every unrecognized transaction waits here; picking a category
  writes an exact-merchant rule. Pending items also drive the
  `needs_category` push notification.
- **Notifications**: configure channels in **Settings** — ntfy (topic +
  optional token) or webhook (plain JSON POST). Each channel routes events
  independently (new tx / re-auth / sync failed).
- **Re-auth**: consent sessions expire (PSD2); the link badge turns red and a
  notification fires. One tap re-opens the consent flow.
- **Metrics**: `<base>/metrics` (Prometheus text format) — sync runs and
  outcomes, transactions ingested, uncategorized count, notification
  send/fail counts. Scrape example:

  ```yaml
  - job_name: fynanz
    static_configs: [{ targets: ["fynanz.example.com"] }]
    scheme: https
  ```

## Data

- File: `/data/fynanz.db` in the container (the `fynanz-data` volume).
- Backup: `docker exec fynanz sqlite3 /data/fynanz.db ".backup '/data/backup.db'"` + copy the file, or stop the service and copy the volume. Restore: stop, replace the file, start (migrations are idempotent).
- First sync pulls ~90 days back (`SYNC_HISTORY_DAYS`); the balance trend is
  derived from transactions since we don't snapshot balances yet.

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
