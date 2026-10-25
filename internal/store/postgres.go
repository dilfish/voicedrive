package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/Study-of-slag-weng/voicedrive1/internal/groupresolve"
	"github.com/Study-of-slag-weng/voicedrive1/internal/model"
)

//go:embed schema.sql
var schemaSQL []byte

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("request already processed")

type Postgres struct{ db *sql.DB }

func Open(ctx context.Context, dsn string) (*Postgres, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("database URL is empty")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	db := sql.OpenDB(stdlib.GetConnector(*config))
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Postgres{db: db}, nil
}

func (p *Postgres) Close() error { return p.db.Close() }

func (p *Postgres) Migrate(ctx context.Context) error {
	if _, err := p.db.ExecContext(ctx, string(schemaSQL)); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if _, err := p.db.ExecContext(ctx, `ALTER TABLE messages ADD COLUMN IF NOT EXISTS audio_status TEXT NOT NULL DEFAULT 'pending'`); err != nil {
		return fmt.Errorf("add message audio status: %w", err)
	}
	if _, err := p.db.ExecContext(ctx, `ALTER TABLE memberships ADD COLUMN IF NOT EXISTS muted BOOLEAN NOT NULL DEFAULT FALSE`); err != nil {
		return fmt.Errorf("add membership mute state: %w", err)
	}
	if _, err := p.db.ExecContext(ctx, `INSERT INTO device_group_acks(device_id,group_id)
SELECT device_id,group_id FROM memberships ON CONFLICT DO NOTHING`); err != nil {
		return fmt.Errorf("backfill device group acknowledgements: %w", err)
	}
	return nil
}

func (p *Postgres) JoinGroup(ctx context.Context, mac, inviteCode string) (model.Group, error) {
	var group model.Group
	err := p.db.QueryRowContext(ctx, `SELECT id::text,name,owner_device_id::text,invite_code,next_seq,created_at FROM groups WHERE invite_code=$1`, strings.TrimSpace(inviteCode)).
		Scan(&group.ID, &group.Name, &group.OwnerID, &group.InviteCode, &group.NextSeq, &group.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Group{}, ErrNotFound
	}
	if err != nil {
		return model.Group{}, fmt.Errorf("find group by invite code: %w", err)
	}
	if _, err := p.AddMember(ctx, group.ID, mac, ""); err != nil {
		return model.Group{}, err
	}
	return group, nil
}

func (p *Postgres) ListGroups(ctx context.Context, mac string) ([]model.Group, string, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT g.id::text,g.name,g.owner_device_id::text,g.invite_code,g.next_seq,g.created_at
FROM groups g JOIN memberships m ON m.group_id=g.id JOIN devices d ON d.id=m.device_id WHERE d.mac=$1 ORDER BY g.created_at`, mac)
	if err != nil {
		return nil, "", fmt.Errorf("list groups: %w", err)
	}
	defer rows.Close()
	groups := []model.Group{}
	for rows.Next() {
		var g model.Group
		if err := rows.Scan(&g.ID, &g.Name, &g.OwnerID, &g.InviteCode, &g.NextSeq, &g.CreatedAt); err != nil {
			return nil, "", err
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	var active string
	_ = p.db.QueryRowContext(ctx, `SELECT COALESCE(active_group_id::text,'') FROM devices WHERE mac=$1`, mac).Scan(&active)
	return groups, active, nil
}

func (p *Postgres) SetGroupMuted(ctx context.Context, mac, groupID string, muted bool) error {
	result, err := p.db.ExecContext(ctx, `UPDATE memberships m SET muted=$3 FROM devices d WHERE d.id=m.device_id AND d.mac=$1 AND m.group_id=$2`, mac, groupID, muted)
	if err != nil {
		return fmt.Errorf("set group mute: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ResolveGroupCandidates returns only groups joined by the device, with their
// explicit aliases and member counts for deterministic name resolution.
func (p *Postgres) ResolveGroupCandidates(ctx context.Context, mac string) ([]groupresolve.Candidate, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT g.id::text,g.name,g.owner_device_id::text,g.invite_code,g.next_seq,g.created_at,
       COUNT(DISTINCT members.device_id), COALESCE(array_agg(DISTINCT ga.alias) FILTER (WHERE ga.alias IS NOT NULL), ARRAY[]::text[])
FROM groups g JOIN memberships mine ON mine.group_id=g.id JOIN devices d ON d.id=mine.device_id
LEFT JOIN memberships members ON members.group_id=g.id LEFT JOIN group_aliases ga ON ga.group_id=g.id
WHERE d.mac=$1 GROUP BY g.id ORDER BY g.created_at,g.id`, mac)
	if err != nil {
		return nil, fmt.Errorf("query group candidates: %w", err)
	}
	defer rows.Close()
	var candidates []groupresolve.Candidate
	for rows.Next() {
		var candidate groupresolve.Candidate
		if err := rows.Scan(&candidate.Group.ID, &candidate.Group.Name, &candidate.Group.OwnerID, &candidate.Group.InviteCode, &candidate.Group.NextSeq, &candidate.Group.CreatedAt, &candidate.Members, &candidate.Aliases); err != nil {
			return nil, fmt.Errorf("scan group candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate group candidates: %w", err)
	}
	return candidates, nil
}

