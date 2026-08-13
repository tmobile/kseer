package clickhouse

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/kseer/kseer-agent/pkg/outputs"
	"github.com/kseer/kseer-agent/pkg/types"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

// tableIdentifierRegex restricts ClickHouse table identifiers to the unquoted
// form, with an optional database prefix (e.g. "kseer_events" or "kseer.events").
// Config-provided table names are interpolated into queries below — this
// allowlist is what makes that interpolation safe; any name that survives this
// check cannot contain quotes, whitespace, or SQL metacharacters.
var tableIdentifierRegex = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)

func validateTableIdentifier(name string) error {
	if !tableIdentifierRegex.MatchString(name) {
		return fmt.Errorf("invalid table identifier %q: must match [A-Za-z_][A-Za-z0-9_]*(\\.[A-Za-z_][A-Za-z0-9_]*)?", name)
	}
	return nil
}

func init() {
	outputs.Register("clickhouse", NewClickHouseOutput)
}

// ClickHouseOutput sends events directly to ClickHouse
type ClickHouseOutput struct {
	conn                driver.Conn
	table               string
	eventsTable         string // Table to query for per-kind resume RVs; empty means disabled
	logger              *zap.Logger
	tableVersion        string // "object_json", "object_string", or "object_normalized"
	verboseEventLogging bool   // When true, log one line per event with kind/namespace/name/uid/resourceVersion

	// Prebuilt SQL. Table identifiers are interpolated once at construction
	// (after validateTableIdentifier); call sites pass these fixed strings to
	// the driver with ? placeholders for values, so there is no per-call SQL
	// string formatting.
	insertSQL            string
	sentinelInsertSQL    string
	resumeRVSQL          string
	lastSeedTimestampSQL string
	clusterPodCountsSQL  string
}

// Config represents ClickHouse output configuration
type Config struct {
	Host          string `mapstructure:"host"`
	Port          int    `mapstructure:"port"`
	Database      string `mapstructure:"database"`
	Username      string `mapstructure:"username"`
	Password      string `mapstructure:"password"`
	Table         string `mapstructure:"table"`
	EventsTable   string `mapstructure:"events_table"` // Optional. Table to read resume RVs from. If empty, resume-from-CH is disabled.
	TLS           bool   `mapstructure:"tls"`
	TLSSkipVerify bool   `mapstructure:"tls_skip_verify"`
	TableVersion  string `mapstructure:"table_version"` // "object_json", "object_string", or "object_normalized" (default: "object_json")
	// VerboseEventLogging, when true, emits one INFO log line per event sent to ClickHouse
	// with kind, namespace, name, uid, and resourceVersion. Useful for validating pipeline
	// completeness; noisy at high throughput. Default: false.
	VerboseEventLogging bool `mapstructure:"verbose_event_logging"`
}

// MetadataV5 represents the metadata Tuple structure for v5 tables
type MetadataV5 struct {
	Name                       *string           `ch:"name"`
	GenerateName               *string           `ch:"generateName"`
	Namespace                  *string           `ch:"namespace"`
	UID                        *string           `ch:"uid"`
	Labels                     map[string]string `ch:"labels"`
	Annotations                map[string]string `ch:"annotations"`
	OwnerReferences            *string           `ch:"ownerReferences"` // JSON string
	Finalizers                 []string          `ch:"finalizers"`
	CreationTimestamp          *time.Time        `ch:"creationTimestamp"`
	DeletionTimestamp          *time.Time        `ch:"deletionTimestamp"`
	DeletionGracePeriodSeconds *int64            `ch:"deletionGracePeriodSeconds"`
	ResourceVersion            *string           `ch:"resourceVersion"`
	Generation                 *int64            `ch:"generation"`
	ManagedFields              *string           `ch:"managedFields"` // JSON string
	SelfLink                   *string           `ch:"selfLink"`
	ClusterName                *string           `ch:"clusterName"`
}

// ObjectV5 represents the object Tuple structure for v5 tables
type ObjectV5 struct {
	APIVersion *string    `ch:"apiVersion"`
	Kind       *string    `ch:"kind"`
	Metadata   MetadataV5 `ch:"metadata"`
	Spec       *string    `ch:"spec"`       // JSON string
	Status     *string    `ch:"status"`     // JSON string
	Data       *string    `ch:"data"`       // JSON string
	Items      *string    `ch:"items"`      // JSON string
	StringData *string    `ch:"stringData"` // JSON string
	BinaryData *string    `ch:"binaryData"` // JSON string
}

