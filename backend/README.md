# ClickHouse Initialization Scripts

Automated setup for distributed ClickHouse tables for Kubernetes event storage.

Two interchangeable implementations apply the same SQL from `sql/`:

| | Requires | Notes |
|--|----------|-------|
| [`init-scripts-sh/`](init-scripts-sh/) | bash + curl | No install step; supports `--dry-run`. See its [README](init-scripts-sh/README.md). |
| [`init-scripts-js/`](init-scripts-js/) | Node.js 16+ | Uses `@clickhouse/client`. |

There is a third way to apply the same schema: the unified Helm chart runs it as
a Job inside the cluster, which is usually what you want if you are deploying
with Helm anyway. See
[`deploy/helm/kseer/README.md`](../deploy/helm/kseer/README.md). The chart
carries its own copy of these `.sql` files, since Helm cannot read files outside
a chart directory, so a schema change here has to be copied there too — the
chart README has the command.

## Choosing a topology

Both scripts can provision either of two layouts, selected with
`CLICKHOUSE_MODE` (or `--mode` on the shell script):

| Mode | Creates | Use when |
|------|---------|----------|
| `distributed` (default) | `kubernetes_events_local` (ReplicatedMergeTree) + `kubernetes_events` (Distributed), both `ON CLUSTER` | You have a multi-node ClickHouse cluster with Keeper |
| `single` | `kubernetes_events` (plain MergeTree) | You have one ClickHouse node — local development, a small install, or a trial |

**The table the agent writes to and the UI reads from is `kubernetes_events` in
both modes**, with identical columns, ordering, partitioning, and TTL. Only the
engine differs, so you can start on a single node and move to a cluster later
without touching agent or UI configuration.

Distributed mode needs a real cluster: `ON CLUSTER` requires the cluster to
exist in `system.clusters`, and `ReplicatedMergeTree` requires Keeper. Both are
checked up front, and either one missing points you at `single` mode. The two
checks are separate on purpose — stock single-node ClickHouse images ship a
cluster named `default` but no Keeper, so a cluster entry alone does not mean
replication will work.

### Switching between modes

Changing `CLICKHOUSE_MODE` on an existing database requires dropping, because
every statement is `CREATE ... IF NOT EXISTS`: the `kubernetes_events` left over
from the other mode would survive with the wrong engine, and the agent would keep
writing to a table outside the new layout. The scripts detect that mismatch and
refuse to continue rather than reporting a success that changed nothing:

```
❌ kseer.kubernetes_events already exists with engine
❌ MergeTree, but distributed mode expects Distributed.
```

Re-run with `--drop` (or `DROP_EXISTING=true`) to complete the switch. **This
discards existing event history**, so on a live install, export what you need
first. Re-running in the *same* mode is idempotent and safe.

Going from distributed to single leaves `kubernetes_events_local` behind, since single
mode only manages `kubernetes_events` and will not drop a table it did not create. It
receives no writes, but it still holds the old data and its Keeper metadata, so drop it
by hand once you are sure you no longer need it:

```sql
DROP TABLE IF EXISTS <database>.kubernetes_events_local SYNC
```

## Quick Start

Shell:

```bash
cd init-scripts-sh
cp .env.example .env
# Edit .env with your ClickHouse credentials
./init.sh                 # distributed (default)
./init.sh --mode single   # single node
```

Node.js:

```bash
# Install dependencies
cd init-scripts-js
npm install

# Set environment variables
cp .env.example .env
# Edit .env with your ClickHouse credentials

# Run initialization
npm start                              # distributed (default)
CLICKHOUSE_MODE=single npm start       # single node
```

## Environment Variables

```bash
CLICKHOUSE_URL=https://your-clickhouse-server:8443
CLICKHOUSE_USER=your-username
CLICKHOUSE_PASS=your-password
CLICKHOUSE_DATABASE=kseer
CLICKHOUSE_MODE=distributed
CLICKHOUSE_CLUSTER=your-cluster
DROP_EXISTING=false
```

`CLICKHOUSE_URL` is the HTTP interface (`8123` plain, `8443` TLS) — not the native
protocol port the agent uses. `CLICKHOUSE_CLUSTER` defaults to `default` if unset
and is ignored in `single` mode.

The database itself is **not** created by either script — create it with
`CREATE DATABASE <name>` first (add `ON CLUSTER <cluster>` in distributed mode).
Both scripts fail with a clear message if it is missing.

`CLICKHOUSE_TLS_SKIP_VERIFY=true` skips certificate verification in the shell
script; the Node script always skips verification.

## Table Architecture

In `distributed` mode the scripts create a table setup optimized for
multi-cluster deployments:

1. **Local Storage** (`kubernetes_events_local`) - ReplicatedMergeTree for data storage per shard
2. **Distributed Table** (`kubernetes_events`) - Router for writes and query fan-out across shards

The agent inserts into `kubernetes_events`, which routes each row to the shard
selected by the sharding key; the UI reads from the same table.

In `single` mode there is one table, `kubernetes_events`, a plain MergeTree that
holds all the data on one node.

### Why This Architecture?

