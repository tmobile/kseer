package types

import (
	"time"
)

// Event represents a Kubernetes object change event
type Event struct {
	Timestamp   time.Time              `json:"timestamp"`
	ClusterName string                 `json:"cluster_name"`
	Operation   string                 `json:"operation"` // ADDED, MODIFIED, DELETED
	Object      map[string]interface{} `json:"object"`    // Full Kubernetes object
}

// EventBatch represents a batch of events
type EventBatch struct {
	Events []Event `json:"events"`
}