// NewClickHouseOutput creates a new ClickHouse output
func NewClickHouseOutput(cfg map[string]interface{}, logger *zap.Logger) (outputs.Output, error) {
	// Parse config with minimal defaults
	config := &Config{
		Port:     9000, // Standard ClickHouse native protocol port
		Username: "",
		Password: "",
		TLS:      false,
	}

	// Required fields
	if host, ok := cfg["host"].(string); ok {
		config.Host = host
	} else {
		return nil, fmt.Errorf("clickhouse host not configured")
	}

	if database, ok := cfg["database"].(string); ok {
		config.Database = database
	} else {
		return nil, fmt.Errorf("clickhouse database not configured")
	}

	if table, ok := cfg["table"].(string); ok {
		config.Table = table
	} else {
		return nil, fmt.Errorf("clickhouse table not configured")
	}

	if eventsTable, ok := cfg["events_table"].(string); ok {
		config.EventsTable = eventsTable
	}

	// Optional fields
	if port, ok := cfg["port"].(int); ok {
		config.Port = port
	} else if port, ok := cfg["port"].(float64); ok {
		config.Port = int(port)
	}

	if username, ok := cfg["username"].(string); ok && username != "" {
		config.Username = username
	} else {
		// Try reading from environment via Viper
		if envUsername := viper.GetString("outputs.clickhouse.username"); envUsername != "" {
			config.Username = envUsername
		}
	}

	if password, ok := cfg["password"].(string); ok && password != "" {
		config.Password = password
	} else {
		// Try reading from environment via Viper
		if envPassword := viper.GetString("outputs.clickhouse.password"); envPassword != "" {
			config.Password = envPassword
		}
	}

	if tls, ok := cfg["tls"].(bool); ok {
		config.TLS = tls
	}

	if tlsSkipVerify, ok := cfg["tls_skip_verify"].(bool); ok {
		config.TLSSkipVerify = tlsSkipVerify
	}

	if verbose, ok := cfg["verbose_event_logging"].(bool); ok {
		config.VerboseEventLogging = verbose
	}

	// Parse table version (object_json, object_string, or object_normalized)
	if tableVersion, ok := cfg["table_version"].(string); ok && tableVersion != "" {
		config.TableVersion = tableVersion
	} else {
		config.TableVersion = "object_json" // Default to object_json for backward compatibility
	}

	// Validate table version
	if config.TableVersion != "object_json" && config.TableVersion != "object_string" && config.TableVersion != "object_normalized" {
		return nil, fmt.Errorf("invalid table_version '%s': must be 'object_json', 'object_string', or 'object_normalized'", config.TableVersion)
	}

	// Validate table identifiers before they are interpolated into queries below.
	if err := validateTableIdentifier(config.Table); err != nil {
		return nil, fmt.Errorf("clickhouse table: %w", err)
	}
	if config.EventsTable != "" {
		if err := validateTableIdentifier(config.EventsTable); err != nil {
			return nil, fmt.Errorf("clickhouse events_table: %w", err)
		}
	}

	// Connect to ClickHouse using native driver
	logger.Info("Connecting to ClickHouse",
		zap.String("host", config.Host),
		zap.Int("port", config.Port),
		zap.String("database", config.Database),
		zap.String("table", config.Table),
		zap.String("table_version", config.TableVersion))

	options := &clickhouse.Options{
		Addr: []string{fmt.Sprintf("%s:%d", config.Host, config.Port)},
		Auth: clickhouse.Auth{
			Database: config.Database,
			Username: config.Username,
			Password: config.Password,
		},
		Settings: clickhouse.Settings{
			"allow_experimental_json_type":  1,
			"json_type_escape_dots_in_keys": 1,
			"async_insert":                  1,
			"wait_for_async_insert":         0,
			"async_insert_busy_timeout_ms":  1000, // increased from 200ms
		},
		DialTimeout:      10 * time.Second,
		MaxOpenConns:     2, // was 10 — fewer connections = fewer independent buffers
		MaxIdleConns:     2,
		ConnMaxLifetime:  time.Hour,
		ConnOpenStrategy: clickhouse.ConnOpenInOrder,
	}

	if config.TLS {
		options.TLS = &tls.Config{
			InsecureSkipVerify: config.TLSSkipVerify,
		}
	}

	conn, err := clickhouse.Open(options)
	if err != nil {
		return nil, fmt.Errorf("failed to open ClickHouse connection: %w", err)
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to ping ClickHouse: %w", err)
	}

	logger.Info("Successfully connected to ClickHouse")

	out := &ClickHouseOutput{
		conn:                conn,
		table:               config.Table,
		eventsTable:         config.EventsTable,
		logger:              logger,
		tableVersion:        config.TableVersion,
		verboseEventLogging: config.VerboseEventLogging,
	}
	out.buildPreparedSQL()
	return out, nil
}

