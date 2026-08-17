# AGENTS.md

## Project: Self-Hosted Transaction Dashboard

A self-hosted app that pulls transactions from Revolut and Mediolanum via
open banking, categorizes them automatically, and asks the user in real
time to classify anything it doesn't recognize. Ships as a single `docker
compose up`.

---

## 1. Goals

- Pull transaction history from **Revolut** and **Banca Mediolanum**
  automatically (no manual CSV export as the steady-state workflow).
- Store everything locally, single source of truth, no third-party
  dashboard/SaaS.
- Auto-categorize transactions into broad categories; when the source/
  merchant is unrecognized, surface it to the user and ask which category
  it belongs to (the answer becomes a rule for next time).
- Dashboard: spending by category, by month, by account, trends over time.
- Deploy via a simple `docker-compose.yml`, minimal moving parts.

## 2. Non-goals (for v1)

- No login screen / auth enforcement in v1 — network boundary
  (Tailscale/Traefik) is the trust boundary. Auth and multi-tenancy are
  planned for v2+ (see §6) but the schema should already be user-scoped
  from v1 to avoid a painful migration later.
- No budgeting/forecasting features yet — insights first, planning later.
- No mobile app — responsive web dashboard is enough.
- No support for banks beyond Revolut and Mediolanum initially.

## 3. Data source strategy

Neither bank exposes a simple personal-use API, so this project uses an
**open banking aggregator** rather than screen-scraping or bank-specific
integrations.

**Provider: [Enable Banking](https://enablebanking.com/)** (not
GoCardless — see note below).

- Both banks confirmed supported:
  - **Revolut** — supported.
  - **Banca Mediolanum** — supported, *except* credit card accounts are
    not exposed through its open banking API (current/checking accounts
    are fine). Worth confirming this hasn't changed once you're
    building, since Enable Banking has been actively expanding Italian
    card coverage.
- **Free tier**: register a production application, then activate it in
  **"Restricted Production"** mode by linking your own accounts. Once
  linked, the app can fetch data only from those accounts — but that's
  exactly this project's use case (single user, own accounts only), so
  it's free indefinitely, not a capped trial. No contract or KYB needed
  for this mode.
  - Tradeoff vs. a managed aggregator: your app owns a bit more of the
    session/consent bookkeeping (JWT auth, storing/refreshing session
    state per bank link) rather than an aggregator abstracting it away.
- Consent/session flow: bank links (sessions) expire periodically per
  PSD2 rules and need periodic re-authorization — the sync job needs to
  detect an expired/revoked link and prompt for re-auth rather than fail
  silently.
- Sync should run as a scheduled job (e.g. daily) inside the compose
  stack, not on-demand only.

> **Note on GoCardless**: the original draft of this doc assumed
> GoCardless Bank Account Data (ex-Nordigen), which used to be the
> default free option. As of July 2025 GoCardless **stopped accepting
> new Bank Account Data signups** — existing integrations keep working,
> but this project can't start on it. Enable Banking is the current
> community-recommended replacement and is what this doc now assumes.

## 4. v1 scope

**In v1** — the full loop: sync in, categorize (with interactive
fallback), see basic insights, get notified when something needs
attention.

- Link Revolut + Mediolanum via Enable Banking, multiple accounts per
  bank.
- Manual "sync now" + daily scheduled sync.
- Transaction list with filters (account, date range, category).
- Transaction detail + manual category override at any time.
- Rule-based auto-categorization + Uncategorized inbox with real-time
  resolve.
- **Rule editor** (`/rules`): view/add/edit/delete rules; each matches a
  merchant *or* description with `exact` / `contains` / `fuzzy` semantics,
  plus a live match preview for tuning fuzzy rules.
- **Person-to-person detection**: transfers between people (no MCC + a
  transfer bank-transaction code, excluding direct debits) are flagged P2P
  and **never auto-categorized** — they always land in the inbox, and
  resolving them writes fuzzy *description*-based rules (the friend's name
  is not a stable match key).
- Internal transfer detection (Revolut ↔ Mediolanum).
- Category management (add/rename/merge).
- Dashboard: spend by category (current + historical), income vs.
  expense over time, per-account balance trend.
- Notifications: `Notifier` interface, ntfy + webhook backends, wired to
  "needs categorization" and "sync failed / re-auth needed" events.
- Prometheus-compatible `/metrics` endpoint for Grafana/homelab
  monitoring (see §8).
- Docker Compose + Traefik/Tailscale deployment.

**Deferred to v2+**

- Notes/tags on transactions.
- Amount-threshold and digest notifications.
- Top merchants/categories, month-over-month comparison views.
- CSV export.
- Settings UI for base currency / connected accounts (env vars are fine
  for v1).
- Multi-currency FX conversion — v1 shows native currency per account,
  no cross-currency aggregation.
- Auth and multi-tenancy — see §6. v1 ships with no login (implicit
  single user, trusting the network boundary), but the schema is
  user-scoped from the start so multi-tenancy is additive later.

## 5. Categorization

- **v1 categories are broad and fixed**, e.g.:
  `Income`, `Groceries`, `Dining & Takeout`, `Transport`, `Utilities & Bills`,
  `Housing`, `Health`, `Shopping`, `Entertainment`, `Subscriptions`,
  `Transfers (internal)`, `Savings & Investments`, `Fees & Charges`,
  `Uncategorized`.
- Matching pipeline per incoming transaction:
  1. Normalize merchant/counterparty string from the bank's raw
     description.
  2. Look up against a local **rules table** (field + pattern → category),
     seeded manually + grown from user answers. Each rule targets a
     **merchant** or the **description** (remittance/reference) with one of
     three semantics: `exact` (identical text), `contains` (substring),
     `fuzzy` (every meaningful pattern word present, order-insensitive,
     one-typo tolerant). One category per (user, field, pattern); rules are
     editable at `/rules`.
  3. If no match: transaction is flagged `Uncategorized` and queued for
     the user to resolve.
- **Person-to-person rule**: P2P transactions are detected at sync time
  from narration and skip rule matching entirely — they always land in the
  inbox. The answer to a P2P item tags the transaction (recorded as a
  manual override, never re-classified) and grows a fuzzy *description*
  rule seeded from the reference text.
  - Detection is **narration-based**, tuned against a live Banca Mediolanum
    feed where the connector populates neither MCC nor bank transaction
    codes: `From <Name>` / `To <Name>` reference segments (in/out friend
    transfers, names may end in a letter initial), or a full SEPA credit
    (`Cod.Id.Ord:`) to a person-shaped counterparty (`versamento`/
    `accredito`). Hard excludes: card markers (`CARTA N./CIRCUITO/Cod.
    MCC/C/O`), direct debits (`ADDEBITO DIRETTO/CORE RCUR/SDD`), self
    top-ups (`Top-Up by`), payroll (`STIPENDIO`), fees/refunds.
  - Bare counterparty rows (`Calcagnile Eleonora` vs `Focacciami`) are
    structurally identical on that feed and are deliberately NOT flagged;
    they keep behaving as merchants. Detector tuning applies retroactively:
    reclassify re-derives the flag from persisted narration every sync.
- **Real-time prompt**: when a new uncategorized transaction lands
  (during sync), the user should see a lightweight in-app notification/
  inbox item asking "What category is this?" — answering it both tags
  the transaction and writes a new rule.
- Categories should be user-editable (rename, merge, add) without a code
  change — stored in the DB, not hardcoded enums.
- Internal transfer detection: a transaction moving money between the
  user's own linked accounts (e.g. Revolut → Mediolanum) should be
  recognized as a transfer, not double-counted as income/expense in
  aggregate views.

## 6. Auth & multi-tenancy (v2+)

Deferred, but shapes the v1 schema (see §2/§4), so defined now.

- **Auth modes** (config-selectable, not mutually exclusive at the code
  level):
  1. **None** (v1 default) — trust the network boundary
     (Tailscale/Traefik), no login screen. Fine for genuine single-user
     homelab use.
  2. **Built-in basic auth** — username/password, session via signed
     cookie. Superuser bootstrapped from `ADMIN_USER`/`ADMIN_PASSWORD`
     env vars on first boot (ideally forcing a password change on first
     login rather than keeping the env password as the long-term
     credential). Superuser can create additional users.
  3. **Authelia (forward-auth)** — app trusts `Remote-User`/
     `Remote-Groups` headers set by Authelia sitting in front via
     Traefik, and skips its own login UI entirely. Just needs to map the
     header value to a local user record.
- **Multi-tenancy**: each user gets a fully isolated set of linked bank
  accounts, transactions, categories, rules, and notification config. A
  superuser manages user accounts but does not get blanket access to
  other users' financial data by default.
- **Per-user everything downstream**: sync job iterates per user's
  linked accounts; notifications route per-user.

**Prerequisite from v1**: every table (`accounts`, `transactions`,
`categories`, `rules`, notification config) should carry a `user_id`
from day one, even with a single implicit user and no login screen.
Turning on auth in v2 then just adds a `users` table and a login layer
on top of foreign keys that already exist, rather than retrofitting
ownership onto existing data.

## 7. Notifications

A pluggable notification layer, decoupled from the transport, so new
channels can be added later without touching business logic.

- **Backends (v1)**:
  - **ntfy** — push to a topic (self-hosted ntfy instance or ntfy.sh),
    optional access token/auth for private topics.
  - **Custom webhook** — plain POST of a JSON payload to a
    user-configured URL, for chaining into n8n, Home Assistant, Slack
    (via a relay), etc. No assumptions about the receiver.
  - Interface should be a small `Notifier` abstraction so both are just
    implementations of the same contract — adding e.g. Pushover or email
    later is additive, not a rewrite.
- **Triggerable events** (each independently toggleable):
  - New transaction needs categorization (the real-time prompt from
    §4) — this is the main driver for having notifications at all.
  - Sync failed, or a bank link expired and needs re-authorization.
  - Optional: single transaction above a user-defined amount threshold.
  - Optional: daily or weekly spend digest.
- **Routing**: multiple channels can be active simultaneously, with
  per-event routing rather than all-or-nothing (e.g. ntfy for anything
  urgent/actionable, webhook for a full event log feeding another
  system).
- **Payload contract**: enough context to act without opening the
  dashboard — amount, merchant/counterparty, account, timestamp, and for
  categorization prompts, a deep link straight to the resolve action.

## 8. Observability: Prometheus metrics

A `/metrics` endpoint in Prometheus exposition format, for scraping into
an existing Grafana/Prometheus homelab setup — separate from the ntfy/
webhook notifications in §7, which are event-driven pushes, not a
scrape target.

- Standard library approach: `promhttp` handler mounted at `/metrics`
  on the same Go binary/port, no extra service in the compose file.
- Useful metrics to expose from day one:
  - Sync runs: count, duration, success/failure, per bank/account.
  - Transactions: count ingested per sync, count currently
    `Uncategorized`.
  - Notifications: sent count/failures per backend (ntfy/webhook).
  - Enable Banking link health: count of expired/needs-reauth links.
- No auth on `/metrics` in v1 (network boundary via Tailscale/Traefik is
  the trust boundary, consistent with §2) — revisit once §6 auth modes
  land, at which point it should probably stay open to the internal
  network but be excluded from user-facing auth.
- Multi-tenant note (v2+, see §6): metrics should carry a `user` label
  once multi-tenancy lands, so per-user activity is distinguishable in
  Grafana without cross-tenant data leaking into dashboard queries by
  default.

## 9. Proposed architecture

- **Backend**: Go
  - HTTP API (net/http or a lightweight router — chi is a reasonable
    default) serving the dashboard's data and handling the sync job.
  - Scheduled sync worker (could be a goroutine + ticker inside the same
    binary to keep the compose file simple — one service instead of two).
- **Database**: SQLite (single file, easy backup, fine for personal
  finance data volumes). Use a migrations tool (e.g. `golang-migrate`)
  from day one.
- **Frontend**: server-rendered Go templates + **HTMX** (+ a touch of
  Alpine.js if needed for small client-side state) — no separate JS
  build step or frontend service, keeps the compose file to one
  container. Fits well with the "Uncategorized" inbox flow: resolving a
  transaction is a small HTMX-swapped partial, not a full page reload.
  See §10 for UI/UX principles.
- **Secrets**: Enable Banking application credentials + any configured
  ntfy token/webhook URL, via `.env`, never committed.
- **Deployment**: single `docker-compose.yml` with one app service (Go
  binary + SQLite volume) — no separate DB container needed with SQLite.

## 10. UI/UX principles

- **Functional and simple over polished** — this is a personal tool, not
  a product. Favor plain tables, lists, and forms over custom widgets;
  spend effort on data clarity, not visual flourish.
- **Mobile-first, not mobile-only**: design and build the layout for a
  phone screen first (the categorization inbox in particular needs to
  be resolvable one-handed from a notification), but that's a starting
  point for information density, not a ceiling.
- **Don't waste space** — no gatekeeping data behind screen size, and no
  wasted whitespace either. At every breakpoint, use the real estate
  that's actually available: dense, information-rich layouts rather than
  padded cards with mostly empty space. A wider viewport should show
  *more information*, not the same content stretched out. Concretely:
  - Transaction list: mobile shows date/merchant/amount/category; wider
    screens add account, running balance, notes inline instead of
    behind a detail view.
  - Dashboard charts: mobile stacks them vertically one at a time;
    desktop can show several side by side without re-navigating.
  - Nothing should require a desktop session to be usable — mobile
    always has a path to every feature, just denser/scrollier.
- Plain HTML forms/tables + HTMX partial swaps satisfy this well: the
  same server-rendered fragment can be dropped into a single-column
  mobile layout or a multi-column desktop grid via CSS alone, with no
  separate mobile/desktop template branching.

## 11. Repo layout (proposed)

```
/cmd/server        entrypoint
/internal/sync      Enable Banking client, scheduled sync job
/internal/categorize rules engine, category matching
/internal/notify     Notifier interface, ntfy + webhook implementations
/internal/auth       auth mode handling (none/basic/Authelia headers), v2+
/internal/metrics    Prometheus collectors + /metrics handler
/internal/store      SQLite access layer, migrations
/internal/api        HTTP handlers
/web                Go templates (HTMX) + static assets
/migrations
docker-compose.yml
.env.example
AGENTS.md
```

## 12. Open questions

- [ ] Multi-currency handling — Revolut accounts are often multi-currency;
      does the dashboard need FX conversion to a base currency (EUR) for
      aggregate views?
- [ ] Retention: how far back should historical transactions be pulled
      on first sync (varies by bank/ASPSP under PSD2)?

## 13. Deployment notes

- Runs on Simon's existing homelab, behind **Traefik** (DNS-01 TLS) and
  reachable over **Tailscale** rather than exposed publicly — add a
  standard Traefik label set to the compose service and put it on the
  Tailscale network/internal DNS rather than punching a public port.
- Given it's SQLite + a single Go binary, this is a one-service compose
  file (no separate DB container) — a good fit for the "minimal moving
  parts" homelab pattern already in use elsewhere.

## 14. Milestones

1. Enable Banking sandbox integration: connect one test account, pull
   raw transactions, store in SQLite (schema scoped by `user_id` from
   the first migration, even with a single implicit user).
2. Real Revolut connection end-to-end (Restricted Production app, link
   own account, session handling/re-auth).
3. Real Mediolanum connection (debit card + current account, no
   separate credit card product — no known coverage gaps).
4. Rules-based categorization + "Uncategorized" inbox with resolve flow.
5. Notifications: `Notifier` interface + ntfy and webhook backends,
   wired to the categorization-inbox and sync-failure events.
6. `/metrics` endpoint with sync/transaction/notification counters, for
   Grafana/Prometheus scraping.
7. Dashboard v1: totals by category/month, simple charts — built
   mobile-first per §10, with layout that expands to use wider viewports
   rather than a separate desktop-only view.
8. Docker Compose packaging with Traefik labels, `.env.example`, README
   with setup steps.
