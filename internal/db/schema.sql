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