// AddGroupAlias adds a normalized spoken alias. It is idempotent per group.
func (p *Postgres) AddGroupAlias(ctx context.Context, mac, groupID, alias string) error {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return errors.New("group alias is required")
	}
	result, err := p.db.ExecContext(ctx, `INSERT INTO group_aliases(group_id,alias)
SELECT $2,$3 FROM devices d JOIN memberships m ON m.device_id=d.id AND m.group_id=$2
WHERE d.mac=$1 ON CONFLICT DO NOTHING`, mac, groupID, alias)
	if err != nil {
		return fmt.Errorf("add group alias: %w", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("read group alias result: %w", err)
	} else if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) EnsureDevice(ctx context.Context, mac, name, agentID string) (model.Device, error) {
	if strings.TrimSpace(mac) == "" {
		return model.Device{}, errors.New("mac is required")
	}
	if strings.TrimSpace(name) == "" {
		name = "设备" + mac[len(mac)-min(5, len(mac)):]
	}
	id := uuid.NewString()
	var d model.Device
	err := p.db.QueryRowContext(ctx, `INSERT INTO devices(id, mac, name, agent_id) VALUES($1,$2,$3,$4)
ON CONFLICT(mac) DO UPDATE SET name=EXCLUDED.name, agent_id=EXCLUDED.agent_id, last_seen_at=NOW()
RETURNING id::text, mac, name, agent_id, active_group_id::text, last_acked_seq, last_seen_at`, id, mac, name, agentID).
		Scan(&d.ID, &d.MAC, &d.Name, &d.AgentID, &d.ActiveGroupID, &d.LastAckedSeq, &d.LastSeenAt)
	if err != nil {
		return model.Device{}, fmt.Errorf("ensure device: %w", err)
	}
	return d, nil
}

// DeviceByMAC returns the device registered with mac.
func (p *Postgres) DeviceByMAC(ctx context.Context, mac string) (model.Device, error) {
	var d model.Device
	err := p.db.QueryRowContext(ctx, `SELECT id::text,mac,name,agent_id,active_group_id::text,last_acked_seq,last_seen_at
FROM devices WHERE mac=$1`, mac).
		Scan(&d.ID, &d.MAC, &d.Name, &d.AgentID, &d.ActiveGroupID, &d.LastAckedSeq, &d.LastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Device{}, ErrNotFound
	}
	if err != nil {
		return model.Device{}, fmt.Errorf("find device by mac: %w", err)
	}
	return d, nil
}

// ActiveGroup returns the device's current focus group.
func (p *Postgres) ActiveGroup(ctx context.Context, deviceID string) (model.Group, error) {
	var group model.Group
	err := p.db.QueryRowContext(ctx, `SELECT g.id::text,g.name,g.owner_device_id::text,g.invite_code,g.next_seq,g.created_at
FROM devices d JOIN groups g ON g.id=d.active_group_id WHERE d.id=$1`, deviceID).
		Scan(&group.ID, &group.Name, &group.OwnerID, &group.InviteCode, &group.NextSeq, &group.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Group{}, ErrNotFound
	}
	if err != nil {
		return model.Group{}, fmt.Errorf("find active group: %w", err)
	}
	return group, nil
}

