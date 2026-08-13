package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/IBM/sarama"
	"github.com/kseer/kseer-agent/pkg/outputs"
	"github.com/kseer/kseer-agent/pkg/types"
	"go.uber.org/zap"
)

func init() {
	outputs.Register("kafka", NewKafkaOutput)
}

// KafkaOutput sends events to Kafka
type KafkaOutput struct {
	producer sarama.AsyncProducer
	topic    string
	logger   *zap.Logger
}

// Config represents Kafka output configuration
type Config struct {
	Brokers     []string `mapstructure:"brokers"`
	Topic       string   `mapstructure:"topic"`
	Compression string   `mapstructure:"compression"`
	BatchSize   int      `mapstructure:"batch_size"`
}

// NewKafkaOutput creates a new Kafka output
func NewKafkaOutput(cfg map[string]interface{}, logger *zap.Logger) (outputs.Output, error) {
	// Parse config
	config := &Config{
		Topic:       "kubernetes-events",
		Compression: "snappy",
		BatchSize:   100,
	}

	// Extract brokers
	if brokers, ok := cfg["brokers"].([]interface{}); ok {
		config.Brokers = make([]string, len(brokers))
		for i, b := range brokers {
			config.Brokers[i] = fmt.Sprintf("%v", b)
		}
		logger.Info("Kafka brokers configured", zap.Strings("brokers", config.Brokers))
	} else {
		return nil, fmt.Errorf("brokers not configured")
	}

	if topic, ok := cfg["topic"].(string); ok {
		config.Topic = topic
	}

	if compression, ok := cfg["compression"].(string); ok {
		config.Compression = compression
	}

	// Create Kafka producer
	producerConfig := sarama.NewConfig()
	producerConfig.Producer.Return.Successes = true
	producerConfig.Producer.Return.Errors = true
	producerConfig.Producer.RequiredAcks = sarama.WaitForLocal
	producerConfig.Producer.Flush.Messages = config.BatchSize

	// Increase timeouts and retries for remote Kafka
	producerConfig.Net.DialTimeout = 30 * time.Second
	producerConfig.Net.ReadTimeout = 30 * time.Second
	producerConfig.Net.WriteTimeout = 30 * time.Second
	producerConfig.Metadata.Retry.Max = 3
	producerConfig.Metadata.Retry.Backoff = 250 * time.Millisecond
	producerConfig.Producer.Retry.Max = 3
	producerConfig.Version = sarama.V2_0_0_0 // Set Kafka version

	// Don't try to refresh metadata from localhost
	producerConfig.Metadata.Full = false

	// Set compression
	switch config.Compression {
	case "gzip":
		producerConfig.Producer.Compression = sarama.CompressionGZIP
	case "snappy":
		producerConfig.Producer.Compression = sarama.CompressionSnappy
	case "lz4":
		producerConfig.Producer.Compression = sarama.CompressionLZ4
	case "zstd":
		producerConfig.Producer.Compression = sarama.CompressionZSTD
	}

	logger.Info("Creating Kafka producer", zap.Strings("brokers", config.Brokers), zap.String("topic", config.Topic))

	producer, err := sarama.NewAsyncProducer(config.Brokers, producerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kafka producer: %w", err)
	}

	logger.Info("Kafka producer created successfully")

	output := &KafkaOutput{
		producer: producer,
		topic:    config.Topic,
		logger:   logger,
	}

	// Start error and success handlers
	go output.handleErrors()
	go output.handleSuccesses()

	return output, nil
}

// Send sends events to Kafka
func (k *KafkaOutput) Send(ctx context.Context, events []types.Event) error {
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			// Extract kind and name from object for logging
			kind, name, _ := extractMetadata(event)
			k.logger.Error("Failed to marshal event",
				zap.String("cluster", event.ClusterName),
				zap.String("kind", kind),
				zap.String("name", name),
				zap.Error(err))
			continue
		}

		// Extract metadata for partition key
		kind, name, namespace := extractMetadata(event)

		// Create message with partition key
		key := fmt.Sprintf("%s/%s/%s/%s",
			event.ClusterName,
			namespace,
			kind,
			name)

		msg := &sarama.ProducerMessage{
			Topic: k.topic,
			Key:   sarama.StringEncoder(key),
			Value: sarama.ByteEncoder(data),
		}

		select {
		case k.producer.Input() <- msg:
			// Message queued
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

// extractMetadata extracts kind, name, and namespace from the event object
func extractMetadata(event types.Event) (kind, name, namespace string) {
	if event.Object != nil {
		if k, ok := event.Object["kind"].(string); ok {
			kind = k
		}
		if metadata, ok := event.Object["metadata"].(map[string]interface{}); ok {
			if n, ok := metadata["name"].(string); ok {
				name = n
			}
			if ns, ok := metadata["namespace"].(string); ok {
				namespace = ns
			}
		}
	}
	return
}

// handleErrors handles producer errors
func (k *KafkaOutput) handleErrors() {
	for err := range k.producer.Errors() {
		k.logger.Error("Kafka producer error",
			zap.String("topic", err.Msg.Topic),
			zap.Error(err.Err))
	}
}

// handleSuccesses handles successful sends (for monitoring)
func (k *KafkaOutput) handleSuccesses() {
	count := 0
	for range k.producer.Successes() {
		count++
		// Log every 100 successful sends
		if count%100 == 0 {
			k.logger.Info("Kafka messages sent successfully",
				zap.Int("count", count))
		}
	}
}

// Close closes the Kafka producer
func (k *KafkaOutput) Close() error {
	return k.producer.Close()
}

// Name returns the output name
func (k *KafkaOutput) Name() string {
	return "kafka"
}
