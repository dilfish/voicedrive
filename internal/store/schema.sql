CREATE TABLE IF NOT EXISTS devices (
    id UUID PRIMARY KEY,
    mac TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    agent_id TEXT NOT NULL DEFAULT '',
    active_group_id UUID,
    last_acked_seq BIGINT NOT NULL DEFAULT 0,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS groups (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    owner_device_id UUID NOT NULL REFERENCES devices(id),
    invite_code TEXT NOT NULL UNIQUE,
    next_seq BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS memberships (
    group_id UUID NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    alias TEXT NOT NULL,
    muted BOOLEAN NOT NULL DEFAULT FALSE,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (group_id, device_id)
);

CREATE TABLE IF NOT EXISTS group_aliases (
    group_id UUID NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    alias TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (group_id, alias)
);

CREATE INDEX IF NOT EXISTS group_aliases_alias_idx ON group_aliases(alias);

CREATE TABLE IF NOT EXISTS messages (
    id UUID PRIMARY KEY,
    group_id UUID NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    from_device_id UUID NOT NULL REFERENCES devices(id),
    text TEXT NOT NULL,
    audio_url TEXT NOT NULL DEFAULT '',
    audio_status TEXT NOT NULL DEFAULT 'pending',
    duration_ms INTEGER NOT NULL DEFAULT 0,
    seq BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (group_id, seq)
);

CREATE TABLE IF NOT EXISTS idempotency_keys (
    key TEXT PRIMARY KEY,
    result JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS device_group_acks (
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    group_id UUID NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    last_acked_seq BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (device_id, group_id)
);

CREATE INDEX IF NOT EXISTS memberships_device_idx ON memberships(device_id);
CREATE INDEX IF NOT EXISTS messages_group_seq_idx ON messages(group_id, seq);
