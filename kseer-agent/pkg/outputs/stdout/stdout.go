package stdout

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/kseer/kseer-agent/pkg/outputs"
	"github.com/kseer/kseer-agent/pkg/types"
	"go.uber.org/zap"
)

func init() {
	outputs.Register("stdout", NewStdoutOutput)
}

// StdoutOutput writes events to stdout
type StdoutOutput struct {
	pretty bool
	logger *zap.Logger
}

// NewStdoutOutput creates a new stdout output
func NewStdoutOutput(cfg map[string]interface{}, logger *zap.Logger) (outputs.Output, error) {
	pretty := false
	if p, ok := cfg["pretty"].(bool); ok {
		pretty = p
	}

	return &StdoutOutput{
		pretty: pretty,
		logger: logger,
	}, nil
}

// Send writes events to stdout
func (s *StdoutOutput) Send(ctx context.Context, events []types.Event) error {
	var encoder *json.Encoder
	if s.pretty {
		encoder = json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
	} else {
		encoder = json.NewEncoder(os.Stdout)
	}

	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			return fmt.Errorf("failed to encode event: %w", err)
		}
	}

	return nil
}

// Close is a no-op for stdout
func (s *StdoutOutput) Close() error {
	return nil
}

// Name returns the output name
func (s *StdoutOutput) Name() string {
	return "stdout"
}
