package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	KnowledgeAuthoringJobPending = "pending"
	KnowledgeAuthoringJobRunning = "running"
	KnowledgeAuthoringJobFailed  = "failed"
)

var ErrKnowledgeAuthoringJobNotFound = errors.New("knowledge authoring job not found")

// KnowledgeAuthoringJob is the durable work item that turns a factual
// checkpoint into current AI-authored project documents.
type KnowledgeAuthoringJob struct {
	ProjectID      string
	TargetRevision int64
	State          string
	Attempts       int
	NotBefore      int64
	ClaimedAt      int64
	LastError      string
	UpdatedAt      int64
}

// EnqueueKnowledgeAuthoring coalesces rapid checkpoints into one job for the
// newest facts revision. A newer revision always replaces an older target.
func (d *Database) EnqueueKnowledgeAuthoring(projectID string, targetRevision int64, notBefore time.Time) error {
	if projectID == "" || targetRevision <= 0 {
		return fmt.Errorf("knowledge authoring job requires project ID and revision")
	}
	now := time.Now().Unix()
	_, err := d.db.Exec(`INSERT INTO knowledge_authoring_jobs
		(project_id, target_revision, state, attempts, not_before, claimed_at, last_error, updated_at)
		VALUES (?, ?, ?, 0, ?, 0, '', ?)
		ON CONFLICT(project_id) DO UPDATE SET
		  target_revision = CASE WHEN excluded.target_revision > knowledge_authoring_jobs.target_revision THEN excluded.target_revision ELSE knowledge_authoring_jobs.target_revision END,
		  state = CASE WHEN excluded.target_revision >= knowledge_authoring_jobs.target_revision THEN excluded.state ELSE knowledge_authoring_jobs.state END,
		  attempts = CASE WHEN excluded.target_revision >= knowledge_authoring_jobs.target_revision THEN 0 ELSE knowledge_authoring_jobs.attempts END,
		  not_before = CASE WHEN excluded.target_revision >= knowledge_authoring_jobs.target_revision THEN excluded.not_before ELSE knowledge_authoring_jobs.not_before END,
		  claimed_at = CASE WHEN excluded.target_revision >= knowledge_authoring_jobs.target_revision THEN 0 ELSE knowledge_authoring_jobs.claimed_at END,
		  last_error = CASE WHEN excluded.target_revision >= knowledge_authoring_jobs.target_revision THEN '' ELSE knowledge_authoring_jobs.last_error END,
		  updated_at = excluded.updated_at`,
		projectID, targetRevision, KnowledgeAuthoringJobPending, notBefore.Unix(), now)
	if err != nil {
		return fmt.Errorf("enqueue knowledge authoring: %w", err)
	}
	return nil
}

// ClaimNextKnowledgeAuthoringJob atomically leases one due job. SQLite's
// single-writer connection makes this compare-and-update safe across daemon
// processes as well as worker goroutines.
func (d *Database) ClaimNextKnowledgeAuthoringJob(now time.Time) (*KnowledgeAuthoringJob, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin claim knowledge authoring: %w", err)
	}
	defer tx.Rollback()
	job := &KnowledgeAuthoringJob{}
	err = tx.QueryRow(`SELECT project_id, target_revision, state, attempts, not_before, claimed_at, last_error, updated_at
		FROM knowledge_authoring_jobs
		WHERE state = ? AND not_before <= ?
		ORDER BY not_before ASC, updated_at ASC LIMIT 1`, KnowledgeAuthoringJobPending, now.Unix()).Scan(
		&job.ProjectID, &job.TargetRevision, &job.State, &job.Attempts, &job.NotBefore, &job.ClaimedAt, &job.LastError, &job.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select knowledge authoring job: %w", err)
	}
	result, err := tx.Exec(`UPDATE knowledge_authoring_jobs SET state = ?, attempts = attempts + 1, claimed_at = ?, updated_at = ?
		WHERE project_id = ? AND state = ?`, KnowledgeAuthoringJobRunning, now.Unix(), now.Unix(), job.ProjectID, KnowledgeAuthoringJobPending)
	if err != nil {
		return nil, fmt.Errorf("claim knowledge authoring job: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit knowledge authoring claim: %w", err)
	}
	job.State = KnowledgeAuthoringJobRunning
	job.Attempts++
	job.ClaimedAt = now.Unix()
	job.UpdatedAt = now.Unix()
	return job, nil
}

