package config

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/viper"
)

// DeploymentMode defines how the agent is deployed
type DeploymentMode string

const (
	// ModeInCluster indicates the agent runs inside a Kubernetes cluster
	ModeInCluster DeploymentMode = "in-cluster"
	// ModeMultiCluster indicates the agent runs externally and connects to multiple clusters
	ModeMultiCluster DeploymentMode = "multi-cluster"
)

// Config represents the agent configuration
type Config struct {
	// Mode defines the deployment mode (in-cluster or multi-cluster)
	Mode     DeploymentMode  `mapstructure:"mode"`
	Clusters []ClusterConfig `mapstructure:"clusters"`
	// Vault configuration for dynamic cluster discovery
	Vault              *VaultConfig             `mapstructure:"vault"`
	Outputs            []OutputConfig           `mapstructure:"outputs"`
	Buffer             BufferConfig             `mapstructure:"buffer"`
	ConcurrencyControl ConcurrencyControlConfig `mapstructure:"concurrency_control"`
	// DefaultWatchConfig provides watch settings applied to clusters that do not
	// specify their own (e.g. Vault-discovered clusters). Values here override
	// the compiled-in defaults.
	DefaultWatchConfig WatchConfig `mapstructure:"default_watch_config"`
	// Shard controls which subset of clusters this agent instance handles.
	// Nil or Total <= 1 disables sharding (agent handles all clusters).
	Shard *ShardConfig `mapstructure:"shard"`
	// StatusLog controls the periodic operator-facing status log emitted by
	// the watcher manager (cluster summary lines + optional per-kind detail).
	StatusLog StatusLogConfig `mapstructure:"status_log"`
}

// StatusLogConfig controls the periodic status log.
type StatusLogConfig struct {
	// Enabled toggles the entire status reporter goroutine. Default: true.
	Enabled bool `mapstructure:"enabled"`
	// Interval between status report cycles. Default: 5m.
	Interval time.Duration `mapstructure:"interval"`
	// PerKindDetail emits one log line per (cluster, kind) that has sent at
	// least one event in the last 24h. Silent kinds and kinds whose last
	// event is older than 24h are dropped — the per-cluster summary line
	// still reflects the full configured kind count. Default: true.
	PerKindDetail bool `mapstructure:"per_kind_detail"`
}

// ClusterConfig contains Kubernetes cluster configuration
type ClusterConfig struct {
	Name       string `mapstructure:"name"`
	Kubeconfig string `mapstructure:"kubeconfig"`
	Context    string `mapstructure:"context"`
	// Service account authentication (alternative to kubeconfig)
	ServiceAccount *ServiceAccountAuth `mapstructure:"service_account"`
	// Resources to watch - if empty, watches all
	Resources []ResourceFilter `mapstructure:"resources"`
	// Namespaces to watch - if empty, watches all
	Namespaces []string `mapstructure:"namespaces"`
	// Watch configuration for raw watch endpoints
	WatchConfig WatchConfig `mapstructure:"watch_config"`
}

// ServiceAccountAuth contains service account token authentication configuration
type ServiceAccountAuth struct {
	// API server URL (e.g., https://cluster-api.example.com:6443)
	Server string `mapstructure:"server"`
	// Service account token
	Token string `mapstructure:"token"`
	// Path to service account token file (alternative to inline token)
	TokenFile string `mapstructure:"token_file"`
	// CA certificate data (base64 encoded)
	CAData string `mapstructure:"ca_data"`
	// Path to CA certificate file (alternative to inline ca_data)
	CAFile string `mapstructure:"ca_file"`
	// Skip TLS verification (insecure, not recommended for production)
	Insecure bool `mapstructure:"insecure"`
}