// PendingMessages returns at most limit messages for the device's active group.
func (p *Postgres) PendingMessages(ctx context.Context, deviceID string, limit int) ([]model.DeviceMessage, error) {
	if limit <= 0 {
		return []model.DeviceMessage{}, nil
	}
	rows, err := p.db.QueryContext(ctx, `SELECT m.id::text,m.group_id::text,m.from_device_id::text,m.text,
m.audio_url,m.audio_status,m.duration_ms,m.seq,m.created_at,g.name
FROM messages m JOIN memberships member ON member.group_id=m.group_id
JOIN devices d ON d.id=member.device_id JOIN groups g ON g.id=m.group_id
JOIN device_group_acks ack ON ack.device_id=d.id AND ack.group_id=m.group_id
WHERE d.id=$1 AND m.group_id=d.active_group_id AND m.seq > ack.last_acked_seq
ORDER BY m.created_at,m.group_id,m.seq LIMIT $2`, deviceID, limit)
	if err != nil {
		return nil, fmt.Errorf("query pending messages: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = fmt.Errorf("close pending messages: %w", closeErr)
		}
	}()
	messages := make([]model.DeviceMessage, 0, limit)
	for rows.Next() {
		var message model.DeviceMessage
		if err := rows.Scan(&message.ID, &message.GroupID, &message.FromDeviceID, &message.Text,
			&message.AudioURL, &message.AudioStatus, &message.DurationMS, &message.Seq, &message.CreatedAt, &message.GroupName); err != nil {
			return nil, fmt.Errorf("scan pending message: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending messages: %w", err)
	}
	return messages, nil
}

// AckDevice advances a device's acknowledged sequence without allowing it to go backwards.
func (p *Postgres) AckDevice(ctx context.Context, deviceID, groupID string, seq int64) error {
	if seq < 0 {
		return errors.New("sequence must not be negative")
	}
	result, err := p.db.ExecContext(ctx, `INSERT INTO device_group_acks(device_id,group_id,last_acked_seq,updated_at)
VALUES($1,$2,$3,NOW()) ON CONFLICT(device_id,group_id) DO UPDATE
SET last_acked_seq=GREATEST(device_group_acks.last_acked_seq,EXCLUDED.last_acked_seq),updated_at=NOW()`, deviceID, groupID, seq)
	if err != nil {
		return fmt.Errorf("ack device message: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read ack result: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	if _, err := p.db.ExecContext(ctx, `UPDATE devices SET last_seen_at=NOW() WHERE id=$1`, deviceID); err != nil {
		return fmt.Errorf("update device heartbeat: %w", err)
	}
	return nil
}

// MemberDeviceIDs returns all devices in a group.
func (p *Postgres) MemberDeviceIDs(ctx context.Context, groupID string) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT m.device_id::text FROM memberships m JOIN devices d ON d.id=m.device_id
WHERE m.group_id=$1 AND d.active_group_id=$1`, groupID)
	if err != nil {
		return nil, fmt.Errorf("query group members: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = fmt.Errorf("close group members: %w", closeErr)
		}
	}()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan group member: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate group members: %w", err)
	}
	return ids, nil
}

// SwitchActiveGroup changes the device's focus to a group it belongs to.
func (p *Postgres) SwitchActiveGroup(ctx context.Context, mac, groupID string) (model.Group, error) {
	var group model.Group
	err := p.db.QueryRowContext(ctx, `UPDATE devices d SET active_group_id=$2,last_seen_at=NOW()
FROM memberships m JOIN groups g ON g.id=m.group_id
WHERE d.mac=$1 AND m.device_id=d.id AND m.group_id=$2
RETURNING g.id::text,g.name,g.owner_device_id::text,g.invite_code,g.next_seq,g.created_at`, mac, groupID).
		Scan(&group.ID, &group.Name, &group.OwnerID, &group.InviteCode, &group.NextSeq, &group.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Group{}, ErrNotFound
	}
	if err != nil {
		return model.Group{}, fmt.Errorf("switch active group: %w", err)
	}
	return group, nil
}

// SwitchActiveGroupByName changes focus using an exact, case-insensitive name.
// Fuzzy and ambiguous name resolution belongs to the group resolution layer.
func (p *Postgres) SwitchActiveGroupByName(ctx context.Context, mac, name string) (model.Group, error) {
	var groupID string
	err := p.db.QueryRowContext(ctx, `SELECT g.id::text FROM groups g
JOIN memberships m ON m.group_id=g.id JOIN devices d ON d.id=m.device_id
WHERE d.mac=$1 AND LOWER(TRIM(g.name))=LOWER(TRIM($2))`, mac, name).Scan(&groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Group{}, ErrNotFound
	}
	if err != nil {
		return model.Group{}, fmt.Errorf("find group by name: %w", err)
	}
	return p.SwitchActiveGroup(ctx, mac, groupID)
}

func (p *Postgres) CreateGroup(ctx context.Context, ownerMAC, name, inviteCode string) (model.Group, error) {
	if strings.TrimSpace(name) == "" {
		return model.Group{}, errors.New("group name is required")
	}
	if inviteCode == "" {
		inviteCode = fmt.Sprintf("%06d", uuid.New().ID()%1000000)
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Group{}, fmt.Errorf("begin create group: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var ownerID string
	if err = tx.QueryRowContext(ctx, `SELECT id::text FROM devices WHERE mac=$1`, ownerMAC).Scan(&ownerID); err != nil {
		return model.Group{}, fmt.Errorf("find owner: %w", err)
	}
	id := uuid.NewString()
	var g model.Group
	err = tx.QueryRowContext(ctx, `INSERT INTO groups(id,name,owner_device_id,invite_code) VALUES($1,$2,$3,$4)
RETURNING id::text,name,owner_device_id::text,invite_code,next_seq,created_at`, id, name, ownerID, inviteCode).
		Scan(&g.ID, &g.Name, &g.OwnerID, &g.InviteCode, &g.NextSeq, &g.CreatedAt)
	if err != nil {
		return model.Group{}, fmt.Errorf("create group: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO memberships(group_id,device_id,alias) VALUES($1,$2,$3)`, g.ID, ownerID, name); err != nil {
		return model.Group{}, fmt.Errorf("add owner membership: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO device_group_acks(device_id,group_id) VALUES($1,$2)`, ownerID, g.ID); err != nil {
		return model.Group{}, fmt.Errorf("initialize owner acknowledgement: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE devices SET active_group_id=$1 WHERE id=$2`, g.ID, ownerID); err != nil {
		return model.Group{}, fmt.Errorf("set active group: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return model.Group{}, fmt.Errorf("commit group: %w", err)
	}
	return g, nil
}

func (p *Postgres) AddMember(ctx context.Context, groupID, mac, alias string) (model.Membership, error) {
	if strings.TrimSpace(alias) == "" {
		alias = "设备" + mac
	}
	var m model.Membership
	err := p.db.QueryRowContext(ctx, `INSERT INTO memberships(group_id,device_id,alias)
SELECT $1,id,$3 FROM devices WHERE mac=$2 ON CONFLICT(group_id,device_id) DO UPDATE SET alias=EXCLUDED.alias
RETURNING group_id::text,device_id::text,alias,joined_at`, groupID, mac, alias).
		Scan(&m.GroupID, &m.DeviceID, &m.Alias, &m.JoinedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Membership{}, ErrNotFound
	}
	if err != nil {
		return model.Membership{}, fmt.Errorf("add member: %w", err)
	}
	if _, err = p.db.ExecContext(ctx, `INSERT INTO device_group_acks(device_id,group_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, m.DeviceID, groupID); err != nil {
		return model.Membership{}, fmt.Errorf("initialize member acknowledgement: %w", err)
	}
	if _, err = p.db.ExecContext(ctx, `UPDATE devices SET active_group_id=$1 WHERE id=$2`, groupID, m.DeviceID); err != nil {
		return model.Membership{}, fmt.Errorf("set member focus: %w", err)
	}
	return m, nil
}

func (p *Postgres) CreateMessage(ctx context.Context, groupID, fromMAC, text, audioURL string, durationMS int, idempotencyKey string) (model.Message, error) {
	if strings.TrimSpace(text) == "" {
		return model.Message{}, errors.New("message text is required")
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Message{}, fmt.Errorf("begin message: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if strings.TrimSpace(idempotencyKey) != "" {
		result, insertErr := tx.ExecContext(ctx, `INSERT INTO idempotency_keys(key,result) VALUES($1,'{}'::jsonb) ON CONFLICT(key) DO NOTHING`, idempotencyKey)
		if insertErr != nil {
			return model.Message{}, fmt.Errorf("claim idempotency key: %w", insertErr)
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return model.Message{}, fmt.Errorf("read idempotency claim: %w", rowsErr)
		}
		if rows == 0 {
			return model.Message{}, ErrConflict
		}
	}
	var deviceID string
	if err = tx.QueryRowContext(ctx, `SELECT id::text FROM devices WHERE mac=$1`, fromMAC).Scan(&deviceID); err != nil {
		return model.Message{}, fmt.Errorf("find sender: %w", err)
	}
	var seq int64
	err = tx.QueryRowContext(ctx, `UPDATE groups SET next_seq=next_seq+1 WHERE id=$1
AND EXISTS (SELECT 1 FROM memberships WHERE group_id=$1 AND device_id=$2) RETURNING next_seq`, groupID, deviceID).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Message{}, ErrNotFound
	}
	if err != nil {
		return model.Message{}, fmt.Errorf("allocate message sequence: %w", err)
	}
	var m model.Message
	id := uuid.NewString()
	status := "pending"
	if audioURL != "" {
		status = "ready"
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO messages(id,group_id,from_device_id,text,audio_url,audio_status,duration_ms,seq)
VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id::text,group_id::text,from_device_id::text,text,audio_url,audio_status,duration_ms,seq,created_at`, id, groupID, deviceID, text, audioURL, status, durationMS, seq).
		Scan(&m.ID, &m.GroupID, &m.FromDeviceID, &m.Text, &m.AudioURL, &m.AudioStatus, &m.DurationMS, &m.Seq, &m.CreatedAt)
	if err != nil {
		return model.Message{}, fmt.Errorf("insert message: %w", err)
	}
	if strings.TrimSpace(idempotencyKey) != "" {
		resultJSON, marshalErr := json.Marshal(m)
		if marshalErr != nil {
			return model.Message{}, fmt.Errorf("marshal message result: %w", marshalErr)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE idempotency_keys SET result=$2 WHERE key=$1`, idempotencyKey, resultJSON); err != nil {
			return model.Message{}, fmt.Errorf("save idempotency result: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return model.Message{}, fmt.Errorf("commit message: %w", err)
	}
	return m, nil
}

// SetMessageAudio stores the ready audio metadata after verifying message ownership.
func (p *Postgres) OwnsMessage(ctx context.Context, messageID, mac string) (bool, error) {
	var owns bool
	err := p.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages m JOIN devices d ON d.id=m.from_device_id WHERE m.id=$1 AND d.mac=$2)`, messageID, mac).Scan(&owns)
	return owns, err
}

func (p *Postgres) SetMessageAudio(ctx context.Context, messageID, mac, audioURL string, durationMS int) (model.Message, error) {
	var message model.Message
	err := p.db.QueryRowContext(ctx, `UPDATE messages m SET audio_url=$3,audio_status='ready',duration_ms=$4
FROM devices d WHERE m.id=$1 AND d.mac=$2 AND m.from_device_id=d.id AND m.audio_status='pending'
RETURNING m.id::text,m.group_id::text,m.from_device_id::text,m.text,m.audio_url,m.audio_status,m.duration_ms,m.seq,m.created_at`, messageID, mac, audioURL, durationMS).
		Scan(&message.ID, &message.GroupID, &message.FromDeviceID, &message.Text, &message.AudioURL, &message.AudioStatus, &message.DurationMS, &message.Seq, &message.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Message{}, ErrNotFound
	}
	if err != nil {
		return model.Message{}, fmt.Errorf("set message audio: %w", err)
	}
	return message, nil
}

// SetMessageFallback atomically claims a pending message for a generated audio
// fallback. The conditional update makes upload and timeout mutually exclusive.
func (p *Postgres) SetMessageFallback(ctx context.Context, messageID, audioURL string, durationMS int) (model.Message, error) {
	var message model.Message
	err := p.db.QueryRowContext(ctx, `UPDATE messages SET audio_url=$2,audio_status='ready',duration_ms=$3
WHERE id=$1 AND audio_status='pending'
RETURNING id::text,group_id::text,from_device_id::text,text,audio_url,audio_status,duration_ms,seq,created_at`, messageID, audioURL, durationMS).
		Scan(&message.ID, &message.GroupID, &message.FromDeviceID, &message.Text, &message.AudioURL, &message.AudioStatus, &message.DurationMS, &message.Seq, &message.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Message{}, ErrConflict
	}
	if err != nil {
		return model.Message{}, fmt.Errorf("set message fallback: %w", err)
	}
	return message, nil
}

// SetMessageFailed marks a pending message failed when fallback generation
// cannot produce audio. It is also conditional to preserve ready messages.
func (p *Postgres) SetMessageFailed(ctx context.Context, messageID string) error {
	result, err := p.db.ExecContext(ctx, `UPDATE messages SET audio_status='failed' WHERE id=$1 AND audio_status='pending'`, messageID)
	if err != nil {
		return fmt.Errorf("set message failed: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read message failure result: %w", err)
	}
	if count == 0 {
		return ErrConflict
	}
	return nil
}

func (p *Postgres) PutIdempotency(ctx context.Context, key string, value any) (bool, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return false, fmt.Errorf("marshal idempotency result: %w", err)
	}
	result, err := p.db.ExecContext(ctx, `INSERT INTO idempotency_keys(key,result) VALUES($1,$2) ON CONFLICT(key) DO NOTHING`, key, b)
	if err != nil {
		return false, fmt.Errorf("store idempotency key: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read idempotency result: %w", err)
	}
	return n == 1, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
