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

	"github.com/dilfish/voicedrive/internal/model"
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
		return model.Membership{}, errors.New("alias is required")
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
	err = tx.QueryRowContext(ctx, `INSERT INTO messages(id,group_id,from_device_id,text,audio_url,duration_ms,seq)
VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id::text,group_id::text,from_device_id::text,text,audio_url,duration_ms,seq,created_at`, id, groupID, deviceID, text, audioURL, durationMS, seq).
		Scan(&m.ID, &m.GroupID, &m.FromDeviceID, &m.Text, &m.AudioURL, &m.DurationMS, &m.Seq, &m.CreatedAt)
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
