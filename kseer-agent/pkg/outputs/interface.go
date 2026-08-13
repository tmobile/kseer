package outputs

import (
	"context"
	"fmt"

	"github.com/kseer/kseer-agent/pkg/types"
	"go.uber.org/zap"
)

// Output is the interface for all output plugins
type Output interface {
	// Send sends a batch of events to the output
	Send(ctx context.Context, events []types.Event) error

	// Close closes the output connection
	Close() error

	// Name returns the name of the output
	Name() string
}

// Factory is a function that creates an Output
type Factory func(config map[string]interface{}, logger *zap.Logger) (Output, error)

// Registry of output factories
var registry = make(map[string]Factory)

// Register registers an output factory
func Register(name string, factory Factory) {
	registry[name] = factory
}

// Create creates an output based on type and config
func Create(outputType string, config map[string]interface{}, logger *zap.Logger) (Output, error) {
	factory, ok := registry[outputType]
	if !ok {
		return nil, fmt.Errorf("unknown output type: %s", outputType)
	}

	return factory(config, logger)
}
