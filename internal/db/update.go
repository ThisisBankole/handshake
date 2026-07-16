package db

import (
	"database/sql"
	"fmt"
)

// UpdateStatus is the locally cached result of Handshake's release check.
// It deliberately contains no user, repository, or agent-session data.
type UpdateStatus struct {
	LastCheckedAt    int64
	ETag             string
	InstalledVersion string
	LatestVersion    string
	ReleaseURL       string
	LastError        string
}

func (d *Database) GetUpdateStatus() (*UpdateStatus, error) {
	status := &UpdateStatus{}
	err := d.db.QueryRow(`SELECT last_checked_at, etag, installed_version, latest_version, release_url, last_error
		FROM update_status WHERE id = 1`).Scan(
		&status.LastCheckedAt, &status.ETag, &status.InstalledVersion,
		&status.LatestVersion, &status.ReleaseURL, &status.LastError)
	if err == sql.ErrNoRows {
		return status, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get update status: %w", err)
	}
	return status, nil
}

func (d *Database) SaveUpdateStatus(status *UpdateStatus) error {
	if status == nil {
		return fmt.Errorf("update status is required")
	}
	_, err := d.db.Exec(`INSERT INTO update_status
		(id, last_checked_at, etag, installed_version, latest_version, release_url, last_error)
		VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		  last_checked_at = excluded.last_checked_at,
		  etag = excluded.etag,
		  installed_version = excluded.installed_version,
		  latest_version = excluded.latest_version,
		  release_url = excluded.release_url,
		  last_error = excluded.last_error`,
		status.LastCheckedAt, status.ETag, status.InstalledVersion,
		status.LatestVersion, status.ReleaseURL, status.LastError)
	if err != nil {
		return fmt.Errorf("save update status: %w", err)
	}
	return nil
}