// buildPreparedSQL formats every query template that needs a table identifier
// interpolated. Both c.table and c.eventsTable have already passed
// validateTableIdentifier in NewClickHouseOutput, so the resulting strings are
// safe to pass directly to the driver with ? bound values.
func (c *ClickHouseOutput) buildPreparedSQL() {
	c.insertSQL = fmt.Sprintf(
		"INSERT INTO %s (timestamp, cluster_name, operation, kind, namespace, uid, object)",
		c.table,
	)
	c.sentinelInsertSQL = c.insertSQL // same shape, kept as a distinct name for call-site clarity

	if c.eventsTable == "" {
		return
	}

	const resumeWindow = "INTERVAL 2 HOUR"
	switch c.tableVersion {
	case "object_json":
		c.resumeRVSQL = fmt.Sprintf(`
			SELECT kind,
			       max(object.metadata.resourceVersion::UInt64) AS max_rv
			FROM %s WHERE cluster_name = ? AND timestamp >= now() - %s
			GROUP BY kind`, c.eventsTable, resumeWindow)
		c.clusterPodCountsSQL = fmt.Sprintf(`
			SELECT cluster_name, uniqExact(object.metadata.uid) AS pod_count
			FROM %s
			WHERE object.kind = 'Pod' AND timestamp >= ? AND timestamp < ?
			GROUP BY cluster_name`, c.eventsTable)
	case "object_string":
		c.resumeRVSQL = fmt.Sprintf(`
			SELECT kind,
			       max(toUInt64OrZero(
			           JSONExtractString(object, 'metadata', 'resourceVersion')
			       )) AS max_rv
			FROM %s WHERE cluster_name = ? AND timestamp >= now() - %s
			GROUP BY kind`, c.eventsTable, resumeWindow)
		c.clusterPodCountsSQL = fmt.Sprintf(`
			SELECT cluster_name,
			       uniqExact(JSONExtractString(object, 'metadata', 'uid')) AS pod_count
			FROM %s
			WHERE JSONExtractString(object, 'kind') = 'Pod'
			  AND timestamp >= ? AND timestamp < ?
			GROUP BY cluster_name`, c.eventsTable)
	case "object_normalized":
		c.resumeRVSQL = fmt.Sprintf(`
			SELECT kind,
			       max(toUInt64OrZero(toString(
			           tupleElement(tupleElement(object, 3), 12)
			       ))) AS max_rv
			FROM %s WHERE cluster_name = ? AND timestamp >= now() - %s
			GROUP BY kind`, c.eventsTable, resumeWindow)
		c.clusterPodCountsSQL = fmt.Sprintf(`
			SELECT cluster_name,
			       uniqExact(tupleElement(tupleElement(object, 3), 4)) AS pod_count
			FROM %s
			WHERE tupleElement(object, 2) = 'Pod'
			  AND timestamp >= ? AND timestamp < ?
			GROUP BY cluster_name`, c.eventsTable)
	}

	c.lastSeedTimestampSQL = fmt.Sprintf(`
		SELECT kind, max(timestamp)
		FROM %s
		WHERE timestamp >= now() - INTERVAL ? SECOND
		  AND operation = 'SEEDED'
		  AND cluster_name = ?
		GROUP BY kind`, c.eventsTable)
}

// extractIdentity pulls kind, metadata.namespace, metadata.uid,
// metadata.name, and metadata.resourceVersion from a Kubernetes object map.
// Missing fields return empty strings.
func extractIdentity(object map[string]interface{}) (kind, namespace, uid, name, resourceVersion string) {
	if k, ok := object["kind"].(string); ok {
		kind = k
	}
	if meta, ok := object["metadata"].(map[string]interface{}); ok {
		if ns, ok := meta["namespace"].(string); ok {
			namespace = ns
		}
		if u, ok := meta["uid"].(string); ok {
			uid = u
		}
		if n, ok := meta["name"].(string); ok {
			name = n
		}
		if rv, ok := meta["resourceVersion"].(string); ok {
			resourceVersion = rv
		}
	}
	return
}

// Send sends events to ClickHouse using batch insert
func (c *ClickHouseOutput) Send(ctx context.Context, events []types.Event) error {
	if len(events) == 0 {
		return nil
	}

	// Begin batch
	batch, err := c.conn.PrepareBatch(ctx, c.insertSQL)
	if err != nil {
		return fmt.Errorf("failed to prepare batch: %w", err)
	}

	// Add events to batch
	for i, event := range events {
		object := event.Object
		if object == nil {
			object = make(map[string]interface{})
		}

		kind, namespace, uid, name, resourceVersion := extractIdentity(object)

		if c.verboseEventLogging {
			c.logger.Info("Queuing event for ClickHouse",
				zap.String("cluster", event.ClusterName),
				zap.String("operation", event.Operation),
				zap.String("kind", kind),
				zap.String("namespace", namespace),
				zap.String("name", name),
				zap.String("uid", uid),
				zap.String("resourceVersion", resourceVersion))
		}

		var objectData interface{}
		var err error

		// Format object based on table version
		switch c.tableVersion {
		case "object_normalized":
			// object_normalized: Structured format with Tuple
			objectData, err = c.buildStructuredObject(object)
			if err != nil {
				c.logger.Error("Failed to build structured object for V5",
					zap.Int("batch_position", i),
					zap.String("cluster", event.ClusterName),
					zap.Error(err))
				return fmt.Errorf("failed to build structured object %d: %w", i, err)
			}
		case "object_json", "object_string":
			// object_json/object_string: Raw JSON format
			objectData, err = json.Marshal(object)
			if err != nil {
				c.logger.Error("Failed to marshal object to JSON",
					zap.Int("batch_position", i),
					zap.String("cluster", event.ClusterName),
					zap.Error(err))
				return fmt.Errorf("failed to marshal event %d: %w", i, err)
			}
		default:
			return fmt.Errorf("unsupported table version: %s", c.tableVersion)
		}

		// Append to batch
		err = batch.Append(
			event.Timestamp,
			event.ClusterName,
			event.Operation,
			kind,
			namespace,
			uid,
			objectData,
		)
		if err != nil {
			c.logger.Error("Failed to append event to batch",
				zap.Int("batch_position", i),
				zap.String("cluster", event.ClusterName),
				zap.Error(err))
			return fmt.Errorf("failed to append event %d: %w", i, err)
		}
	}

	// Send batch
	if err := batch.Send(); err != nil {
		return fmt.Errorf("failed to send batch: %w", err)
	}

	c.logger.Info("Successfully sent events to ClickHouse",
		zap.Int("count", len(events)),
		zap.String("table_version", c.tableVersion))

	return nil
}

