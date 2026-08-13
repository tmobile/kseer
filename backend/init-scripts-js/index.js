require('dotenv').config();
const { createClient } = require('@clickhouse/client');
const fs = require('fs');
const path = require('path');

// Disable TLS certificate verification
process.env.NODE_TLS_REJECT_UNAUTHORIZED = '0';

// Topology to provision. 'distributed' creates ReplicatedMergeTree storage plus
// a Distributed table ON CLUSTER; 'single' creates one plain MergeTree table on
// a single node. Both expose the same `kubernetes_events` table to the agent
// and the UI.
const MODE = (process.env.CLICKHOUSE_MODE || 'distributed').toLowerCase();

// Dropped in this order so dependents go before their dependencies.
const DROP_ORDER = MODE === 'single'
  ? ['kubernetes_events']
  : ['kubernetes_events', 'kubernetes_events_local'];

// Configuration
const config = {
  database: process.env.CLICKHOUSE_DATABASE,
  clusterName: process.env.CLICKHOUSE_CLUSTER || 'default',
  sqlDir: path.join(__dirname, '..', 'sql', MODE),
  dropExisting: /^(1|true|yes)$/i.test(process.env.DROP_EXISTING || ''),
};

// Single-node deployments have no cluster to coordinate through.
const onCluster = MODE === 'single' ? '' : ` ON CLUSTER ${config.clusterName}`;

const client = createClient({
  url: process.env.CLICKHOUSE_URL,
  username: process.env.CLICKHOUSE_USER,
  password: process.env.CLICKHOUSE_PASS,
  tls: { rejectUnauthorized: false },
  clickhouse_settings: {
    allow_experimental_json_type: 1,
  },
});

/**
 * Read SQL file and replace template variables
 */
function readSqlFile(filename) {
  const filePath = path.join(config.sqlDir, filename);
  let sql = fs.readFileSync(filePath, 'utf8');
  
  // Replace template variables
  sql = sql.replace(/\$\{database\}/g, config.database);
  sql = sql.replace(/\$\{cluster\}/g, config.clusterName);
  
  return sql;
}

/**
 * Execute a SQL file
 */
async function executeSqlFile(filename) {
  console.log(`Executing ${filename}...`);
  
  const sql = readSqlFile(filename);
  
  await client.exec({
    query: sql,
    clickhouse_settings: {
      allow_experimental_json_type: 1,
    },
  });
  
  console.log(`✅ ${filename} executed successfully`);
}

/**
 * Run a scalar query and return its single value as a trimmed string
 */
async function scalar(query) {
  const rs = await client.query({ query, format: 'TabSeparated' });
  return (await rs.text()).trim();
}

/**
 * Fail early on the misconfigurations that would otherwise surface as cryptic
 * DDL errors partway through. Mirrors init-scripts-sh/init.sh.
 */
async function preflight() {
  if (await scalar(`SELECT count() FROM system.databases WHERE name = '${config.database}'`) === '0') {
    console.error(`❌ Database '${config.database}' does not exist.`);
    console.error(`   Create it first:  CREATE DATABASE ${config.database}${onCluster}`);
    process.exit(1);
  }

  // Switching topology over an existing install is silently wrong otherwise:
  // every statement is CREATE ... IF NOT EXISTS, so an existing
  // kubernetes_events of the other engine survives and the agent keeps writing
  // to a table that is no longer part of the intended layout. Dropping
  // recreates it, so the check does not apply then.
  if (!config.dropExisting) {
    const expected = MODE === 'distributed' ? 'Distributed' : 'MergeTree';
    const actual = await scalar(
      `SELECT engine FROM system.tables WHERE database = '${config.database}' AND name = 'kubernetes_events'`
    );
    if (actual && actual !== expected) {
      console.error(`❌ ${config.database}.kubernetes_events already exists with engine`);
      console.error(`   ${actual}, but ${MODE} mode expects ${expected}.`);
      console.error(`   CREATE ... IF NOT EXISTS would leave the existing table in place, so the`);
      console.error(`   topology would not actually change. Re-run with DROP_EXISTING=true.`);
      process.exit(1);
    }
  }

  if (MODE !== 'distributed') return;

  if (await scalar(`SELECT count() FROM system.clusters WHERE cluster = '${config.clusterName}'`) === '0') {
    console.error(`❌ No ClickHouse cluster named '${config.clusterName}' exists.`);
    console.error(`   Set CLICKHOUSE_CLUSTER to a cluster from system.clusters, or if this`);
    console.error(`   is a single node, re-run with:  CLICKHOUSE_MODE=single npm start`);
    process.exit(1);
  }

  // Stock single-node images ship a "default" cluster but no Keeper, and
  // ReplicatedMergeTree needs one. system.zookeeper_connection only exists
  // once Keeper/ZooKeeper is configured.
  const keeper = await scalar(
    `SELECT count() FROM system.tables WHERE database = 'system' AND name = 'zookeeper_connection'`
  );
  if (keeper === '0') {
    console.error(`❌ This server has no ClickHouse Keeper (or ZooKeeper) configured, which`);
    console.error(`   ReplicatedMergeTree requires. Cluster '${config.clusterName}' exists but`);
    console.error(`   cannot replicate. For a standalone node, re-run with:`);
    console.error(`   CLICKHOUSE_MODE=single npm start`);
    process.exit(1);
  }

  // The shipped DDL substitutes {shard} and {replica} into the replica path.
  const macros = await scalar(`SELECT macro FROM system.macros WHERE macro IN ('shard', 'replica') ORDER BY macro`);
  const missing = ['replica', 'shard'].filter(m => !macros.split('\n').includes(m));
  if (missing.length > 0) {
    console.log(`⚠️  Server defines no '${missing.join(", ")}' macro(s). The shipped distributed`);
    console.log(`   DDL references {shard} and {replica}; either define them in the server`);
    console.log(`   config or edit sql/distributed/01-events-local.sql.`);
  }
}

