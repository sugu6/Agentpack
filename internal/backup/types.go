package backup

import "time"

type Backup struct {
	ID          string    `json:"id"`
	CreatedAt   time.Time `json:"createdAt"`
	Description string    `json:"description"`
	Action      string    `json:"action"`
	AgentID     string    `json:"agentId"`
	AgentPath   string    `json:"agentPath"`
	Data        string    `json:"data"`
	Size        int       `json:"size"`
}

type Snapshot struct {
	Version       int            `json:"version"`
	SchemaVersion int            `json:"schemaVersion"`
	CreatedAt     string         `json:"createdAt"`
	Action        string         `json:"action,omitempty"`
	AgentID       string         `json:"agentId,omitempty"`
	AgentPath     string         `json:"agentPath,omitempty"`
	Description   string         `json:"description,omitempty"`
	Settings      map[string]any `json:"settings,omitempty"`
	MCPServers    []SnapshotMCP  `json:"mcpServers,omitempty"`
}

type ExportPayload struct {
	Manifest Manifest `json:"manifest"`
	Snapshot Snapshot `json:"snapshot"`
}

type SnapshotMCP struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"`
	Transport   string            `json:"transport"`
	URL         string            `json:"url"`
	Source      string            `json:"source"`
	SourceID    string            `json:"sourceId"`
	BoundAgents []string          `json:"boundAgents,omitempty"`
}

type Manifest struct {
	Version       int    `json:"version"`
	SchemaVersion int    `json:"schemaVersion"`
	AppName       string `json:"appName"`
	ExportedAt    string `json:"exportedAt"`
	MCPCount      int    `json:"mcpCount"`
}

const (
	CurrentVersion       = 1
	CurrentSchemaVersion = 1
	AppName              = "AgentPack"
)