// GetResumeRVsByKind returns the highest resourceVersion per Kubernetes kind
// seen in the output table for the given cluster. Used on startup to open
// per-resource WATCH cursors, skipping the initial LIST for already-seeded
// resource types.
//
// Returns an empty map (not an error) when the cluster has no data in CH.
// Returns an error only on CH connectivity or query failure.
func (c *ClickHouseOutput) GetResumeRVsByKind(ctx context.Context, cluster string) (map[string]string, error) {
	if c.eventsTable == "" {
		return nil, fmt.Errorf("events_table not configured; resume from ClickHouse disabled")
	}
	if c.resumeRVSQL == "" {
		return nil, fmt.Errorf("unsupported table version: %s", c.tableVersion)
	}

	// Time bound on the prebuilt query is INTERVAL 2 HOUR: K8s expires watch RVs
	// within minutes (etcd compaction + watch cache window), so an RV from >2h
	// ago will 410-Gone anyway. The bound also lets CH prune partitions
	// (PARTITION BY toYYYYMMDD(timestamp)). Idle kinds fall back to the global
	// init RV (tier 3) or a full LIST.
	rows, err := c.conn.Query(ctx, c.resumeRVSQL, cluster)
	if err != nil {
		return nil, fmt.Errorf("GetResumeRVsByKind for cluster %q: %w", cluster, err)
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var kind string
		var maxRV uint64
		if err := rows.Scan(&kind, &maxRV); err != nil {
			return nil, fmt.Errorf("scan resume RV row: %w", err)
		}
		if kind != "" && maxRV > 0 {
			result[kind] = strconv.FormatUint(maxRV, 10)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("resume RV rows error: %w", err)
	}
	return result, nil
}

// WriteListCompleteSentinel writes a SEEDED sentinel row for the given kind and
// snapshot resourceVersion directly to ClickHouse (bypasses the Manager buffer).
// Must only be called after the caller has flushed all EXISTING events for this
// kind through the output manager (Manager.Flush), so the sentinel is only
// present when the full LIST is durable.
func (c *ClickHouseOutput) WriteListCompleteSentinel(ctx context.Context, cluster, kind, snapshotRV string) error {
	if c.table == "" {
		return fmt.Errorf("table not configured")
	}

	batch, err := c.conn.PrepareBatch(ctx, c.sentinelInsertSQL)
	if err != nil {
		return fmt.Errorf("WriteListCompleteSentinel prepare: %w", err)
	}

	sentinel := map[string]interface{}{
		"kind": kind,
		"metadata": map[string]interface{}{
			"resourceVersion": snapshotRV,
		},
	}

	var objectData interface{}
	switch c.tableVersion {
	case "object_normalized":
		objectData, err = c.buildStructuredObject(sentinel)
		if err != nil {
			return fmt.Errorf("WriteListCompleteSentinel build object: %w", err)
		}
	case "object_json", "object_string":
		objectData, err = json.Marshal(sentinel)
		if err != nil {
			return fmt.Errorf("WriteListCompleteSentinel marshal: %w", err)
		}
	default:
		return fmt.Errorf("unsupported table version: %s", c.tableVersion)
	}

	if err := batch.Append(time.Now(), cluster, "SEEDED", kind, "", "", objectData); err != nil {
		return fmt.Errorf("WriteListCompleteSentinel append: %w", err)
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("WriteListCompleteSentinel send: %w", err)
	}

	c.logger.Info("Wrote LIST complete sentinel",
		zap.String("cluster", cluster),
		zap.String("kind", kind),
		zap.String("snapshotRV", snapshotRV))

	return nil
}

// GetLastSeedTimestampsByKind returns the most recent SEEDED row's timestamp
// per kind for the given cluster, bounded to the past `window`. Kinds whose
// most recent SEEDED is older than `window` are absent from the result; the
// caller treats those as "fire resync immediately."
//
// Used by the watcher at startup to anchor P1/P2/P3 resync schedules in
// real elapsed time rather than process uptime — restart-resilient.
func (c *ClickHouseOutput) GetLastSeedTimestampsByKind(ctx context.Context, cluster string, window time.Duration) (map[string]time.Time, error) {
	if c.eventsTable == "" {
		return nil, fmt.Errorf("events_table not configured; last-seed timestamps disabled")
	}
	if window <= 0 {
		return nil, fmt.Errorf("window must be > 0")
	}

	// Pass the window in seconds via the driver (uniform across CH versions).
	windowSeconds := int64(window.Seconds())

	rows, err := c.conn.Query(ctx, c.lastSeedTimestampSQL, windowSeconds, cluster)
	if err != nil {
		return nil, fmt.Errorf("GetLastSeedTimestampsByKind for cluster %q: %w", cluster, err)
	}
	defer rows.Close()

	result := make(map[string]time.Time)
	for rows.Next() {
		var kind string
		var ts time.Time
		if err := rows.Scan(&kind, &ts); err != nil {
			return nil, fmt.Errorf("scan last-seed row: %w", err)
		}
		if kind != "" && !ts.IsZero() {
			result[kind] = ts
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("last-seed rows error: %w", err)
	}
	return result, nil
}

// GetClusterPodCounts returns the number of unique pods per cluster observed
// in the events table within the given time window. Uses uniqExact(uid) to
// count distinct pods even if they appear multiple times in the window.
// Used by the shard manager to compute pod-count-weighted shard assignment.
func (c *ClickHouseOutput) GetClusterPodCounts(ctx context.Context, start, end time.Time) (map[string]int64, error) {
	if c.eventsTable == "" {
		return nil, fmt.Errorf("events_table not configured")
	}
	if c.clusterPodCountsSQL == "" {
		return nil, fmt.Errorf("unsupported table version: %s", c.tableVersion)
	}

	// object_normalized layout: object tuple = (apiVersion, kind, metadata, ...);
	// metadata tuple = (name, generateName, namespace, uid, ...) — uid is element 4.
	rows, err := c.conn.Query(ctx, c.clusterPodCountsSQL, start, end)
	if err != nil {
		return nil, fmt.Errorf("GetClusterPodCounts: %w", err)
	}
	defer rows.Close()

	result := make(map[string]int64)
	for rows.Next() {
		var cluster string
		var count uint64
		if err := rows.Scan(&cluster, &count); err != nil {
			return nil, fmt.Errorf("scan pod count row: %w", err)
		}
		if cluster != "" {
			result[cluster] = int64(count)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pod count rows error: %w", err)
	}
	return result, nil
}

// Close closes the ClickHouse connection
func (c *ClickHouseOutput) Close() error {
	c.logger.Info("Closing ClickHouse connection")
	return c.conn.Close()
}

// Name returns the output name
func (c *ClickHouseOutput) Name() string {
	return "clickhouse"
}

// buildStructuredObject converts a Kubernetes object to V5 structured format
// Returns a []any (clickhouse.Tuple) with nested tuples and proper JSON field handling
func (c *ClickHouseOutput) buildStructuredObject(obj map[string]interface{}) (interface{}, error) {
	// Extract apiVersion and kind
	var apiVersion, kind *string
	if v, ok := obj["apiVersion"].(string); ok && v != "" {
		apiVersion = &v
	}
	if v, ok := obj["kind"].(string); ok && v != "" {
		kind = &v
	}

	// Extract metadata as a nested []any (Tuple)
	metadata := c.extractMetadataSlice(obj)

	// Extract nullable JSON fields (spec, status, items) - use []byte("null") for null values
	// Extract Map fields (data, stringData, binaryData) - use empty maps for missing values
	nullJSON := []byte("null")
	var spec, status, items interface{}

	if v, ok := obj["spec"]; ok && v != nil {
		if b, err := json.Marshal(v); err == nil {
			spec = b
		} else {
			spec = nullJSON
		}
	} else {
		spec = nullJSON
	}

	if v, ok := obj["status"]; ok && v != nil {
		if b, err := json.Marshal(v); err == nil {
			status = b
		} else {
			status = nullJSON
		}
	} else {
		status = nullJSON
	}

	if v, ok := obj["items"]; ok && v != nil {
		if b, err := json.Marshal(v); err == nil {
			items = b
		} else {
			items = nullJSON
		}
	} else {
		items = nullJSON
	}

	// Extract data fields as Map(String, String) for ConfigMaps and Secrets
	data := c.extractMap(obj, "data")
	stringData := c.extractMap(obj, "stringData")
	binaryData := c.extractMap(obj, "binaryData")

	// Return as []any (clickhouse.Tuple) in exact order
	return []any{
		apiVersion,
		kind,
		metadata, // nested []any
		spec,
		status,
		data,
		items,
		stringData,
		binaryData,
	}, nil
}

// extractMetadataSlice extracts and structures metadata fields into a []any (Tuple)
func (c *ClickHouseOutput) extractMetadataSlice(obj map[string]interface{}) []any {
	metadataRaw, ok := obj["metadata"].(map[string]interface{})
	if !ok {
		// Return empty values in correct order
		return []any{
			(*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), // name, generateName, namespace, uid
			make(map[string]string),                             // labels
			make(map[string]string),                             // annotations
			nil,                                                 // ownerReferences (Nullable JSON)
			[]string{},                                          // finalizers
			(*time.Time)(nil), (*time.Time)(nil), (*int64)(nil), // timestamps, deletionGracePeriodSeconds
			(*string)(nil), (*int64)(nil), // resourceVersion, generation
			nil,                            // managedFields (Nullable JSON)
			(*string)(nil), (*string)(nil), // selfLink, clusterName
		}
	}

	// Extract simple string fields
	var name, generateName, namespace, uid, resourceVersion, selfLink, clusterName *string
	if v, ok := metadataRaw["name"].(string); ok && v != "" {
		name = &v
	}
	if v, ok := metadataRaw["generateName"].(string); ok && v != "" {
		generateName = &v
	}
	if v, ok := metadataRaw["namespace"].(string); ok && v != "" {
		namespace = &v
	}
	if v, ok := metadataRaw["uid"].(string); ok && v != "" {
		uid = &v
	}
	if v, ok := metadataRaw["resourceVersion"].(string); ok && v != "" {
		resourceVersion = &v
	}
	if v, ok := metadataRaw["selfLink"].(string); ok && v != "" {
		selfLink = &v
	}
	if v, ok := metadataRaw["clusterName"].(string); ok && v != "" {
		clusterName = &v
	}

	// Extract numeric fields
	var generation, deletionGracePeriodSeconds *int64
	if gen, ok := metadataRaw["generation"].(int64); ok {
		generation = &gen
	} else if genFloat, ok := metadataRaw["generation"].(float64); ok {
		gen := int64(genFloat)
		generation = &gen
	}
	if dgs, ok := metadataRaw["deletionGracePeriodSeconds"].(int64); ok {
		deletionGracePeriodSeconds = &dgs
	} else if dgsFloat, ok := metadataRaw["deletionGracePeriodSeconds"].(float64); ok {
		dgs := int64(dgsFloat)
		deletionGracePeriodSeconds = &dgs
	}

	// Extract timestamps
	var creationTimestamp, deletionTimestamp *time.Time
	if ts := c.parseTimestamp(metadataRaw, "creationTimestamp"); ts != nil {
		if t, ok := ts.(time.Time); ok {
			creationTimestamp = &t
		}
	}
	if ts := c.parseTimestamp(metadataRaw, "deletionTimestamp"); ts != nil {
		if t, ok := ts.(time.Time); ok {
			deletionTimestamp = &t
		}
	}

	// Extract maps and arrays
	labels := c.extractMap(metadataRaw, "labels")
	annotations := c.extractMap(metadataRaw, "annotations")
	finalizers := c.extractStringArray(metadataRaw, "finalizers")

	// Extract ownerReferences as Array(Tuple(...))
	ownerReferences := make([][]interface{}, 0)
	if ownerRefsRaw, ok := metadataRaw["ownerReferences"].([]interface{}); ok && len(ownerRefsRaw) > 0 {
		for _, refRaw := range ownerRefsRaw {
			if refMap, ok := refRaw.(map[string]interface{}); ok {
				var apiVersion, kind, name, uid *string
				var controller, blockOwnerDeletion *uint8

				if v, ok := refMap["apiVersion"].(string); ok && v != "" {
					apiVersion = &v
				}
				if v, ok := refMap["kind"].(string); ok && v != "" {
					kind = &v
				}
				if v, ok := refMap["name"].(string); ok && v != "" {
					name = &v
				}
				if v, ok := refMap["uid"].(string); ok && v != "" {
					uid = &v
				}
				if v, ok := refMap["controller"].(bool); ok {
					val := uint8(0)
					if v {
						val = 1
					}
					controller = &val
				}
				if v, ok := refMap["blockOwnerDeletion"].(bool); ok {
					val := uint8(0)
					if v {
						val = 1
					}
					blockOwnerDeletion = &val
				}

				ownerReferences = append(ownerReferences, []interface{}{
					apiVersion, kind, name, uid, controller, blockOwnerDeletion,
				})
			}
		}
	}

	// Extract managedFields as Array(Tuple(...))
	managedFields := make([][]interface{}, 0)
	if managedFieldsRaw, ok := metadataRaw["managedFields"].([]interface{}); ok && len(managedFieldsRaw) > 0 {
		for _, fieldRaw := range managedFieldsRaw {
			if fieldMap, ok := fieldRaw.(map[string]interface{}); ok {
				var manager, operation, apiVersion, fieldsType, fieldsV1Str, subresource *string
				var timeVal *time.Time

				if v, ok := fieldMap["manager"].(string); ok && v != "" {
					manager = &v
				}
				if v, ok := fieldMap["operation"].(string); ok && v != "" {
					operation = &v
				}
				if v, ok := fieldMap["apiVersion"].(string); ok && v != "" {
					apiVersion = &v
				}
				if v, ok := fieldMap["fieldsType"].(string); ok && v != "" {
					fieldsType = &v
				}
				if v, ok := fieldMap["subresource"].(string); ok && v != "" {
					subresource = &v
				}

				// Marshal fieldsV1 to JSON string
				if v, ok := fieldMap["fieldsV1"]; ok && v != nil {
					if b, err := json.Marshal(v); err == nil {
						s := string(b)
						fieldsV1Str = &s
					}
				}

				// Parse time
				if ts := c.parseTimestamp(fieldMap, "time"); ts != nil {
					if t, ok := ts.(time.Time); ok {
						timeVal = &t
					}
				}

				managedFields = append(managedFields, []interface{}{
					manager, operation, apiVersion, timeVal, fieldsType, fieldsV1Str, subresource,
				})
			}
		}
	}

	// Return as []any (Tuple) in exact order
	return []any{
		name,
		generateName,
		namespace,
		uid,
		labels,
		annotations,
		ownerReferences,
		finalizers,
		creationTimestamp,
		deletionTimestamp,
		deletionGracePeriodSeconds,
		resourceVersion,
		generation,
		managedFields,
		selfLink,
		clusterName,
	}
}

// extractMetadataStruct extracts and structures metadata fields into a MetadataV5 struct
func (c *ClickHouseOutput) extractMetadataStruct(obj map[string]interface{}) MetadataV5 {
	metadataRaw, ok := obj["metadata"].(map[string]interface{})
	if !ok {
		// Return empty metadata struct
		return MetadataV5{
			Labels:      make(map[string]string),
			Annotations: make(map[string]string),
			Finalizers:  []string{},
		}
	}

	// Extract simple string fields
	var name, generateName, namespace, uid, resourceVersion, selfLink, clusterName *string
	if v, ok := metadataRaw["name"].(string); ok && v != "" {
		name = &v
	}
	if v, ok := metadataRaw["generateName"].(string); ok && v != "" {
		generateName = &v
	}
	if v, ok := metadataRaw["namespace"].(string); ok && v != "" {
		namespace = &v
	}
	if v, ok := metadataRaw["uid"].(string); ok && v != "" {
		uid = &v
	}
	if v, ok := metadataRaw["resourceVersion"].(string); ok && v != "" {
		resourceVersion = &v
	}
	if v, ok := metadataRaw["selfLink"].(string); ok && v != "" {
		selfLink = &v
	}
	if v, ok := metadataRaw["clusterName"].(string); ok && v != "" {
		clusterName = &v
	}

	// Extract numeric fields
	var generation, deletionGracePeriodSeconds *int64
	if gen, ok := metadataRaw["generation"].(int64); ok {
		generation = &gen
	} else if genFloat, ok := metadataRaw["generation"].(float64); ok {
		gen := int64(genFloat)
		generation = &gen
	}
	if dgs, ok := metadataRaw["deletionGracePeriodSeconds"].(int64); ok {
		deletionGracePeriodSeconds = &dgs
	} else if dgsFloat, ok := metadataRaw["deletionGracePeriodSeconds"].(float64); ok {
		dgs := int64(dgsFloat)
		deletionGracePeriodSeconds = &dgs
	}

	// Extract timestamps
	var creationTimestamp, deletionTimestamp *time.Time
	if ts := c.parseTimestamp(metadataRaw, "creationTimestamp"); ts != nil {
		if t, ok := ts.(time.Time); ok {
			creationTimestamp = &t
		}
	}
	if ts := c.parseTimestamp(metadataRaw, "deletionTimestamp"); ts != nil {
		if t, ok := ts.(time.Time); ok {
			deletionTimestamp = &t
		}
	}

	// Extract maps and arrays
	labels := c.extractMap(metadataRaw, "labels")
	annotations := c.extractMap(metadataRaw, "annotations")
	finalizers := c.extractStringArray(metadataRaw, "finalizers")

	// Extract JSON fields as strings
	var ownerReferences, managedFields *string
	if v := c.extractJSONField(metadataRaw, "ownerReferences"); v != nil {
		s := v.(string)
		ownerReferences = &s
	}
	if v := c.extractJSONField(metadataRaw, "managedFields"); v != nil {
		s := v.(string)
		managedFields = &s
	}

	return MetadataV5{
		Name:                       name,
		GenerateName:               generateName,
		Namespace:                  namespace,
		UID:                        uid,
		Labels:                     labels,
		Annotations:                annotations,
		OwnerReferences:            ownerReferences,
		Finalizers:                 finalizers,
		CreationTimestamp:          creationTimestamp,
		DeletionTimestamp:          deletionTimestamp,
		DeletionGracePeriodSeconds: deletionGracePeriodSeconds,
		ResourceVersion:            resourceVersion,
		Generation:                 generation,
		ManagedFields:              managedFields,
		SelfLink:                   selfLink,
		ClusterName:                clusterName,
	}
}

// extractMetadata extracts and structures metadata fields (old slice-based version, kept for compatibility)
// Returns a slice matching the metadata Tuple structure in exact order
// Uses pointers for Nullable fields as required by ClickHouse
func (c *ClickHouseOutput) extractMetadata(obj map[string]interface{}) []interface{} {
	metadataRaw, ok := obj["metadata"].(map[string]interface{})
	if !ok {
		// Return empty values in correct order if metadata is missing
		return []interface{}{
			(*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), // name, generateName, namespace, uid
			make(map[string]string),                             // labels
			make(map[string]string),                             // annotations
			(*string)(nil),                                      // ownerReferences (JSON as string)
			[]string{},                                          // finalizers
			(*time.Time)(nil), (*time.Time)(nil), (*int64)(nil), // creationTimestamp, deletionTimestamp, deletionGracePeriodSeconds
			(*string)(nil), (*int64)(nil), // resourceVersion, generation
			(*string)(nil),                 // managedFields (JSON as string)
			(*string)(nil), (*string)(nil), // selfLink, clusterName
		}
	}

	// Extract simple string fields as pointers
	var name, generateName, namespace, uid, resourceVersion, selfLink, clusterName *string
	if v, ok := metadataRaw["name"].(string); ok && v != "" {
		name = &v
	}
	if v, ok := metadataRaw["generateName"].(string); ok && v != "" {
		generateName = &v
	}
	if v, ok := metadataRaw["namespace"].(string); ok && v != "" {
		namespace = &v
	}
	if v, ok := metadataRaw["uid"].(string); ok && v != "" {
		uid = &v
	}
	if v, ok := metadataRaw["resourceVersion"].(string); ok && v != "" {
		resourceVersion = &v
	}
	if v, ok := metadataRaw["selfLink"].(string); ok && v != "" {
		selfLink = &v
	}
	if v, ok := metadataRaw["clusterName"].(string); ok && v != "" {
		clusterName = &v
	}

	// Extract numeric fields as pointers
	var generation, deletionGracePeriodSeconds *int64
	if gen, ok := metadataRaw["generation"].(int64); ok {
		generation = &gen
	} else if genFloat, ok := metadataRaw["generation"].(float64); ok {
		gen := int64(genFloat)
		generation = &gen
	}
	if dgs, ok := metadataRaw["deletionGracePeriodSeconds"].(int64); ok {
		deletionGracePeriodSeconds = &dgs
	} else if dgsFloat, ok := metadataRaw["deletionGracePeriodSeconds"].(float64); ok {
		dgs := int64(dgsFloat)
		deletionGracePeriodSeconds = &dgs
	}

	// Extract timestamps as pointers
	var creationTimestamp, deletionTimestamp *time.Time
	if ts := c.parseTimestamp(metadataRaw, "creationTimestamp"); ts != nil {
		if t, ok := ts.(time.Time); ok {
			creationTimestamp = &t
		}
	}
	if ts := c.parseTimestamp(metadataRaw, "deletionTimestamp"); ts != nil {
		if t, ok := ts.(time.Time); ok {
			deletionTimestamp = &t
		}
	}

	// Extract labels and annotations as maps (not nullable)
	labels := c.extractMap(metadataRaw, "labels")
	annotations := c.extractMap(metadataRaw, "annotations")

	// Extract finalizers as array (not nullable)
	finalizers := c.extractStringArray(metadataRaw, "finalizers")

	// ownerReferences and managedFields as JSON strings for Nullable(JSON) columns
	ownerReferences := c.extractJSONField(metadataRaw, "ownerReferences")
	managedFields := c.extractJSONField(metadataRaw, "managedFields")

	// Return as slice in exact order matching the Tuple schema:
	// Tuple(name, generateName, namespace, uid, labels, annotations, ownerReferences, finalizers,
	//       creationTimestamp, deletionTimestamp, deletionGracePeriodSeconds,
	//       resourceVersion, generation, managedFields, selfLink, clusterName)
	return []interface{}{
		name,
		generateName,
		namespace,
		uid,
		labels,
		annotations,
		ownerReferences,
		finalizers,
		creationTimestamp,
		deletionTimestamp,
		deletionGracePeriodSeconds,
		resourceVersion,
		generation,
		managedFields,
		selfLink,
		clusterName,
	}
}

// extractJSONField extracts a field and marshals it to a JSON string for Nullable(JSON) columns in Tuples
// Returns nil for missing/null fields, or a JSON string for present fields
func (c *ClickHouseOutput) extractJSONField(obj map[string]interface{}, field string) interface{} {
	if val, ok := obj[field]; ok && val != nil {
		jsonBytes, err := json.Marshal(val)
		if err != nil {
			c.logger.Warn("Failed to marshal field to JSON",
				zap.String("field", field),
				zap.Error(err))
			return nil
		}
		return string(jsonBytes)
	}
	return nil
}

// extractField extracts a field and marshals it to JSON bytes for ClickHouse JSON columns
// JSON columns in Tuples need JSON strings for complex nested structures
// extractField extracts a field and marshals it to JSON bytes for ClickHouse JSON columns
// JSON columns in Tuples need JSON strings for complex nested structures
func (c *ClickHouseOutput) extractField(obj map[string]interface{}, field string) interface{} {
	if val, ok := obj[field]; ok {
		// Marshal to JSON string for ClickHouse JSON column in Tuple
		jsonBytes, err := json.Marshal(val)
		if err != nil {
			c.logger.Warn("Failed to marshal field to JSON",
				zap.String("field", field),
				zap.Error(err))
			return nil
		}
		return string(jsonBytes) // Return as string for JSON column
	}
	return nil
}

// extractFieldRaw extracts a field and marshals it to JSON string for JSON fields in Tuples
// JSON columns in Tuples need JSON strings for complex nested structures
func (c *ClickHouseOutput) extractFieldRaw(obj map[string]interface{}, field string) interface{} {
	if val, ok := obj[field]; ok {
		// Marshal to JSON string for ClickHouse JSON column in Tuple
		jsonBytes, err := json.Marshal(val)
		if err != nil {
			c.logger.Warn("Failed to marshal field to JSON",
				zap.String("field", field),
				zap.Error(err))
			return nil
		}
		return string(jsonBytes) // Return as string for JSON column
	}
	return nil
}

// extractMap extracts a map field
func (c *ClickHouseOutput) extractMap(obj map[string]interface{}, field string) map[string]string {
	if val, ok := obj[field].(map[string]interface{}); ok {
		result := make(map[string]string)
		for k, v := range val {
			if strVal, ok := v.(string); ok {
				result[k] = strVal
			}
		}
		return result
	}
	// Return empty map instead of nil (ClickHouse Maps cannot be nullable)
	return make(map[string]string)
}

// extractStringArray extracts an array of strings
func (c *ClickHouseOutput) extractStringArray(obj map[string]interface{}, field string) []string {
	if val, ok := obj[field].([]interface{}); ok {
		result := make([]string, 0, len(val))
		for _, item := range val {
			if strVal, ok := item.(string); ok {
				result = append(result, strVal)
			}
		}
		return result
	}
	// Return empty array instead of nil (ClickHouse Arrays cannot be nullable)
	return []string{}
}

// parseTimestamp parses a timestamp field from metadata
func (c *ClickHouseOutput) parseTimestamp(obj map[string]interface{}, field string) interface{} {
	if val, ok := obj[field].(string); ok && val != "" {
		// Parse RFC3339 timestamp
		t, err := time.Parse(time.RFC3339, val)
		if err == nil {
			return t
		}
		c.logger.Warn("Failed to parse timestamp",
			zap.String("field", field),
			zap.String("value", val),
			zap.Error(err))
	}
	return nil
}