func (d *Database) CompleteKnowledgeAuthoringJob(projectID string) error {
	_, err := d.db.Exec(`DELETE FROM knowledge_authoring_jobs WHERE project_id = ?`, projectID)
	if err != nil {
		return fmt.Errorf("complete knowledge authoring job: %w", err)
	}
	return nil
}

func (d *Database) FailKnowledgeAuthoringJob(projectID, message string, retryAt time.Time, retry bool) error {
	state := KnowledgeAuthoringJobFailed
	if retry {
		state = KnowledgeAuthoringJobPending
	}
	_, err := d.db.Exec(`UPDATE knowledge_authoring_jobs SET state = ?, not_before = ?, claimed_at = 0, last_error = ?, updated_at = ? WHERE project_id = ?`,
		state, retryAt.Unix(), message, time.Now().Unix(), projectID)
	if err != nil {
		return fmt.Errorf("fail knowledge authoring job: %w", err)
	}
	return nil
}

func (d *Database) GetKnowledgeAuthoringJob(projectID string) (*KnowledgeAuthoringJob, error) {
	job := &KnowledgeAuthoringJob{}
	err := d.db.QueryRow(`SELECT project_id, target_revision, state, attempts, not_before, claimed_at, last_error, updated_at
		FROM knowledge_authoring_jobs WHERE project_id = ?`, projectID).Scan(
		&job.ProjectID, &job.TargetRevision, &job.State, &job.Attempts, &job.NotBefore, &job.ClaimedAt, &job.LastError, &job.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrKnowledgeAuthoringJobNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get knowledge authoring job: %w", err)
	}
	return job, nil
}

func (d *Database) ListKnowledgeAuthoringJobs() ([]*KnowledgeAuthoringJob, error) {
	rows, err := d.db.Query(`SELECT project_id, target_revision, state, attempts, not_before, claimed_at, last_error, updated_at
		FROM knowledge_authoring_jobs ORDER BY updated_at DESC, project_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list knowledge authoring jobs: %w", err)
	}
	defer rows.Close()
	var jobs []*KnowledgeAuthoringJob
	for rows.Next() {
		job := &KnowledgeAuthoringJob{}
		if err := rows.Scan(&job.ProjectID, &job.TargetRevision, &job.State, &job.Attempts, &job.NotBefore, &job.ClaimedAt, &job.LastError, &job.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan knowledge authoring job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge authoring jobs: %w", err)
	}
	return jobs, nil
}

// RecoverKnowledgeAuthoringJobs returns leases abandoned by a stopped daemon
// to the pending queue before the worker starts.
func (d *Database) RecoverKnowledgeAuthoringJobs() error {
	_, err := d.db.Exec(`UPDATE knowledge_authoring_jobs SET state = ?, claimed_at = 0, not_before = ?, updated_at = ? WHERE state = ?`,
		KnowledgeAuthoringJobPending, time.Now().Unix(), time.Now().Unix(), KnowledgeAuthoringJobRunning)
	if err != nil {
		return fmt.Errorf("recover knowledge authoring jobs: %w", err)
	}
	return nil
}

// GetKnowledgeProjectRoot returns the local root captured by the latest
// factual snapshot, never a path supplied by an authoring model.
func (d *Database) GetKnowledgeProjectRoot(projectID string) (string, error) {
	var root string
	err := d.db.QueryRow(`SELECT project_instances.root_path
		FROM knowledge_state
		JOIN git_snapshots ON git_snapshots.id = knowledge_state.last_snapshot_id
		JOIN project_instances ON project_instances.id = git_snapshots.instance_id
		WHERE knowledge_state.project_id = ?`, projectID).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrProjectInstanceNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get knowledge project root: %w", err)
	}
	return root, nil
}
