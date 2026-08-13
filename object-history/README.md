# Object History UI

Interactive visualization for Kubernetes object history. Renders a time-based graph
of your clusters' resources and their relationships, backed by the ClickHouse event
store that [`kseer-agent`](../kseer-agent/) writes to.

## Contents

- [Features](#features)
- [Quick start](#quick-start)
- [Configuration](#configuration)
- [Authentication](#authentication)
- [The ClickHouse proxy](#the-clickhouse-proxy)
- [Architecture](#architecture)
- [How the queries work](#how-the-queries-work)
- [Shareable URLs](#shareable-urls)
- [Development](#development)
- [Troubleshooting](#troubleshooting)
- [Licensing](#licensing)

## Features

- **Time travel** — a slider scrubs the graph backwards and forwards through history.
- **Playback** — play, pause, and vary the speed of time progression.
- **Live updates** — while parked at the current time, the view polls for new events.
- **Canvas graph** — objects and their relationships drawn on a 2D canvas with D3 for
  layout and zoom, so large clusters stay interactive.
- **Multi-cluster** — view and filter across every cluster in the event store.
- **Filtering** — by cluster, kind, and namespace, plus label and annotation attributes.
- **Search** — find specific objects by name.
- **Hierarchical grouping** — configure custom grouping levels to organize the graph.
- **Object details** — open any object to see its full stored JSON.
- **Shareable URLs** — filters, time range, and grouping all live in the URL.
- **UID-based tracking** — an object deleted and recreated under the same name stays
  distinguishable.

## Quick start

The UI is a static single-page app; it needs a reachable ClickHouse HTTP endpoint and
a database that the [`backend/`](../backend/) init scripts have already provisioned.

```bash
npm install
cp .env.example .env    # then edit to match your deployment
npm run dev
```

The dev server listens on <http://localhost:3000>.

Available scripts:

| Script | Purpose |
|--------|---------|
| `npm run dev` | Vite dev server on port 3000, with the `/clickhouse` proxy |
| `npm run build` | Type-check with `tsc`, then produce a production bundle |
| `npm run preview` | Serve the built bundle locally |
| `npm run lint` | ESLint over `src/` (see the note in [Development](#development)) |

To build and run the container image, which serves the bundle through nginx:

```bash
docker build -t kseer-frontend .
docker run -p 8080:80 kseer-frontend
```

## Configuration

The UI reads its database and table from configuration rather than hardcoding them, so
the same bundle works against any deployment. It resolves each value in this order:

1. `window.env` — injected at runtime in production (see below).
2. `import.meta.env.VITE_*` — baked in at build time, used for local development.
3. A built-in default: database `kseer`, table `kubernetes_events`.

### Local development — `.env`

```bash
VITE_CLICKHOUSE_DATABASE=kseer
VITE_CLICKHOUSE_TABLE=kubernetes_events

# Optional: Azure AD / MSAL auth for the authz proxy.
VITE_AZURE_ENABLED=false
VITE_AZURE_TENANT_ID=your-azure-tenant-id
VITE_AZURE_CLIENT_ID=your-azure-client-id
VITE_AZURE_USE_ID_TOKEN=false
```

The table name is the same whether the backend was provisioned in distributed or
single-instance mode, so this does not change between topologies.

### Production — runtime config via `window.env`

A production bundle is built once and configured at deploy time. `index.html` loads
`/env-config.js` before the app starts, and that file sets `window.env`. Ship it as a
ConfigMap mounted into the nginx html root, which lets you change the database or table
without rebuilding the image.

ConfigMap:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kseer-ui-config
data:
  env-config.js: |
    window.env = {
      CLICKHOUSE_DATABASE: "{{ .Values.clickhouse.database }}",
      CLICKHOUSE_TABLE: "{{ .Values.clickhouse.table }}"
    };
```

Mount it over the single file, leaving the rest of the html root intact:

```yaml
volumeMounts:
  - name: env-config
    mountPath: /usr/share/nginx/html/env-config.js
    subPath: env-config.js
volumes:
  - name: env-config
    configMap:
      name: kseer-ui-config
```

Corresponding values:

```yaml
clickhouse:
  database: "kseer"
  table: "kubernetes_events"
```

`window.env` also carries the optional `AZURE_ENABLED` and `AZURE_TENANT_ID` keys, which
take precedence over their `VITE_` equivalents in the same way.

## Authentication

There are two mutually exclusive modes, selected by `AZURE_ENABLED` / `VITE_AZURE_ENABLED`.

**Azure AD (MSAL), when enabled.** The app initializes MSAL before rendering, acquires a
token, and sends it to the proxy as `Authorization: Bearer <token>`. Use this when an
authorizing proxy in front of ClickHouse validates the token and decides what the user
may read.

**Direct ClickHouse credentials, when disabled.** The app renders a login form before the
main UI and sends `Authorization: Basic <base64>` on each query. These credentials are
held **in memory only** and are cleared on page refresh — nothing is written to
`localStorage`, so a reload means logging in again.

## The ClickHouse proxy

The browser never talks to ClickHouse directly. All queries go to the same-origin path
`/clickhouse`, which is proxied to the real endpoint. This keeps credentials and CORS
manageable and gives you one place to enforce authorization.

- **Development** — the Vite dev server proxies `/clickhouse`, stripping the prefix. The
  target is set in `vite.config.ts`; it ships pointing at a placeholder authz proxy, and
  a commented-out line shows the shape of a direct ClickHouse HTTPS endpoint.
- **Production** — nginx does the same rewrite and proxy pass. The target is in
  `nginx.conf`, also a placeholder you must change.

Both targets are examples. Point them at your own endpoint before deploying.

Queries are sent as `POST` bodies with `FORMAT JSON` appended, and three ClickHouse
settings are passed as headers: `allow_experimental_json_type` and
`json_type_escape_dots_in_keys` (both required by the `JSON` object column) and
`enable_http_compression`, which typically shrinks large aggregate responses by an order
of magnitude.

## Architecture

### Components

| Component | Role |
|-----------|------|
| `App.tsx` | Owns application state, data fetching, and filtering |
| `GraphVisualizationRectangular` | The canvas graph — the only visualization wired up |
| `TimeControls` | Timeline slider and playback controls |
| `FilterPanel` / `FilterSelect` | Multi-select filters for clusters, kinds, namespaces |
| `ObjectSearch` | Name search across the loaded objects |
| `ObjectDetailsModal` | Full stored JSON for a selected object |
| `GroupingConfigurator` | Hierarchical grouping levels |
| `StatusBar` | Connection status and object counts |
| `ClickHouseLogin` | Credential prompt when MSAL is disabled |

`GraphVisualization.tsx` (the older SVG renderer) and `ClickHouseSettings.tsx` are still
present in `src/components/` but are not imported anywhere.

### Hooks

- `useUrlState` — mirrors application state into the URL and restores it on load.
- `useLiveDruidPolling` — polls for new events while viewing the current time. The name
  is a leftover from the Druid era; it queries ClickHouse.
- `useObjectAnimations` — drives the appear, update, and delete animations.

### Rendering

The graph draws to a 2D canvas rather than the DOM, which is what keeps it usable at
scale. D3 still handles force layout and zoom behavior, hit detection is done manually
against node geometry, redraws are throttled, and off-screen nodes are culled. Heavier
data processing runs in a Web Worker (`dataProcessor.worker.ts`) to keep the main thread
responsive.

## How the queries work

All SQL lives in `src/api/clickhouse.ts`, the single place that knows the schema.

Reconstructing "what existed at time T" means finding the most recent event per object
UID at or before T. The queries do this with `LIMIT 1 BY uid` over a bounded window
rather than a `GROUP BY` with aggregates, because it uses a streaming merge-sort and
stays memory-safe on a distributed cluster.

Two details worth knowing when reading the code:

- **Lookback window.** Current-state queries scan back 7 days from the end of the
  selected range, not the full table. An object that has not changed in longer than the
  lookback will not appear. Widening it costs query time proportionally.
- **Deferred detail.** Bulk queries deliberately omit the large `object` JSON column and
  select only what the graph needs. The full object is fetched on demand when you open
  an object's details, which is the difference between a small response and a huge one.

Label and annotation access is centralized in one helper, because dotted keys need
percent-encoding and backtick-quoting under `json_type_escape_dots_in_keys`, and the
cast has to happen outside any aggregate.

To exercise queries outside the browser:

```bash
CLICKHOUSE_URL=http://localhost:8123 \
CLICKHOUSE_USER=default \
CLICKHOUSE_PASSWORD=secret \
  ./scripts/test-clickhouse-queries.sh
```

## Shareable URLs

State is serialized into query parameters and written back with a 300 ms debounce, so
dragging a slider does not spam browser history. Copying the address bar reproduces the
exact view for someone else.

| Parameter | Meaning |
|-----------|---------|
| `clusters`, `kinds`, `namespaces` | Comma-separated filter selections |
| `startTime`, `endTime` | Bounds of the loaded range, epoch milliseconds |
| `currentTime` | Playhead position, epoch milliseconds |
| `isPlaying`, `playbackSpeed` | Playback state |
| `grouping` | URL-encoded JSON grouping configuration |

```
http://localhost:3000/?clusters=prod-cluster&kinds=Pod,Deployment&namespaces=default
```

Name search is deliberately not synced to the URL.

## Development

**Stack.** React 18, TypeScript in strict mode, Vite, Material-UI, D3, date-fns, axios,
and MSAL for optional Azure auth.

**Conventions.** Functional components with hooks, Material-UI's `sx` prop for styling,
and focused single-purpose components.

**Where to make changes.**

- New filters — `FilterPanel` plus the corresponding state in `App.tsx`.
- Graph behavior — `GraphVisualizationRectangular`; remember it is canvas drawing code,
  not DOM, so there is no element to inspect.
- Schema or query changes — `src/api/clickhouse.ts` and the types in `src/types/`.

**Note on `npm run lint`.** The script invokes ESLint, but ESLint is not currently in
`devDependencies`, so it will not run until you add it. Type errors are caught by
`npm run build`, which runs `tsc` before bundling.

## Troubleshooting

**No data, or 401/403 responses.** Confirm the proxy target in `vite.config.ts` (dev) or
`nginx.conf` (production) points at your real endpoint — both ship as placeholders. Then
confirm the credentials or Azure token are accepted by that endpoint.

**Empty graph but queries succeed.** Check that `CLICKHOUSE_DATABASE` and
`CLICKHOUSE_TABLE` resolve to the database the agent is actually writing to, and that
the selected time range overlaps data that exists. Objects untouched for longer than the
7-day lookback will not appear.

**Objects missing after a long idle period.** Same cause: the lookback window. This is a
bound on the query, not missing data.

**Slow rendering.** Narrow the time range or add filters. Fewer nodes is the most
effective lever, since layout cost grows with node and edge count.

**Queries time out.** The client allows 120 seconds. A timeout usually means the range
is too wide or the table is unexpectedly large for the cluster.

## Licensing

Copyright 2026 T-Mobile US, Inc.

The source code in this repository, including the database setup, schema-management,
migration, and related executable scripts, is licensed under the Apache License, Version
2.0. See the [LICENSE](../LICENSE) file for details.

The documentation in this repository, including this README, is licensed under the
Creative Commons Attribution 4.0 International License. See the
[LICENSE-docs](../LICENSE-docs) file for details.
