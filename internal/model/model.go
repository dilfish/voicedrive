package model

import "time"

type Device struct {
	ID            string    `json:"id"`
	MAC           string    `json:"mac"`
	Name          string    `json:"name"`
	AgentID       string    `json:"agent_id"`
	ActiveGroupID *string   `json:"active_group_id,omitempty"`
	LastAckedSeq  int64     `json:"last_acked_seq"`
	LastSeenAt    time.Time `json:"last_seen_at"`
}

type Group struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	OwnerID    string    `json:"owner_device_id"`
	InviteCode string    `json:"invite_code"`
	NextSeq    int64     `json:"next_seq"`
	CreatedAt  time.Time `json:"created_at"`
}

type Membership struct {
	GroupID  string    `json:"group_id"`
	DeviceID string    `json:"device_id"`
	Alias    string    `json:"alias"`
	JoinedAt time.Time `json:"joined_at"`
}

type Message struct {
	ID           string    `json:"id"`
	GroupID      string    `json:"group_id"`
	FromDeviceID string    `json:"from_device_id"`
	Text         string    `json:"text"`
	AudioURL     string    `json:"audio_url"`
	AudioStatus  string    `json:"audio_status"`
	DurationMS   int       `json:"duration_ms"`
	Seq          int64     `json:"seq"`
	CreatedAt    time.Time `json:"created_at"`
}

// AudioStatus values describe the message audio lifecycle.
const (
	AudioPending = "pending"
	AudioReady   = "ready"
	AudioFailed  = "failed"
)

// DeviceMessage is a message together with the group it was delivered from.
type DeviceMessage struct {
	Message
	GroupName string `json:"group_name"`
}
