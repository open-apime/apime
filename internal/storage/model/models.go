package model

import "time"

type InstanceStatus string

const (
	InstanceStatusPending      InstanceStatus = "pending"
	InstanceStatusActive       InstanceStatus = "active"
	InstanceStatusError        InstanceStatus = "error"
	InstanceStatusDisconnected InstanceStatus = "disconnected"
)

type Instance struct {
	ID                   string            `json:"id"`
	Name                 string            `json:"name"`
	OwnerUserID          string            `json:"ownerUserId"`
	OwnerEmail           string            `json:"ownerEmail,omitempty"`
	WhatsAppJID          string            `json:"whatsappJid,omitempty"`
	WebhookURL           string            `json:"webhookUrl,omitempty"`
	WebhookSecret        string            `json:"-"`
	TokenHash            string            `json:"-"`
	TokenUpdatedAt       *time.Time        `json:"tokenUpdatedAt,omitempty"`
	Status               InstanceStatus    `json:"status"`
	SessionBlob          []byte            `json:"-"`
	HistorySyncStatus    HistorySyncStatus `json:"historySyncStatus"`
	HistorySyncCycleID   string            `json:"historySyncCycleId"`
	HistorySyncUpdatedAt *time.Time        `json:"historySyncUpdatedAt,omitempty"`
	CreatedAt            time.Time         `json:"createdAt"`
	UpdatedAt            time.Time         `json:"updatedAt"`
}

type Message struct {
	ID         string    `json:"id"`
	InstanceID string    `json:"instanceId"`
	To         string    `json:"to"`
	Type       string    `json:"type"`
	Payload    string    `json:"payload"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"createdAt"`
}

type EventLog struct {
	ID          string     `json:"id"`
	InstanceID  string     `json:"instanceId"`
	Type        string     `json:"type"`
	Payload     string     `json:"payload"`
	DeliveredAt *time.Time `json:"deliveredAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}

type Webhook struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Secret    string    `json:"secret"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"createdAt"`
}

type APIToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	TokenHash  string     `json:"-"`
	UserID     string     `json:"userId"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	IsActive   bool       `json:"isActive"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

type DeviceConfig struct {
	ID           string    `json:"id"`
	PlatformType string    `json:"platformType"`
	OSName       string    `json:"osName"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type HistorySyncStatus string

const (
	HistorySyncStatusPending   HistorySyncStatus = "pending"
	HistorySyncStatusRunning   HistorySyncStatus = "running"
	HistorySyncStatusCompleted HistorySyncStatus = "completed"
	HistorySyncStatusFailed    HistorySyncStatus = "failed"
)

type HistorySyncPayloadStatus string

const (
	HistorySyncPayloadPending    HistorySyncPayloadStatus = "pending"
	HistorySyncPayloadProcessing HistorySyncPayloadStatus = "processing"
	HistorySyncPayloadDone       HistorySyncPayloadStatus = "done"
	HistorySyncPayloadError      HistorySyncPayloadStatus = "error"
)

type WhatsappHistorySync struct {
	ID          string                   `json:"id"`
	InstanceID  string                   `json:"instanceId"`
	PayloadType string                   `json:"payloadType"`
	Payload     []byte                   `json:"payload"`
	CycleID     string                   `json:"cycleId"`
	Status      HistorySyncPayloadStatus `json:"status"`
	CreatedAt   time.Time                `json:"createdAt"`
	ProcessedAt *time.Time               `json:"processedAt"`
}