// VaultConfig contains Vault configuration for dynamic cluster discovery
type VaultConfig struct {
	// Vault server address (e.g., https://vault.example.com:8200)
	Address string `mapstructure:"address"`
	// Authentication method: token, kubernetes, approle
	AuthMethod string `mapstructure:"auth_method"`
	// Token for token auth
	Token string `mapstructure:"token"`
	// Token file path (alternative to inline token)
	TokenFile string `mapstructure:"token_file"`
	// Kubernetes auth configuration
	KubernetesAuth *VaultKubernetesAuth `mapstructure:"kubernetes_auth"`
	// AppRole auth configuration
	AppRoleAuth *VaultAppRoleAuth `mapstructure:"approle_auth"`
	// Base path in Vault where cluster credentials are stored (e.g., secret/data/kseer/clusters)
	SecretPath string `mapstructure:"secret_path"`
	// Refresh interval for checking new clusters (default: 5m)
	RefreshInterval time.Duration `mapstructure:"refresh_interval"`
	// Skip TLS verification (not recommended for production)
	TLSSkipVerify bool `mapstructure:"tls_skip_verify"`
	// CA certificate for Vault TLS
	CACert string `mapstructure:"ca_cert"`
	// Namespace for Vault Enterprise
	Namespace string `mapstructure:"namespace"`
}

// VaultKubernetesAuth contains Kubernetes auth method configuration
type VaultKubernetesAuth struct {
	// Role name for Kubernetes auth
	Role string `mapstructure:"role"`
	// Path to service account JWT token (default: /var/run/secrets/kubernetes.io/serviceaccount/token)
	TokenPath string `mapstructure:"token_path"`
	// Mount path for Kubernetes auth (default: kubernetes)
	MountPath string `mapstructure:"mount_path"`
}

// VaultAppRoleAuth contains AppRole auth method configuration
type VaultAppRoleAuth struct {
	// Role ID
	RoleID string `mapstructure:"role_id"`
	// Role ID file path (alternative to inline role_id)
	RoleIDFile string `mapstructure:"role_id_file"`
	// Secret ID
	SecretID string `mapstructure:"secret_id"`
	// Secret ID file path
	SecretIDFile string `mapstructure:"secret_id_file"`
	// Mount path for AppRole auth (default: approle)
	MountPath string `mapstructure:"mount_path"`
}

// WatchConfig defines watch endpoint configuration
type WatchConfig struct {
	// Initial backoff for watch reconnection (default: 1s)
	InitialBackoff time.Duration `mapstructure:"initial_backoff"`
	// Max backoff for watch reconnection (default: 5m)
	MaxBackoff time.Duration `mapstructure:"max_backoff"`
	// Watch timeout - how long before server closes connection (default: 10m)
	TimeoutSeconds int `mapstructure:"timeout_seconds"`
	// Enable watch bookmarks for better reconnection (default: true)
	AllowBookmarks bool `mapstructure:"allow_bookmarks"`
	// ListPageSize controls pagination for initial LIST operations (default: 500, -1 = disabled)
	ListPageSize int64 `mapstructure:"list_page_size"`
	// Per-cluster concurrency limit (inherited from global config)
	MaxListsPerCluster int `mapstructure:"max_lists_per_cluster"`
	// ResyncInterval triggers a periodic full re-LIST of all resources.
	// When the interval elapses, each per-GVR watch closes and re-enters the
	// LIST→WATCH cycle, sending all current objects as EXISTING. Default: 0 (disabled).
	ResyncInterval time.Duration `mapstructure:"resync_interval"`
	// SeederQueryRetries is the number of additional attempts (beyond the
	// first) for ClickHouse seeder queries at watcher startup. Used by the
	// resume-RV check, the seed-completion check, and the last-seed timestamp
	// check that anchors P1/P2/P3 schedules across restarts. Default: 2 (so
	// 3 total attempts).
	SeederQueryRetries int `mapstructure:"seeder_query_retries"`
	// SeederQueryBackoff is the initial backoff between seeder retries; the
	// delay doubles each attempt. Default: 2s (so worst-case 2s + 4s = 6s
	// before falling back to the full-LIST / now+interval default).
	SeederQueryBackoff time.Duration `mapstructure:"seeder_query_backoff"`
	// Tiers assigns priority tiers to specific kinds. Kinds not listed default
	// to P2 (the legacy LIST+WATCH+optional-resync behavior).
	Tiers TierConfig `mapstructure:"tiers"`
}

