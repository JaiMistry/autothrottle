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
    type TEXT NOT NULL CHECK (type IN ('qbittorrent', 'transmission')),
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

-- Key/value store; today it holds only the pause-all override, owned
-- by internal/settings.
CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_events_level ON events(level);

-- Throughput was sampled for an Analytics page that no longer exists.
DROP TABLE IF EXISTS bandwidth_samples;
DROP TABLE IF EXISTS bandwidth_rollups_hourly;

-- When AutoThrottle was running: one row per process run, its heartbeat
-- moved forward every minute. The gaps between runs are what the
-- Dashboard's throttle history shows as "not running", rather than as
-- hours in which nothing was throttled.
CREATE TABLE IF NOT EXISTS runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL
);

-- Spans during which at least one downloader was throttled. A NULL
-- ended_at is the period in progress, or one the process died during,
-- which the next start closes at the previous run's last heartbeat.
CREATE TABLE IF NOT EXISTS throttle_periods (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at TEXT NOT NULL,
    ended_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_runs_last_seen ON runs(last_seen_at);
CREATE INDEX IF NOT EXISTS idx_throttle_periods_started ON throttle_periods(started_at);

-- Session history: one row per playback session, opened when it starts
-- counting toward throttling and closed when it stops.
--
-- The scheduler tracks active sessions in memory because that is what
-- resolving throttle state needs; this table lets the Dashboard name
-- what is playing before a snapshot has described it. A NULL ended_at
-- means still playing — or that the process died while it was, which
-- CloseDanglingSessions resolves at startup.
CREATE TABLE IF NOT EXISTS session_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_server_id INTEGER NOT NULL REFERENCES media_servers(id) ON DELETE CASCADE,
    session_key TEXT NOT NULL,
    started_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    ended_at TEXT,
    -- Best-effort, for display only: nothing about throttling depends
    -- on either, and a server may report neither.
    title TEXT NOT NULL DEFAULT '',
    device TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_session_history_started ON session_history(started_at);
CREATE INDEX IF NOT EXISTS idx_session_history_server ON session_history(media_server_id, started_at);
-- Session keys are reused by media servers over time, so "the open row
-- for this server and key" is the only safe way to find what to close.
CREATE INDEX IF NOT EXISTS idx_session_history_open ON session_history(media_server_id, session_key) WHERE ended_at IS NULL;

-- Login sessions. Held in the database rather than in memory so a
-- restart — an update, a host reboot, a crash — does not sign everyone
-- out. Tokens are opaque random strings; nothing about the user is
-- stored, because there is only ever one user.
CREATE TABLE IF NOT EXISTS sessions (
    token TEXT PRIMARY KEY,
    expires_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at);

-- How long each session has been continuously paused. Held here rather
-- than in memory because the abandoned-pause timeout is measured in
-- tens of minutes and an update or a host reboot takes seconds: a
-- purely in-memory clock restarts on every one of them, so a stream
-- that had already been retired as abandoned comes back and throttles
-- for another full timeout with nobody watching it.
--
-- position_ms is stored alongside so a resumed stream can be told from
-- one that is merely still listed: if the position has moved since it
-- was recorded, the viewer came back and the clock no longer applies.
CREATE TABLE IF NOT EXISTS paused_sessions (
    media_server_id INTEGER NOT NULL REFERENCES media_servers(id) ON DELETE CASCADE,
    session_key TEXT NOT NULL,
    paused_since TEXT NOT NULL,
    position_ms INTEGER NOT NULL,
    PRIMARY KEY (media_server_id, session_key)
);

-- The login account created on the first-run setup screen. There is only
-- ever one, which the CHECK enforces at the database rather than trusting
-- the handler: a second insert fails instead of quietly adding a user.
-- Unused when credentials come from the environment, which always win.
CREATE TABLE IF NOT EXISTS account (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    username TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