- **ReplicatedMergeTree**: Data is replicated within each shard for high availability
- **Distributed Table**: Provides transparent sharding across multiple ClickHouse nodes
- **Sharding Key**: `sipHash64(cluster_name, uid)` ensures even distribution across 300+ clusters
- **Single write and read path**: Both the agent and the UI use `kubernetes_events`, so there is no ingestion-only table whose schema can drift from the agent's `INSERT`
- **Same table name in both modes**: Moving between a single node and a cluster is a schema change, not a reconfiguration of every component

Every table carries the seven columns the agent writes: `timestamp`,
`cluster_name`, `operation`, `kind`, `namespace`, `uid`, and `object`.

## Modifying Table Schema

Table definitions live in `sql/`, one subdirectory per mode:

```
sql/
├── distributed/
│   ├── 01-events-local.sql       # ReplicatedMergeTree storage, per shard
│   └── 02-events-distributed.sql # Distributed router
└── single/
    └── 01-events.sql             # Plain MergeTree, single node
```

Only the directory matching the selected mode is applied, in filename order.
Numeric prefixes exist purely to fix that order —
`kubernetes_events_local` has to be created before the distributed table that
references it.

If you change the column list, change it in **both** directories so the two
modes stay interchangeable.

Keep each file to a single statement; the shell script sends one statement per
HTTP request.

### To modify a table schema:

1. Edit the appropriate SQL file under `sql/<mode>/`
2. Re-run with dropping enabled — every statement is `CREATE ... IF NOT EXISTS`,
   so an existing table is left untouched otherwise:
   - Shell: `./init.sh --drop`
   - Node.js: `DROP_EXISTING=true npm start`

**Example**: To add a new column to the storage table, edit [sql/distributed/01-events-local.sql](sql/distributed/01-events-local.sql) (and its `single/` counterpart):

```sql
CREATE TABLE IF NOT EXISTS ${database}.kubernetes_events_local
ON CLUSTER default
(
    timestamp   DateTime64(6) CODEC(Delta, LZ4),
    cluster_name LowCardinality(String) CODEC(ZSTD(1)),
    operation    LowCardinality(String) CODEC(ZSTD(1)),
    kind         LowCardinality(String) CODEC(ZSTD(1)),
    namespace    LowCardinality(String) CODEC(ZSTD(1)),
    uid          String CODEC(ZSTD(1)),
    new_field    String,  -- ADD YOUR NEW FIELD HERE
    object       JSON CODEC(ZSTD(3))
)
-- ... rest of table definition
```

Adding a column the agent does not write is safe. Removing or renaming one of
the seven columns above will break the agent's `INSERT`.

### Template Variables

SQL files support these template variables:
- `${database}` - Replaced with `CLICKHOUSE_DATABASE` value
- `${cluster}` - Replaced with `CLICKHOUSE_CLUSTER` value

## Adding New Tables

1. Create a new `.sql` file in `sql/distributed/`, `sql/single/`, or both
2. Use numeric prefix for execution order (e.g., `05-my-new-table.sql`)
3. Use template variables for database/cluster names
4. Run either script - it will auto-discover and execute new files

New tables are not added to the drop list automatically. If you want
`--drop` / `DROP_EXISTING=true` to remove them too, add them to `DROP_ORDER` in
[init-scripts-sh/init.sh](init-scripts-sh/init.sh) and
[init-scripts-js/index.js](init-scripts-js/index.js).

## Table Drop Order

When dropping is enabled, tables are dropped in reverse dependency order.

Distributed mode:

1. Distributed table (`kubernetes_events`)
2. Local table (`kubernetes_events_local`)

Single mode drops only `kubernetes_events`. Drops are issued `ON CLUSTER` in
distributed mode and without it in single mode.

Drops use `DROP TABLE ... SYNC`. This is required rather than cosmetic: the
Atomic database engine otherwise defers the drop for
`database_atomic_delay_before_drop_table_sec` (8 minutes by default), leaving the
ReplicatedMergeTree replica path in Keeper so the immediate recreate fails with
`REPLICA_ALREADY_EXISTS`.

This ensures clean recreation without dependency violations.

## Project Structure

```
backend/
├── sql/                        # SQL schema definitions, one dir per mode
│   ├── distributed/
│   │   ├── 01-events-local.sql
│   │   └── 02-events-distributed.sql
│   └── single/
│       └── 01-events.sql
├── init-scripts-sh/            # Shell initialization script
│   ├── init.sh
│   ├── README.md
│   ├── .env.example
│   └── .env (create this)
└── init-scripts-js/            # Node.js initialization script
    ├── index.js
    ├── package.json
    ├── .env.example
    └── .env (create this)
```

## Features

✅ SQL-based schema management (no hardcoded queries)  
✅ Template variable substitution  
✅ Automatic file discovery and ordering  
✅ Distributed table support with `ON CLUSTER`  
✅ Proper drop ordering for dependencies  
✅ Experimental JSON type support  
✅ Configurable via environment variables  
✅ ReplicatedMergeTree for HA within shards  
✅ Optimized sharding key for large-scale deployments  
✅ Single-node and distributed topologies from the same table name  
✅ Node.js and dependency-free shell implementations

## Licensing

Copyright 2026 T-Mobile US, Inc.

The source code in this repository, including the database setup, schema-management, migration, and related executable scripts, is licensed under the Apache License, Version 2.0. See the [LICENSE](../LICENSE) file for details.

The documentation in this repository, including this README, is licensed under the Creative Commons Attribution 4.0 International License. See the [LICENSE-docs](../LICENSE-docs) file for details.