// TierConfig groups the per-tier settings. Kinds not listed in any tier
// default to P2 — the legacy LIST+WATCH behavior driven by ResyncInterval.
type TierConfig struct {
	// P1 kinds run the legacy LIST+WATCH path AND a sequential resync loop
	// that re-LISTs each kind in order with a synchronous flush after each
	// kind. P1 LISTs are non-paginated.
	P1 P1TierConfig `mapstructure:"p1"`
	// P3 kinds do not WATCH — only periodic LIST. Each LIST emits EXISTING
	// events for every current object. Clients determine "live" objects by
	// filtering events to within the most recent relist_interval window;
	// the agent does not synthesize DELETED events.
	P3 P3TierConfig `mapstructure:"p3"`
	// P4 kinds are WATCH-only from the current point in time — no initial
	// LIST, no relist, no SEEDED sentinel, and no resume from a stored RV.
	// The agent probes the current collection RV and watches forward from
	// it, capturing only new changes. Ideal for ephemeral, high-volume,
	// stream-shaped kinds like Event where backfilling state is pointless.
	P4 P4TierConfig `mapstructure:"p4"`
	// P5 kinds are ignored entirely — no LIST, no WATCH, no relist. They
	// don't contribute to gvrKindMap, status counts, or resync loops.
	// Useful for high-volume, low-value kinds (Lease, EndpointSlice) that
	// the operator doesn't want in ClickHouse.
	P5 P5TierConfig `mapstructure:"p5"`
}

// P1TierConfig holds the global P1 resync interval and per-kind entries.
type P1TierConfig struct {
	// ResyncInterval is the default cadence at which P1 kinds are re-LISTed.
	// Each per-kind entry may override this with its own ResyncInterval.
	// Required if Kinds is non-empty.
	ResyncInterval time.Duration `mapstructure:"resync_interval"`
	// Kinds enumerates which kinds belong to the P1 tier.
	Kinds []KindTierEntry `mapstructure:"kinds"`
}

// P3TierConfig holds the global P3 relist interval and per-kind entries.
type P3TierConfig struct {
	// RelistInterval is the default cadence at which P3 kinds are LISTed.
	// Each per-kind entry may override this with its own RelistInterval.
	// Required if Kinds is non-empty.
	RelistInterval time.Duration `mapstructure:"relist_interval"`
	// Kinds enumerates which kinds belong to the P3 tier.
	Kinds []KindTierEntry `mapstructure:"kinds"`
}

// P4TierConfig lists kinds the agent should WATCH from the current point in
// time. No interval fields — P4 never relists; it just streams changes
// forward from the moment the watch opens.
type P4TierConfig struct {
	Kinds []KindTierEntry `mapstructure:"kinds"`
}

// P5TierConfig lists kinds the agent should ignore entirely. No interval
// fields — P5 has no periodic activity.
type P5TierConfig struct {
	Kinds []KindTierEntry `mapstructure:"kinds"`
}

// KindTierEntry assigns a single kind to a tier with optional interval override.
// APIVersion is optional — when empty, the entry matches any GVR with the given
// Kind. Set APIVersion to disambiguate when the same Kind exists in multiple
// API groups (e.g. CRDs that reuse a built-in kind name).
type KindTierEntry struct {
	Kind       string `mapstructure:"kind"`
	APIVersion string `mapstructure:"apiVersion"`
	// ResyncInterval overrides the tier's global interval for P1 kinds.
	// Ignored for P3 entries.
	ResyncInterval time.Duration `mapstructure:"resync_interval"`
	// RelistInterval overrides the tier's global interval for P3 kinds.
	// Ignored for P1 entries.
	RelistInterval time.Duration `mapstructure:"relist_interval"`
}

// ResourceFilter defines which resources to watch
type ResourceFilter struct {
	APIVersion string `mapstructure:"apiVersion"`
	Kind       string `mapstructure:"kind"`
}

