# Shell initialization script

A dependency-free alternative to [`../init-scripts-js/`](../init-scripts-js/).
Both apply the same SQL from [`../sql/`](../sql/); pick whichever fits your
environment.

|  | `init-scripts-sh` | `init-scripts-js` |
|--|-------------------|-------------------|
| Requires | bash + curl | Node.js 16+ and `npm install` |
| Protocol | HTTP interface | HTTP interface (`@clickhouse/client`) |
| TLS verification | On by default; opt out with `CLICKHOUSE_TLS_SKIP_VERIFY=true` | Always disabled |
| `DROP_EXISTING` | Honored | Honored |
| Dry run | `--dry-run` | Not available |

Use the shell script on a jump host or in a container where installing Node is
inconvenient, and the Node script if you are already running `npm` in this
directory.

## Usage

```bash
cd backend/init-scripts-sh
cp .env.example .env      # fill in URL, credentials, database, cluster
./init.sh
```

If the script is not executable yet:

```bash
chmod +x init.sh
```

### Options

```
--mode MODE       distributed (default) or single
--env-file PATH   Environment file to load (default: ./.env next to the script)
--sql-dir PATH    Apply this directory instead of the one implied by --mode
--drop            Drop existing tables first; same as DROP_EXISTING=true
--dry-run         Print the rendered SQL without executing anything
-h, --help        Show help
```

### Topology

`--mode` (or `CLICKHOUSE_MODE`) picks which SQL directory to apply:

| Mode | Applies | Creates |
|------|---------|---------|
| `distributed` | `../sql/distributed/` | `kubernetes_events_local` (ReplicatedMergeTree) + `kubernetes_events` (Distributed), `ON CLUSTER` |
| `single` | `../sql/single/` | `kubernetes_events` (plain MergeTree), no cluster involved |

The agent and UI use `kubernetes_events` either way, so switching topologies does
not change their configuration.

```bash
./init.sh --mode single
```

In distributed mode the script first confirms `CLICKHOUSE_CLUSTER` exists in
`system.clusters` **and** that Keeper is configured, since `ON CLUSTER` needs the
former and `ReplicatedMergeTree` the latter. Both are checked because stock
single-node images ship a cluster named `default` with no Keeper, so the cluster
existing does not mean replication works. Either one missing points you at
`--mode single` instead of a cryptic DDL error.

Switching modes on an existing database is also refused unless you pass `--drop`,
since `CREATE ... IF NOT EXISTS` would otherwise leave the previous
`kubernetes_events` in place with the wrong engine and report success.

Preview exactly what would run, with template variables already substituted:

```bash
./init.sh --dry-run
./init.sh --mode single --dry-run
```

Recreate the tables after editing a `.sql` file — every statement is
`CREATE ... IF NOT EXISTS`, so an existing table is otherwise left untouched:

```bash
./init.sh --drop
```

Configuration can also come from the environment instead of a file, which is
handy in CI:

```bash
CLICKHOUSE_URL=https://clickhouse.example.com:8443 \
CLICKHOUSE_USER=default \
CLICKHOUSE_PASS=secret \
CLICKHOUSE_DATABASE=kseer \
CLICKHOUSE_MODE=single \
  ./init.sh
```

## Behavior

1. Loads `.env` if present, then applies any variables already in the
   environment.
2. Verifies it can query ClickHouse, then that the target database exists. The
   script creates **tables, not the database** — run `CREATE DATABASE <name>`
   first, adding `ON CLUSTER <cluster>` in distributed mode.
3. In distributed mode, confirms the cluster exists in `system.clusters` and that
   Keeper is configured.
4. Unless dropping, checks that any existing `kubernetes_events` has the engine
   the selected mode expects.
5. Optionally drops the tables for the selected mode, dependents first:
   `kubernetes_events` then `kubernetes_events_local` in distributed mode, just
   `kubernetes_events` in single mode. `ON CLUSTER` is used only in distributed
   mode, and `SYNC` is used so the immediate recreate does not trip over the
   replica path still held in Keeper.
6. Renders every `.sql` file in `../sql/<mode>` in filename order, substituting
   `${database}` and `${cluster}`, and executes them.

Each statement is sent with `allow_experimental_json_type=1`, which the `object
JSON` column requires.

## Constraints

- **One statement per `.sql` file.** ClickHouse's HTTP interface rejects
  multi-statement requests. Every file under `../sql/` already follows this; if
  you add a file with several statements, split it or give each a numbered prefix.
- Files execute in filename order, so numeric prefixes matter when one table
  depends on another. `kubernetes_events_local` must be created before the
  `kubernetes_events` distributed table that references it.
- Only the directory for the selected mode is applied. If you change the column
  list, update both `../sql/distributed/` and `../sql/single/` so the modes stay
  interchangeable.

## Licensing

Copyright 2026 T-Mobile US, Inc.

The source code in this repository, including the database setup, schema-management, migration, and related executable scripts, is licensed under the Apache License, Version 2.0. See the [LICENSE](../../LICENSE) file for details.

The documentation in this repository, including this README, is licensed under the Creative Commons Attribution 4.0 International License. See the [LICENSE-docs](../../LICENSE-docs) file for details.
