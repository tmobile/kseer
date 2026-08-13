package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/kseer/kseer-agent/pkg/config"
	"github.com/kseer/kseer-agent/pkg/outputs"
	"github.com/kseer/kseer-agent/pkg/watcher"

	// Import output plugins to register them
	_ "github.com/kseer/kseer-agent/pkg/outputs/clickhouse"
	_ "github.com/kseer/kseer-agent/pkg/outputs/kafka"
	_ "github.com/kseer/kseer-agent/pkg/outputs/stdout"
)

var (
	cfgFile string
	logger  *zap.Logger
)

var rootCmd = &cobra.Command{
	Use:   "kseer-agent",
	Short: "Kubernetes object watcher agent",
	Long: `Kseer Agent watches Kubernetes objects and streams changes to configured outputs.
It supports multiple output backends including Kafka, HTTP endpoints, and stdout.`,
	Run: run,
}

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.kseer-agent.yaml)")
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		viper.AddConfigPath(home)
		viper.AddConfigPath(".")
		viper.SetConfigType("yaml")
		viper.SetConfigName(".kseer-agent")
	}

	// Set env configuration BEFORE AutomaticEnv to avoid race conditions
	viper.SetEnvPrefix("KSEER")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Explicitly bind environment variables so they work even when commented out in config
	// Must be done BEFORE AutomaticEnv() to prevent race conditions with config unmarshaling
	viper.BindEnv("vault.address")
	viper.BindEnv("vault.secret_path")
	viper.BindEnv("vault.namespace")
	viper.BindEnv("vault.auth_method")
	viper.BindEnv("vault.tls_skip_verify")
	// Shard ID injected per-Deployment by Helm via KSEER_SHARD_ID env var
	viper.BindEnv("shard.id", "KSEER_SHARD_ID")

	// Note: Not binding nested output fields to avoid race conditions with array unmarshaling
	// ClickHouse credentials should be set in config file or via secrets
	
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err == nil {
		fmt.Fprintln(os.Stderr, "Using config file:", viper.ConfigFileUsed())
	}

	// Initialize logger
	config := zap.NewProductionConfig()
	config.EncoderConfig.TimeKey = "timestamp"
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	// Disable sampling: the production default drops repeated messages (keeps
	// first 100/sec, then every 100th), which silently truncates high-volume
	// per-event logs like verbose_event_logging.
	config.Sampling = nil

	var err error
	logger, err = config.Build()
	if err != nil {
		panic(fmt.Sprintf("Failed to initialize logger: %v", err))
	}
}

func run(cmd *cobra.Command, args []string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		logger.Fatal("Failed to load configuration", zap.Error(err))
	}

	logger.Info("Starting kseer-agent",
		zap.String("mode", string(cfg.Mode)),
		zap.Int("clusters", len(cfg.Clusters)))

	// Initialize outputs
	outputManager, err := outputs.NewManager(cfg.Outputs, cfg.Buffer, logger)
	if err != nil {
		logger.Fatal("Failed to initialize output manager", zap.Error(err))
	}
	defer outputManager.Close()

	// Create watcher manager (supports both static and dynamic clusters)
	watcherManager, err := watcher.NewManager(cfg, outputManager, logger)
	if err != nil {
		logger.Fatal("Failed to create watcher manager", zap.Error(err))
	}

	// Start all watchers (static from config + dynamic from Vault)
	if err := watcherManager.Start(ctx); err != nil {
		logger.Fatal("Failed to start watchers", zap.Error(err))
	}

	logger.Info("All initial watchers started",
		zap.Strings("active_clusters", watcherManager.GetActiveWatchers()))

	if cfg.Vault != nil {
		logger.Info("Vault integration enabled - watching for new clusters",
			zap.Duration("refresh_interval", cfg.Vault.RefreshInterval))
	}

	// Wait for interrupt signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	logger.Info("Shutting down...")
	cancel()
}

func main() {
	defer func() {
		if logger != nil {
			logger.Sync()
		}
	}()
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
