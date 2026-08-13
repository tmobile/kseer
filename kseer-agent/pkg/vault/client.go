package vault

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/vault/api"
	"go.uber.org/zap"

	"github.com/kseer/kseer-agent/pkg/config"
)

// ClusterCredentials represents cluster credentials stored in Vault
type ClusterCredentials struct {
	Name     string `json:"name"`
	Server   string `json:"server"`
	Token    string `json:"token"`
	CAData   string `json:"ca_data"`
	Insecure bool   `json:"insecure"`
}

// UnmarshalJSON handles unmarshaling of ClusterCredentials with flexible insecure field
func (c *ClusterCredentials) UnmarshalJSON(data []byte) error {
	// Use an alias to avoid recursion
	type Alias ClusterCredentials
	aux := &struct {
		Insecure interface{} `json:"insecure"`
		*Alias
	}{
		Alias: (*Alias)(c),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Handle insecure field - can be bool or string
	switch v := aux.Insecure.(type) {
	case bool:
		c.Insecure = v
	case string:
		if v == "" {
			c.Insecure = false
		} else {
			parsed, err := strconv.ParseBool(v)
			if err != nil {
				// If parse fails, treat non-empty string as true
				c.Insecure = true
			} else {
				c.Insecure = parsed
			}
		}
	case nil:
		c.Insecure = false
	default:
		c.Insecure = false
	}

	return nil
}

// Client wraps Vault API client with helper methods
type Client struct {
	client     *api.Client
	config     *config.VaultConfig
	logger     *zap.Logger
	secretPath string
}

// NewClient creates a new Vault client
func NewClient(cfg *config.VaultConfig, logger *zap.Logger) (*Client, error) {
	vaultConfig := api.DefaultConfig()
	vaultConfig.Address = cfg.Address

	if cfg.TLSSkipVerify {
		tlsConfig := &api.TLSConfig{
			Insecure: true,
		}
		if err := vaultConfig.ConfigureTLS(tlsConfig); err != nil {
			return nil, fmt.Errorf("failed to configure TLS: %w", err)
		}
	} else if cfg.CACert != "" {
		tlsConfig := &api.TLSConfig{
			CACert: cfg.CACert,
		}
		if err := vaultConfig.ConfigureTLS(tlsConfig); err != nil {
			return nil, fmt.Errorf("failed to configure TLS: %w", err)
		}
	}

	client, err := api.NewClient(vaultConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create vault client: %w", err)
	}

	// Set namespace if using Vault Enterprise
	if cfg.Namespace != "" {
		client.SetNamespace(cfg.Namespace)
	}

	vaultClient := &Client{
		client:     client,
		config:     cfg,
		logger:     logger,
		secretPath: cfg.SecretPath,
	}

	// Authenticate
	if err := vaultClient.authenticate(); err != nil {
		return nil, fmt.Errorf("vault authentication failed: %w", err)
	}

	return vaultClient, nil
}

// authenticate handles Vault authentication based on configured method
func (c *Client) authenticate() error {
	switch c.config.AuthMethod {
	case "token":
		return c.authenticateWithToken()
	case "kubernetes":
		return c.authenticateWithKubernetes()
	case "approle":
		return c.authenticateWithAppRole()
	default:
		return fmt.Errorf("unsupported auth method: %s", c.config.AuthMethod)
	}
}

// authenticateWithToken authenticates using a static token
func (c *Client) authenticateWithToken() error {
	var token string
	if c.config.Token != "" {
		token = c.config.Token
	} else if c.config.TokenFile != "" {
		tokenBytes, err := os.ReadFile(c.config.TokenFile)
		if err != nil {
			return fmt.Errorf("failed to read token file: %w", err)
		}
		token = strings.TrimSpace(string(tokenBytes))
	} else {
		return fmt.Errorf("no token or token_file configured")
	}

	c.client.SetToken(token)
	c.logger.Info("Authenticated to Vault using token auth")
	return nil
}

// authenticateWithKubernetes authenticates using Kubernetes auth method
func (c *Client) authenticateWithKubernetes() error {
	if c.config.KubernetesAuth == nil {
		return fmt.Errorf("kubernetes auth not configured")
	}

	// Read JWT token
	tokenPath := c.config.KubernetesAuth.TokenPath
	if tokenPath == "" {
		tokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	}

	jwt, err := os.ReadFile(tokenPath)
	if err != nil {
		return fmt.Errorf("failed to read kubernetes service account token: %w", err)
	}

	// Login to Vault
	mountPath := c.config.KubernetesAuth.MountPath
	if mountPath == "" {
		mountPath = "kubernetes"
	}

	loginPath := fmt.Sprintf("auth/%s/login", mountPath)
	loginData := map[string]interface{}{
		"role": c.config.KubernetesAuth.Role,
		"jwt":  string(jwt),
	}

	secret, err := c.client.Logical().Write(loginPath, loginData)
	if err != nil {
		return fmt.Errorf("kubernetes auth login failed: %w", err)
	}

	if secret == nil || secret.Auth == nil {
		return fmt.Errorf("no auth info returned from kubernetes auth")
	}

	c.client.SetToken(secret.Auth.ClientToken)
	c.logger.Info("Authenticated to Vault using Kubernetes auth",
		zap.String("role", c.config.KubernetesAuth.Role))

	// Start token renewal
	go c.renewToken(secret)

	return nil
}

// authenticateWithAppRole authenticates using AppRole auth method
func (c *Client) authenticateWithAppRole() error {
	if c.config.AppRoleAuth == nil {
		return fmt.Errorf("approle auth not configured")
	}

	var roleID string
	if c.config.AppRoleAuth.RoleID != "" {
		roleID = c.config.AppRoleAuth.RoleID
	} else if c.config.AppRoleAuth.RoleIDFile != "" {
		roleIDBytes, err := os.ReadFile(c.config.AppRoleAuth.RoleIDFile)
		if err != nil {
			return fmt.Errorf("failed to read role_id file: %w", err)
		}
		roleID = strings.TrimSpace(string(roleIDBytes))
	} else {
		return fmt.Errorf("no role_id or role_id_file configured")
	}

	var secretID string
	if c.config.AppRoleAuth.SecretID != "" {
		secretID = c.config.AppRoleAuth.SecretID
	} else if c.config.AppRoleAuth.SecretIDFile != "" {
		secretIDBytes, err := os.ReadFile(c.config.AppRoleAuth.SecretIDFile)
		if err != nil {
			return fmt.Errorf("failed to read secret_id file: %w", err)
		}
		secretID = strings.TrimSpace(string(secretIDBytes))
	} else {
		return fmt.Errorf("no secret_id or secret_id_file configured")
	}

	mountPath := c.config.AppRoleAuth.MountPath
	if mountPath == "" {
		mountPath = "approle"
	}

	loginPath := fmt.Sprintf("auth/%s/login", mountPath)
	loginData := map[string]interface{}{
		"role_id":   roleID,
		"secret_id": secretID,
	}

	secret, err := c.client.Logical().Write(loginPath, loginData)
	if err != nil {
		return fmt.Errorf("approle auth login failed: %w", err)
	}

	if secret == nil || secret.Auth == nil {
		return fmt.Errorf("no auth info returned from approle auth")
	}

	c.client.SetToken(secret.Auth.ClientToken)
	c.logger.Info("Authenticated to Vault using AppRole auth",
		zap.String("role_id", roleID))

	// Start token renewal
	go c.renewToken(secret)

	return nil
}

// renewToken handles automatic token renewal
func (c *Client) renewToken(secret *api.Secret) {
	renewer, err := c.client.NewLifetimeWatcher(&api.LifetimeWatcherInput{
		Secret: secret,
	})
	if err != nil {
		c.logger.Error("Failed to create token renewer", zap.Error(err))
		return
	}

	go renewer.Start()
	defer renewer.Stop()

	for {
		select {
		case err := <-renewer.DoneCh():
			if err != nil {
				c.logger.Error("Token renewal stopped", zap.Error(err))
			}
			// Re-authenticate with backoff. Use an inner loop so we never re-select on
			// the dead renewer channels (DoneCh fires exactly once; after that the
			// renewer goroutine has exited and neither channel will ever receive again).
			for {
				if authErr := c.authenticate(); authErr != nil {
					c.logger.Error("Failed to re-authenticate to Vault", zap.Error(authErr))
					time.Sleep(30 * time.Second)
					continue // inner loop only — retries authenticate()
				}
				return // success: authenticate() already started a new renewToken goroutine
			}
		case renewal := <-renewer.RenewCh():
			c.logger.Debug("Token renewed", zap.Time("renewal_time", renewal.RenewedAt))
		}
	}
}

// ListClusters retrieves all cluster credentials from Vault
func (c *Client) ListClusters(ctx context.Context) (map[string]*ClusterCredentials, error) {
	// For KV v2: mount is e.g. "kv", path is "kseer/clusters"
	// Need to construct: kv/metadata/kseer/clusters
	parts := strings.SplitN(c.secretPath, "/", 2)
	listPath := fmt.Sprintf("%s/metadata/%s", parts[0], parts[1])

	secret, err := c.client.Logical().List(listPath)
	if err != nil {
		return nil, fmt.Errorf("failed to list secrets: %w", err)
	}

	if secret == nil || secret.Data == nil {
		c.logger.Debug("No secrets found in Vault", zap.String("path", listPath))
		return map[string]*ClusterCredentials{}, nil
	}

	keys, ok := secret.Data["keys"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected response format from Vault list")
	}

	clusters := make(map[string]*ClusterCredentials)
	for _, keyInterface := range keys {
		key, ok := keyInterface.(string)
		if !ok {
			continue
		}

		// Read each cluster's credentials
		creds, err := c.GetClusterCredentials(ctx, key)
		if err != nil {
			c.logger.Warn("Failed to read cluster credentials",
				zap.String("key", key),
				zap.Error(err))
			continue
		}

		clusters[key] = creds
	}

	return clusters, nil
}

// GetClusterCredentials retrieves credentials for a specific cluster
func (c *Client) GetClusterCredentials(ctx context.Context, clusterName string) (*ClusterCredentials, error) {
	// For KV v2: mount is e.g. "kv", path is "kseer/clusters/{name}"
	// Need to construct: kv/data/kseer/clusters/{name}
	parts := strings.SplitN(c.secretPath, "/", 2)
	secretPath := fmt.Sprintf("%s/data/%s/%s", parts[0], parts[1], clusterName)

	secret, err := c.client.Logical().Read(secretPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read secret: %w", err)
	}

	if secret == nil || secret.Data == nil {
		return nil, fmt.Errorf("secret not found: %s", secretPath)
	}

	// Handle KV v2 format (data is nested under "data" key)
	data := secret.Data
	if dataField, ok := secret.Data["data"]; ok {
		if dataMap, ok := dataField.(map[string]interface{}); ok {
			data = dataMap
		}
	}

	// Marshal and unmarshal to convert to ClusterCredentials
	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal secret data: %w", err)
	}

	var creds ClusterCredentials
	if err := json.Unmarshal(jsonData, &creds); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cluster credentials: %w", err)
	}

	// Set name if not provided in secret
	if creds.Name == "" {
		creds.Name = clusterName
	}

	return &creds, nil
}

// WatchClusters watches for new clusters in Vault and returns them via channel
func (c *Client) WatchClusters(ctx context.Context, interval time.Duration) (<-chan map[string]*ClusterCredentials, error) {
	clusterChan := make(chan map[string]*ClusterCredentials)

	go func() {
		defer close(clusterChan)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		// Send initial cluster list
		clusters, err := c.ListClusters(ctx)
		if err != nil {
			c.logger.Error("Failed to list clusters", zap.Error(err))
		} else {
			select {
			case clusterChan <- clusters:
			case <-ctx.Done():
				return
			}
		}

		// Watch for changes
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				clusters, err := c.ListClusters(ctx)
				if err != nil {
					c.logger.Error("Failed to list clusters", zap.Error(err))
					continue
				}

				select {
				case clusterChan <- clusters:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return clusterChan, nil
}
