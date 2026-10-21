package db

import (
	"database/sql"

	"github.com/JaiMistry/autothrottle/internal/schedule"
)

const windowColumns = `id, name, days, start_minute, end_minute, download_limit, upload_limit, throttle`

func scanWindow(row interface{ Scan(...any) error }, w *schedule.Window) error {
	return row.Scan(&w.ID, &w.Name, &w.Days, &w.Start, &w.End, &w.DownloadKBps, &w.UploadKBps, &w.Throttle)
}

// ListWindows returns every downloader's time windows, by downloader ID,
// each in the order they start.
func (d *DB) ListWindows() (map[int64][]schedule.Window, error) {
	rows, err := d.conn.Query(`SELECT downloader_id, ` + windowColumns + ` FROM downloader_windows ORDER BY downloader_id, start_minute, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make(map[int64][]schedule.Window)
	for rows.Next() {
		var downloaderID int64
		var w schedule.Window
		if err := rows.Scan(&downloaderID, &w.ID, &w.Name, &w.Days, &w.Start, &w.End, &w.DownloadKBps, &w.UploadKBps, &w.Throttle); err != nil {
			return nil, err
		}
		out[downloaderID] = append(out[downloaderID], w)
	}
	return out, rows.Err()
}

// DownloaderWindows returns one downloader's time windows in the order
// they start.
func (d *DB) DownloaderWindows(downloaderID int64) ([]schedule.Window, error) {
	rows, err := d.conn.Query(`SELECT `+windowColumns+` FROM downloader_windows WHERE downloader_id = ? ORDER BY start_minute, id`, downloaderID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []schedule.Window
	for rows.Next() {
		var w schedule.Window
		if err := scanWindow(rows, &w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// CreateWindow adds a time window to a downloader. It stores what it is
// given: checking it against the others is the caller's job.
func (d *DB) CreateWindow(downloaderID int64, w schedule.Window) (int64, error) {
	res, err := d.conn.Exec(
		`INSERT INTO downloader_windows (downloader_id, name, days, start_minute, end_minute, download_limit, upload_limit, throttle)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		downloaderID, w.Name, w.Days, w.Start, w.End, w.DownloadKBps, w.UploadKBps, w.Throttle,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateWindow replaces one of a downloader's time windows. A window
// that isn't that downloader's is sql.ErrNoRows.
func (d *DB) UpdateWindow(downloaderID int64, w schedule.Window) error {
	res, err := d.conn.Exec(
		`UPDATE downloader_windows SET name = ?, days = ?, start_minute = ?, end_minute = ?,
		 download_limit = ?, upload_limit = ?, throttle = ? WHERE id = ? AND downloader_id = ?`,
		w.Name, w.Days, w.Start, w.End, w.DownloadKBps, w.UploadKBps, w.Throttle, w.ID, downloaderID,
	)
	if err != nil {
		return err
	}
	return oneRow(res)
}

// DeleteWindow removes one of a downloader's time windows.
func (d *DB) DeleteWindow(downloaderID, windowID int64) error {
	res, err := d.conn.Exec(`DELETE FROM downloader_windows WHERE id = ? AND downloader_id = ?`, windowID, downloaderID)
	if err != nil {
		return err
	}
	return oneRow(res)
}

func oneRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