/**
 * Drop tables in reverse dependency order
 */
async function dropTables() {
  console.log('Dropping existing tables...\n');
  
  for (const table of DROP_ORDER) {
    try {
      // SYNC is required, not cosmetic: without it the Atomic database engine
      // defers the drop for database_atomic_delay_before_drop_table_sec, so the
      // ReplicatedMergeTree replica path lingers in Keeper and recreating the
      // table immediately fails with REPLICA_ALREADY_EXISTS.
      await client.exec({
        query: `DROP TABLE IF EXISTS ${config.database}.${table}${onCluster} SYNC`,
      });
      console.log(`✅ Dropped ${table}`);
    } catch (e) {
      console.log(`⚠️  Could not drop ${table}:`, e.message);
    }
  }
  
  console.log('');
}

async function main() {
  if (MODE !== 'distributed' && MODE !== 'single') {
    console.error(`❌ CLICKHOUSE_MODE must be 'distributed' or 'single', got '${MODE}'.`);
    process.exit(1);
  }

  for (const required of ['CLICKHOUSE_URL', 'CLICKHOUSE_DATABASE']) {
    if (!process.env[required]) {
      console.error(`❌ ${required} is not set. Copy .env.example to .env and fill it in.`);
      process.exit(1);
    }
  }

  console.log(`\n========================================`);
  console.log(`ClickHouse Table Initialization`);
  console.log(`========================================`);
  console.log(`Mode: ${MODE}`);
  console.log(`Database: ${config.database}`);
  if (MODE === 'distributed') {
    console.log(`Cluster: ${config.clusterName}`);
  }
  console.log(`SQL Directory: ${config.sqlDir}`);
  console.log(`Drop existing: ${config.dropExisting}`);
  console.log(`========================================\n`);
  
  // Verify experimental JSON type is enabled
  const check = await client.query({
    query: `
      SELECT name, value, changed
      FROM system.settings
      WHERE name = 'allow_experimental_json_type'
    `,
    format: 'JSONEachRow',
    clickhouse_settings: { 
      allow_experimental_json_type: 1,
    },
  });

  const rows = await check.json();
  console.log('Experimental JSON type setting:', rows);
  console.log('');

  await preflight();

  // Drop existing tables if requested
  if (config.dropExisting) {
    await dropTables();
  }

  // Every .sql file in sql/ runs, ordered by its numeric filename prefix
  const sqlFiles = fs.readdirSync(config.sqlDir)
    .filter(f => f.endsWith('.sql'))
    .sort();

  if (sqlFiles.length === 0) {
    console.error(`❌ No .sql files found in ${config.sqlDir}`);
    process.exit(1);
  }

  console.log(`Found ${sqlFiles.length} SQL files:\n`);
  
  // Execute each SQL file in order
  for (const file of sqlFiles) {
    await executeSqlFile(file);
  }

  console.log('\n========================================');
  console.log('✅ All tables created successfully');
  console.log('========================================\n');
  
  await client.close();
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
