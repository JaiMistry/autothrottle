package db

import (
	"database/sql"
	"fmt"
)

type MediaServer struct {
	ID                   int64
	Name                 string
	Type                 string // plex, emby, jellyfin
	URL                  string
	CredentialEncrypted  string
	SkipLocalPlayback    bool
	Enabled              bool
	CreatedAt, UpdatedAt string
}

type Downloader struct {
	ID                                       int64
	Name                                     string
	Type                                     string // qbittorrent, transmission
	URL                                      string
	CredentialEncrypted                      sql.NullString
	DefaultDownloadLimit, DefaultUploadLimit int64 // KB/s, 0 = unlimited
	BackupDownloadLimit, BackupUploadLimit   int64
	Enabled                                  bool
	CreatedAt, UpdatedAt                     string
}

type Mapping struct {
	ID            int64
	MediaServerID int64
	DownloaderID  int64
	Enabled       bool
	CreatedAt     string
}

type Event struct {
	ID                                     int64
	OccurredAt                             string
	Level                                  string // info, warn, error
	Message                                string
	MappingID, MediaServerID, DownloaderID sql.NullInt64
}

// --- media servers ---

func (d *DB) CreateMediaServer(m MediaServer) (int64, error) {
	res, err := d.conn.Exec(
		`INSERT INTO media_servers (name, type, url, credential_encrypted, skip_local_playback, enabled)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		m.Name, m.Type, m.URL, m.CredentialEncrypted, m.SkipLocalPlayback, m.Enabled,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) ListMediaServers() ([]MediaServer, error) {
	rows, err := d.conn.Query(`SELECT id, name, type, url, credential_encrypted, skip_local_playback, enabled, created_at, updated_at FROM media_servers ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []MediaServer
	for rows.Next() {
		var m MediaServer
		if err := rows.Scan(&m.ID, &m.Name, &m.Type, &m.URL, &m.CredentialEncrypted, &m.SkipLocalPlayback, &m.Enabled, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (d *DB) GetMediaServer(id int64) (MediaServer, error) {
	var m MediaServer
	err := d.conn.QueryRow(
		`SELECT id, name, type, url, credential_encrypted, skip_local_playback, enabled, created_at, updated_at
		 FROM media_servers WHERE id = ?`, id,
	).Scan(&m.ID, &m.Name, &m.Type, &m.URL, &m.CredentialEncrypted, &m.SkipLocalPlayback, &m.Enabled, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}

func (d *DB) UpdateMediaServer(m MediaServer) error {
	_, err := d.conn.Exec(
		`UPDATE media_servers SET name = ?, type = ?, url = ?, credential_encrypted = ?,
		 skip_local_playback = ?, enabled = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		 WHERE id = ?`,
		m.Name, m.Type, m.URL, m.CredentialEncrypted, m.SkipLocalPlayback, m.Enabled, m.ID,
	)
	return err
}

func (d *DB) DeleteMediaServer(id int64) error {
	_, err := d.conn.Exec(`DELETE FROM media_servers WHERE id = ?`, id)
	return err
}

// --- downloaders ---

func (d *DB) CreateDownloader(dl Downloader) (int64, error) {
	res, err := d.conn.Exec(
		`INSERT INTO downloaders (name, type, url, credential_encrypted,
		 default_download_limit, default_upload_limit, backup_download_limit, backup_upload_limit, enabled)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		dl.Name, dl.Type, dl.URL, dl.CredentialEncrypted,
		dl.DefaultDownloadLimit, dl.DefaultUploadLimit, dl.BackupDownloadLimit, dl.BackupUploadLimit, dl.Enabled,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) ListDownloaders() ([]Downloader, error) {
	rows, err := d.conn.Query(
		`SELECT id, name, type, url, credential_encrypted, default_download_limit, default_upload_limit,
		 backup_download_limit, backup_upload_limit, enabled, created_at, updated_at FROM downloaders ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Downloader
	for rows.Next() {
		var dl Downloader
		if err := rows.Scan(&dl.ID, &dl.Name, &dl.Type, &dl.URL, &dl.CredentialEncrypted,
			&dl.DefaultDownloadLimit, &dl.DefaultUploadLimit, &dl.BackupDownloadLimit, &dl.BackupUploadLimit,
			&dl.Enabled, &dl.CreatedAt, &dl.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, dl)
	}
	return out, rows.Err()
}

func (d *DB) GetDownloader(id int64) (Downloader, error) {
	var dl Downloader
	err := d.conn.QueryRow(
		`SELECT id, name, type, url, credential_encrypted, default_download_limit, default_upload_limit,
		 backup_download_limit, backup_upload_limit, enabled, created_at, updated_at FROM downloaders WHERE id = ?`, id,
	).Scan(&dl.ID, &dl.Name, &dl.Type, &dl.URL, &dl.CredentialEncrypted,
		&dl.DefaultDownloadLimit, &dl.DefaultUploadLimit, &dl.BackupDownloadLimit, &dl.BackupUploadLimit,
		&dl.Enabled, &dl.CreatedAt, &dl.UpdatedAt)
	return dl, err
}

func (d *DB) UpdateDownloader(dl Downloader) error {
	_, err := d.conn.Exec(
		`UPDATE downloaders SET name = ?, type = ?, url = ?, credential_encrypted = ?,
		 default_download_limit = ?, default_upload_limit = ?, backup_download_limit = ?, backup_upload_limit = ?,
		 enabled = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = ?`,
		dl.Name, dl.Type, dl.URL, dl.CredentialEncrypted,
		dl.DefaultDownloadLimit, dl.DefaultUploadLimit, dl.BackupDownloadLimit, dl.BackupUploadLimit,
		dl.Enabled, dl.ID,
	)
	return err
}

func (d *DB) DeleteDownloader(id int64) error {
	_, err := d.conn.Exec(`DELETE FROM downloaders WHERE id = ?`, id)
	return err
}

// --- mappings ---

func (d *DB) CreateMapping(mediaServerID, downloaderID int64) (int64, error) {
	res, err := d.conn.Exec(
		`INSERT INTO mappings (media_server_id, downloader_id, enabled) VALUES (?, ?, 1)`,
		mediaServerID, downloaderID,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) ListMappings() ([]Mapping, error) {
	rows, err := d.conn.Query(`SELECT id, media_server_id, downloader_id, enabled, created_at FROM mappings`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Mapping
	for rows.Next() {
		var m Mapping
		if err := rows.Scan(&m.ID, &m.MediaServerID, &m.DownloaderID, &m.Enabled, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// --- events ---

// InsertEvent records an event. An empty OccurredAt means now; tests
// set it to date one in the past.
func (d *DB) InsertEvent(e Event) error {
	err := d.insertEvent(e)
	if err == nil {
		d.eventsChanged()
	}
	return err
}

func (d *DB) insertEvent(e Event) error {
	if e.OccurredAt == "" {
		_, err := d.conn.Exec(
			`INSERT INTO events (level, message, mapping_id, media_server_id, downloader_id) VALUES (?, ?, ?, ?, ?)`,
			e.Level, e.Message, e.MappingID, e.MediaServerID, e.DownloaderID,
		)
		return err
	}
	_, err := d.conn.Exec(
		`INSERT INTO events (occurred_at, level, message, mapping_id, media_server_id, downloader_id) VALUES (?, ?, ?, ?, ?, ?)`,
		e.OccurredAt, e.Level, e.Message, e.MappingID, e.MediaServerID, e.DownloaderID,
	)
	return err
}

// EnableMappingsForMediaServer maps a new server to every downloader,
// switched on: a missing mapping means "do not throttle", which would
// leave a new pair silently doing nothing. Existing rows are untouched.
//
// `WHERE true` lets SQLite tell the ON CONFLICT is the INSERT's.
func (d *DB) EnableMappingsForMediaServer(mediaServerID int64) error {
	_, err := d.conn.Exec(
		`INSERT INTO mappings (media_server_id, downloader_id, enabled)
		 SELECT ?, id, 1 FROM downloaders WHERE true
		 ON CONFLICT(media_server_id, downloader_id) DO NOTHING`,
		mediaServerID,
	)
	if err != nil {
		return fmt.Errorf("seeding mappings for media server: %w", err)
	}
	return nil
}

// EnableMappingsForDownloader is EnableMappingsForMediaServer the other
// way round.
func (d *DB) EnableMappingsForDownloader(downloaderID int64) error {
	_, err := d.conn.Exec(
		`INSERT INTO mappings (media_server_id, downloader_id, enabled)
		 SELECT id, ?, 1 FROM media_servers WHERE true
		 ON CONFLICT(media_server_id, downloader_id) DO NOTHING`,
		downloaderID,
	)
	if err != nil {
		return fmt.Errorf("seeding mappings for downloader: %w", err)
	}
	return nil
}

// SetMappingForPair turns a server/downloader mapping on or off,
// creating it if needed: an unticked matrix cell has no row yet.
func (d *DB) SetMappingForPair(mediaServerID, downloaderID int64, enabled bool) error {
	_, err := d.conn.Exec(
		`INSERT INTO mappings (media_server_id, downloader_id, enabled)
		 VALUES (?, ?, ?)
		 ON CONFLICT(media_server_id, downloader_id) DO UPDATE SET enabled = excluded.enabled`,
		mediaServerID, downloaderID, enabled,
	)
	if err != nil {
		return fmt.Errorf("setting mapping: %w", err)
	}
	return nil
}