// OutputConfig defines an output destination
type OutputConfig struct {
	Type   string                 `mapstructure:"type"`
	Config map[string]interface{} `mapstructure:"config"`
}

// BufferConfig defines local buffering settings
type BufferConfig struct {
	Size          int           `mapstructure:"size"`
	FlushInterval time.Duration `mapstructure:"flush_interval"`
	RetryInterval time.Duration `mapstructure:"retry_interval"`
	BatchSize     int           `mapstructure:"batch_size"`
	// FlushConcurrency is the number of worker goroutines that send batches
	// to outputs in parallel for the default (P2/P3) pipeline. Values <= 0
	// are treated as 1 (single in-flight batch, original behavior). The P1
	// pipeline always uses a single worker — the watcher's per-kind
	// sequential resync can't issue the next FlushP1 until the previous
	// returns, so additional P1 workers would sit idle.
	FlushConcurrency int `mapstructure:"flush_concurrency"`
}

// ConcurrencyControlConfig defines concurrency control settings
type ConcurrencyControlConfig struct {
	// Enable concurrency limits (semaphores)
	Enabled bool `mapstructure:"enabled"`
	// Maximum concurrent LIST operations across all clusters (global semaphore)
	MaxGlobalLists int `mapstructure:"max_global_lists"`
	// Maximum concurrent LIST operations per cluster
	MaxListsPerCluster int `mapstructure:"max_lists_per_cluster"`
	// Maximum concurrent cluster initializations
	MaxClusterInits int `mapstructure:"max_cluster_inits"`
}

// ShardConfig defines shard assignment for this agent instance.
// The agent uses rendezvous (HRW) hashing: for each cluster name, all shard IDs
// are hashed with the name, and the shard with the highest hash wins.
// The total shard count is computed at runtime from Vault cluster count divided
// by ClustersPerShard; each pod receives its ID via KSEER_SHARD_ID env var.
type ShardConfig struct {
	// ID is the zero-indexed shard number for this agent instance.
	// Set via KSEER_SHARD_ID environment variable (injected by Helm per Deployment).
	ID int `mapstructure:"id"`
	// Total is a manual override for the shard count.
	// When > 0, bypasses the Vault-computed total. Rarely needed.
	Total int `mapstructure:"total"`
	// ClustersPerShard is the target number of clusters per shard pod.
	// The agent computes neededTotal = ceil(vaultClusterCount / ClustersPerShard)
	// and auto-creates missing Deployments via the Kubernetes API.
	// Set to 0 to disable auto scale-out.
	ClustersPerShard int `mapstructure:"clusters_per_shard"`
	// MaxShards caps the StatefulSet replica count during auto scale-out.
	// 0 means unlimited (default). When the computed neededTotal exceeds MaxShards,
	// the agent clamps to MaxShards and logs a warning. Clusters beyond the cap
	// are distributed across the available shards via rendezvous hashing.
	MaxShards int `mapstructure:"max_shards"`
	// Overrides pins specific clusters to a shard by name, bypassing the hash.
	// Key is cluster name, value is the shard ID that should handle it.
	Overrides map[string]int `mapstructure:"overrides"`
	// PodsPerShard enables CH-weighted shard assignment when > 0.
	// Target number of unique pods per shard over the 7-day window ending at
	// the StatefulSet's kseer.io/shard-window-anchor annotation (YYYY-MM-DD).
	// All shard pods share the same anchor → same window → same deterministic assignment.
	// Falls back to clustersPerShard sorted-modulo when 0 or CH is unavailable.
	// Requires events_table to be configured in the ClickHouse output.
	PodsPerShard int64 `mapstructure:"pods_per_shard"`
}

