CREATE TABLE IF NOT EXISTS media_servers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('plex', 'emby', 'jellyfin')),
    url TEXT NOT NULL,
    credential_encrypted TEXT NOT NULL,
    skip_local_playback INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS downloaders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('qbittorrent', 'transmission', 'deluge')),
    url TEXT NOT NULL,
    credential_encrypted TEXT,
    default_download_limit INTEGER NOT NULL DEFAULT 0,
    default_upload_limit INTEGER NOT NULL DEFAULT 0,
    backup_download_limit INTEGER NOT NULL DEFAULT 0,
    backup_upload_limit INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS mappings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_server_id INTEGER NOT NULL REFERENCES media_servers(id) ON DELETE CASCADE,
    downloader_id INTEGER NOT NULL REFERENCES downloaders(id) ON DELETE CASCADE,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(media_server_id, downloader_id)
);

CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    level TEXT NOT NULL CHECK (level IN ('info', 'warn', 'error')),
    message TEXT NOT NULL,
    mapping_id INTEGER REFERENCES mappings(id) ON DELETE SET NULL,
    media_server_id INTEGER REFERENCES media_servers(id) ON DELETE SET NULL,
    downloader_id INTEGER REFERENCES downloaders(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_mappings_media_server ON mappings(media_server_id);
CREATE INDEX IF NOT EXISTS idx_mappings_downloader ON mappings(downloader_id);
CREATE INDEX IF NOT EXISTS idx_events_occurred_at ON events(occurred_at);

-- Key/value store for internal/settings (the pause switch).
CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_events_level ON events(level);

-- Left by the removed Analytics page.
DROP TABLE IF EXISTS bandwidth_samples;
DROP TABLE IF EXISTS bandwidth_rollups_hourly;

-- One row per process run, its heartbeat moved forward every minute. The
-- gaps between runs are what the throttle history shows as "not running".
CREATE TABLE IF NOT EXISTS runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL
);

-- Spans during which anything was throttled. A NULL ended_at is the
-- period in progress, or one the next start closes at the last heartbeat.
CREATE TABLE IF NOT EXISTS throttle_periods (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at TEXT NOT NULL,
    ended_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_runs_last_seen ON runs(last_seen_at);
CREATE INDEX IF NOT EXISTS idx_throttle_periods_started ON throttle_periods(started_at);

-- One row per playback session while it counts toward throttling. A
-- NULL ended_at is still playing, or one the next start closes.
CREATE TABLE IF NOT EXISTS session_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_server_id INTEGER NOT NULL REFERENCES media_servers(id) ON DELETE CASCADE,
    session_key TEXT NOT NULL,
    started_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    ended_at TEXT,
    -- Display only, and best-effort.
    title TEXT NOT NULL DEFAULT '',
    device TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_session_history_started ON session_history(started_at);
CREATE INDEX IF NOT EXISTS idx_session_history_server ON session_history(media_server_id, started_at);
-- Servers reuse session keys, so a session is found by its open row.
CREATE INDEX IF NOT EXISTS idx_session_history_open ON session_history(media_server_id, session_key) WHERE ended_at IS NULL;

-- Login sessions, kept here so a restart does not sign anyone out. token
-- is the cookie value's SHA-256, never the value itself.
CREATE TABLE IF NOT EXISTS sessions (
    token TEXT PRIMARY KEY,
    expires_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at);

-- How long each session has been continuously paused, kept here so a
-- restart does not revive a stream already retired as abandoned.
-- position_ms tells a resumed stream from one merely still listed.
CREATE TABLE IF NOT EXISTS paused_sessions (
    media_server_id INTEGER NOT NULL REFERENCES media_servers(id) ON DELETE CASCADE,
    session_key TEXT NOT NULL,
    paused_since TEXT NOT NULL,
    position_ms INTEGER NOT NULL,
    PRIMARY KEY (media_server_id, session_key)
);

-- The one login account, from the first-run screen; the CHECK makes a
-- second insert fail. Credentials from the environment win over it.
CREATE TABLE IF NOT EXISTS account (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    username TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