// Load loads configuration from viper
func Load() (*Config, error) {
	var cfg Config

	// Set defaults
	viper.SetDefault("mode", ModeMultiCluster)
	viper.SetDefault("buffer.size", 1000)
	viper.SetDefault("buffer.flush_interval", "5s")
	viper.SetDefault("buffer.retry_interval", "30s")
	viper.SetDefault("buffer.batch_size", 1000)
	// Concurrency control defaults
	viper.SetDefault("concurrency_control.enabled", true)
	viper.SetDefault("concurrency_control.max_global_lists", 30)
	viper.SetDefault("concurrency_control.max_lists_per_cluster", 10)
	viper.SetDefault("concurrency_control.max_cluster_inits", 5)
	// Default watch configuration (applied to clusters that don't specify their own)
	viper.SetDefault("default_watch_config.initial_backoff", "1s")
	viper.SetDefault("default_watch_config.max_backoff", "5m")
	viper.SetDefault("default_watch_config.timeout_seconds", 432000)
	viper.SetDefault("default_watch_config.allow_bookmarks", true)
	viper.SetDefault("default_watch_config.list_page_size", 500)
	viper.SetDefault("default_watch_config.resync_interval", "0")
	viper.SetDefault("default_watch_config.seeder_query_retries", 2)
	viper.SetDefault("default_watch_config.seeder_query_backoff", "2s")
	// Status log defaults
	viper.SetDefault("status_log.enabled", true)
	viper.SetDefault("status_log.interval", "5m")
	viper.SetDefault("status_log.per_kind_detail", true)
	// Vault configuration defaults
	viper.SetDefault("vault.refresh_interval", "5m")
	viper.SetDefault("vault.kubernetes_auth.token_path", "/var/run/secrets/kubernetes.io/serviceaccount/token")
	viper.SetDefault("vault.kubernetes_auth.mount_path", "kubernetes")
	viper.SetDefault("vault.approle_auth.mount_path", "approle")

	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Validate mode
	if cfg.Mode != ModeInCluster && cfg.Mode != ModeMultiCluster {
		return nil, fmt.Errorf("invalid mode: %s (must be 'in-cluster' or 'multi-cluster')", cfg.Mode)
	}

	// Viper's BindEnv + SetDefault calls for vault.* keys in main.go and Load
	// cause mapstructure to materialize a *VaultConfig (with non-nil nested
	// KubernetesAuth/AppRoleAuth from their own defaults) even when no vault
	// config is actually present. Vault is unusable without an Address, so
	// treat an empty Address as "not configured" and nil it out. In-cluster
	// mode would otherwise trip the mode check below.
	if cfg.Vault != nil && cfg.Vault.Address == "" {
		cfg.Vault = nil
	}

	// Vault configuration validation
	if cfg.Vault != nil {
		if cfg.Mode != ModeMultiCluster {
			return nil, fmt.Errorf("vault integration only supported in multi-cluster mode")
		}
		if cfg.Vault.Address == "" {
			return nil, fmt.Errorf("vault.address is required")
		}
		if cfg.Vault.SecretPath == "" {
			return nil, fmt.Errorf("vault.secret_path is required")
		}
		// Validate auth method
		switch cfg.Vault.AuthMethod {
		case "token":
			if cfg.Vault.Token == "" && cfg.Vault.TokenFile == "" {
				return nil, fmt.Errorf("vault token or token_file required for token auth")
			}
		case "kubernetes":
			if cfg.Vault.KubernetesAuth == nil {
				return nil, fmt.Errorf("vault kubernetes_auth configuration required for kubernetes auth")
			}
			if cfg.Vault.KubernetesAuth.Role == "" {
				return nil, fmt.Errorf("vault kubernetes_auth.role is required")
			}
		case "approle":
			if cfg.Vault.AppRoleAuth == nil {
				return nil, fmt.Errorf("vault approle_auth configuration required for approle auth")
			}
			if cfg.Vault.AppRoleAuth.RoleID == "" && cfg.Vault.AppRoleAuth.RoleIDFile == "" {
				return nil, fmt.Errorf("vault approle_auth role_id or role_id_file is required")
			}
			if cfg.Vault.AppRoleAuth.SecretID == "" && cfg.Vault.AppRoleAuth.SecretIDFile == "" {
				return nil, fmt.Errorf("vault approle_auth secret_id or secret_id_file is required")
			}
		default:
			return nil, fmt.Errorf("vault auth_method must be one of: token, kubernetes, approle")
		}
		if cfg.Vault.RefreshInterval == 0 {
			cfg.Vault.RefreshInterval = 5 * time.Minute
		}
	}

	// In-cluster mode validation
	if cfg.Mode == ModeInCluster {
		// In-cluster mode should only have one cluster configured with empty kubeconfig
		if len(cfg.Clusters) > 1 {
			return nil, fmt.Errorf("in-cluster mode supports only one cluster configuration")
		}
		if len(cfg.Clusters) == 0 {
			// Create default in-cluster config
			cfg.Clusters = []ClusterConfig{
				{
					Name:       "local-cluster",
					Kubeconfig: "", // Empty means use in-cluster config
				},
			}
		}
		if cfg.Clusters[0].Kubeconfig != "" {
			return nil, fmt.Errorf("in-cluster mode should not specify kubeconfig path (uses service account token)")
		}
	}

	// Multi-cluster mode validation
	if cfg.Mode == ModeMultiCluster {
		// Allow empty clusters if using Vault (dynamic discovery)
		if len(cfg.Clusters) == 0 && cfg.Vault == nil {
			return nil, fmt.Errorf("multi-cluster mode requires at least one cluster configured or vault configuration")
		}
		// Validate each cluster has either kubeconfig or service account auth
		for _, cluster := range cfg.Clusters {
			hasKubeconfig := cluster.Kubeconfig != ""
			hasServiceAccount := cluster.ServiceAccount != nil && cluster.ServiceAccount.Server != ""

			if !hasKubeconfig && !hasServiceAccount {
				return nil, fmt.Errorf("cluster %q must specify either 'kubeconfig' or 'service_account'", cluster.Name)
			}
			if hasKubeconfig && hasServiceAccount {
				return nil, fmt.Errorf("cluster %q cannot specify both 'kubeconfig' and 'service_account'", cluster.Name)
			}

			// Validate service account configuration
			if hasServiceAccount {
				sa := cluster.ServiceAccount
				if sa.Token == "" && sa.TokenFile == "" {
					return nil, fmt.Errorf("cluster %q service account must specify either 'token' or 'token_file'", cluster.Name)
				}
				if sa.Token != "" && sa.TokenFile != "" {
					return nil, fmt.Errorf("cluster %q service account cannot specify both 'token' and 'token_file'", cluster.Name)
				}
				if !sa.Insecure && sa.CAData == "" && sa.CAFile == "" {
					return nil, fmt.Errorf("cluster %q service account must specify 'ca_data', 'ca_file', or set 'insecure: true'", cluster.Name)
				}
				if sa.CAData != "" && sa.CAFile != "" {
					return nil, fmt.Errorf("cluster %q service account cannot specify both 'ca_data' and 'ca_file'", cluster.Name)
				}
			}
		}
	}

	if len(cfg.Outputs) == 0 {
		return nil, fmt.Errorf("no outputs configured")
	}

	// Override output credentials from environment variables if set
	// This is done post-unmarshal to avoid Viper race conditions with array unmarshaling
	for i := range cfg.Outputs {
		if cfg.Outputs[i].Type == "clickhouse" {
			if username := os.Getenv("KSEER_CLICKHOUSE_USERNAME"); username != "" {
				if cfg.Outputs[i].Config == nil {
					cfg.Outputs[i].Config = make(map[string]interface{})
				}
				cfg.Outputs[i].Config["username"] = username
			}
			if password := os.Getenv("KSEER_CLICKHOUSE_PASSWORD"); password != "" {
				if cfg.Outputs[i].Config == nil {
					cfg.Outputs[i].Config = make(map[string]interface{})
				}
				cfg.Outputs[i].Config["password"] = password
			}
		}
	}

	// Set default cluster names and watch config if not provided
	for i, cluster := range cfg.Clusters {
		if cluster.Name == "" {
			if cfg.Mode == ModeInCluster {
				cfg.Clusters[i].Name = "local-cluster"
			} else {
				cfg.Clusters[i].Name = fmt.Sprintf("cluster-%d", i)
			}
		}

		// Set watch config defaults if not specified, falling back to DefaultWatchConfig
		dwc := cfg.DefaultWatchConfig
		if cfg.Clusters[i].WatchConfig.InitialBackoff == 0 {
			if dwc.InitialBackoff != 0 {
				cfg.Clusters[i].WatchConfig.InitialBackoff = dwc.InitialBackoff
			} else {
				cfg.Clusters[i].WatchConfig.InitialBackoff = 1 * time.Second
			}
		}
		if cfg.Clusters[i].WatchConfig.MaxBackoff == 0 {
			if dwc.MaxBackoff != 0 {
				cfg.Clusters[i].WatchConfig.MaxBackoff = dwc.MaxBackoff
			} else {
				cfg.Clusters[i].WatchConfig.MaxBackoff = 5 * time.Minute
			}
		}
		if cfg.Clusters[i].WatchConfig.TimeoutSeconds == 0 {
			if dwc.TimeoutSeconds != 0 {
				cfg.Clusters[i].WatchConfig.TimeoutSeconds = dwc.TimeoutSeconds
			} else {
				cfg.Clusters[i].WatchConfig.TimeoutSeconds = 432000
			}
		}
		// AllowBookmarks defaults to true (zero value is false, so we need explicit check)
		if !cluster.WatchConfig.AllowBookmarks {
			cfg.Clusters[i].WatchConfig.AllowBookmarks = true
		}
		if cfg.Clusters[i].WatchConfig.ListPageSize == 0 {
			if dwc.ListPageSize != 0 {
				cfg.Clusters[i].WatchConfig.ListPageSize = dwc.ListPageSize
			} else {
				cfg.Clusters[i].WatchConfig.ListPageSize = 500
			}
		}
		if cfg.Clusters[i].WatchConfig.ResyncInterval == 0 && dwc.ResyncInterval != 0 {
			cfg.Clusters[i].WatchConfig.ResyncInterval = dwc.ResyncInterval
		}
		if cfg.Clusters[i].WatchConfig.SeederQueryRetries == 0 {
			if dwc.SeederQueryRetries != 0 {
				cfg.Clusters[i].WatchConfig.SeederQueryRetries = dwc.SeederQueryRetries
			} else {
				cfg.Clusters[i].WatchConfig.SeederQueryRetries = 2
			}
		}
		if cfg.Clusters[i].WatchConfig.SeederQueryBackoff == 0 {
			if dwc.SeederQueryBackoff != 0 {
				cfg.Clusters[i].WatchConfig.SeederQueryBackoff = dwc.SeederQueryBackoff
			} else {
				cfg.Clusters[i].WatchConfig.SeederQueryBackoff = 2 * time.Second
			}
		}
		// Inherit tier config from default_watch_config when the cluster does
		// not specify its own tiers. Tier assignments are about kinds, which
		// are global, so defaulting from DefaultWatchConfig is the right move.
		if len(cfg.Clusters[i].WatchConfig.Tiers.P1.Kinds) == 0 && len(dwc.Tiers.P1.Kinds) > 0 {
			cfg.Clusters[i].WatchConfig.Tiers.P1 = dwc.Tiers.P1
		}
		if len(cfg.Clusters[i].WatchConfig.Tiers.P3.Kinds) == 0 && len(dwc.Tiers.P3.Kinds) > 0 {
			cfg.Clusters[i].WatchConfig.Tiers.P3 = dwc.Tiers.P3
		}
		if len(cfg.Clusters[i].WatchConfig.Tiers.P4.Kinds) == 0 && len(dwc.Tiers.P4.Kinds) > 0 {
			cfg.Clusters[i].WatchConfig.Tiers.P4 = dwc.Tiers.P4
		}
		if len(cfg.Clusters[i].WatchConfig.Tiers.P5.Kinds) == 0 && len(dwc.Tiers.P5.Kinds) > 0 {
			cfg.Clusters[i].WatchConfig.Tiers.P5 = dwc.Tiers.P5
		}
		// Copy the global concurrency_control.max_lists_per_cluster (set by
		// the helm chart's concurrencyControl.maxListsPerCluster) into this
		// cluster's WatchConfig. Vault-discovered clusters get this via
		// vaultCredsToClusterConfig in the watcher manager; static clusters
		// need it here. Without it, watcher.New() creates no per-cluster
		// listSemaphore and watchResource deadlocks on a nil-channel send.
		if cfg.Clusters[i].WatchConfig.MaxListsPerCluster == 0 {
			if cfg.ConcurrencyControl.MaxListsPerCluster > 0 {
				cfg.Clusters[i].WatchConfig.MaxListsPerCluster = cfg.ConcurrencyControl.MaxListsPerCluster
			} else {
				cfg.Clusters[i].WatchConfig.MaxListsPerCluster = 10
			}
		}

		if err := validateTierConfig(&cfg.Clusters[i].WatchConfig.Tiers, cfg.Clusters[i].Name); err != nil {
			return nil, err
		}
	}

	if err := validateTierConfig(&cfg.DefaultWatchConfig.Tiers, "default_watch_config"); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// validateTierConfig enforces invariants on a TierConfig:
//   - P1 ResyncInterval > 0 if any P1 kinds are configured.
//   - P3 RelistInterval > 0 if any P3 kinds are configured.
//   - No kind appears in more than one tier (matching by Kind+APIVersion;
//     APIVersion="" matches any apiVersion of that Kind). P4 (stream) and
//     P5 (ignore) are included in the overlap matrix.
//   - Per-kind entries have a non-empty Kind.
func validateTierConfig(t *TierConfig, scope string) error {
	if len(t.P1.Kinds) > 0 && t.P1.ResyncInterval <= 0 {
		return fmt.Errorf("%s: tiers.p1.resync_interval must be > 0 when tiers.p1.kinds is non-empty", scope)
	}
	if len(t.P3.Kinds) > 0 && t.P3.RelistInterval <= 0 {
		return fmt.Errorf("%s: tiers.p3.relist_interval must be > 0 when tiers.p3.kinds is non-empty", scope)
	}
	for i, e := range t.P1.Kinds {
		if e.Kind == "" {
			return fmt.Errorf("%s: tiers.p1.kinds[%d].kind is required", scope, i)
		}
	}
	for i, e := range t.P3.Kinds {
		if e.Kind == "" {
			return fmt.Errorf("%s: tiers.p3.kinds[%d].kind is required", scope, i)
		}
	}
	for i, e := range t.P4.Kinds {
		if e.Kind == "" {
			return fmt.Errorf("%s: tiers.p4.kinds[%d].kind is required", scope, i)
		}
	}
	for i, e := range t.P5.Kinds {
		if e.Kind == "" {
			return fmt.Errorf("%s: tiers.p5.kinds[%d].kind is required", scope, i)
		}
	}

	pairs := []struct {
		nameA, nameB string
		kindsA       []KindTierEntry
		kindsB       []KindTierEntry
	}{
		{"p1", "p3", t.P1.Kinds, t.P3.Kinds},
		{"p1", "p4", t.P1.Kinds, t.P4.Kinds},
		{"p1", "p5", t.P1.Kinds, t.P5.Kinds},
		{"p3", "p4", t.P3.Kinds, t.P4.Kinds},
		{"p3", "p5", t.P3.Kinds, t.P5.Kinds},
		{"p4", "p5", t.P4.Kinds, t.P5.Kinds},
	}
	for _, p := range pairs {
		for _, a := range p.kindsA {
			for _, b := range p.kindsB {
				if a.Kind != b.Kind {
					continue
				}
				// Same kind name. If either side leaves apiVersion blank,
				// the kind-only entry shadows everything for that kind so
				// we treat the overlap as a conflict.
				if a.APIVersion == "" || b.APIVersion == "" || a.APIVersion == b.APIVersion {
					return fmt.Errorf("%s: kind %q appears in both tiers.%s and tiers.%s",
						scope, a.Kind, p.nameA, p.nameB)
				}
			}
		}
	}
	return nil
}
