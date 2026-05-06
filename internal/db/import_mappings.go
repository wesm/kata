package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ImportMapping mirrors a row in import_mappings.
type ImportMapping struct {
	ID              int64      `json:"id"`
	Source          string     `json:"source"`
	ExternalID      string     `json:"external_id"`
	ObjectType      string     `json:"object_type"`
	ProjectID       int64      `json:"project_id"`
	IssueID         *int64     `json:"issue_id,omitempty"`
	CommentID       *int64     `json:"comment_id,omitempty"`
	LinkID          *int64     `json:"link_id,omitempty"`
	Label           *string    `json:"label,omitempty"`
	SourceUpdatedAt *time.Time `json:"source_updated_at,omitempty"`
	ImportedAt      time.Time  `json:"imported_at"`
}

// ImportMappingParams carries values for inserting or updating a source
// identity mapping.
type ImportMappingParams struct {
	Source          string
	ExternalID      string
	ObjectType      string
	ProjectID       int64
	IssueID         *int64
	CommentID       *int64
	LinkID          *int64
	Label           *string
	SourceUpdatedAt *time.Time
}

// UpsertImportMapping inserts or updates a source identity mapping.
func (d *DB) UpsertImportMapping(ctx context.Context, p ImportMappingParams) (ImportMapping, error) {
	return upsertImportMapping(ctx, d.DB, p)
}

func upsertImportMapping(ctx context.Context, e execQuerier, p ImportMappingParams) (ImportMapping, error) {
	_, err := e.ExecContext(ctx, `INSERT INTO import_mappings(
		source, external_id, object_type, project_id, issue_id, comment_id, link_id, label, source_updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(source, external_id, object_type, project_id) DO UPDATE SET
		issue_id=excluded.issue_id,
		comment_id=excluded.comment_id,
		link_id=excluded.link_id,
		label=excluded.label,
		source_updated_at=excluded.source_updated_at,
		imported_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		p.Source, p.ExternalID, p.ObjectType, p.ProjectID, p.IssueID, p.CommentID, p.LinkID, p.Label, p.SourceUpdatedAt)
	if err != nil {
		return ImportMapping{}, fmt.Errorf("upsert import mapping: %w", err)
	}
	return importMappingBySource(ctx, e, p.ProjectID, p.Source, p.ObjectType, p.ExternalID)
}

// ImportMappingBySource fetches one mapping by source identity.
func (d *DB) ImportMappingBySource(ctx context.Context, projectID int64, source, objectType, externalID string) (ImportMapping, error) {
	return importMappingBySource(ctx, d.DB, projectID, source, objectType, externalID)
}

func importMappingBySource(ctx context.Context, q queryer, projectID int64, source, objectType, externalID string) (ImportMapping, error) {
	row := q.QueryRowContext(ctx, importMappingSelect+` WHERE project_id = ? AND source = ? AND object_type = ? AND external_id = ?`,
		projectID, source, objectType, externalID)
	return scanImportMapping(row)
}

// ImportMappingsByProjectSource returns every mapping for a project/source pair.
func (d *DB) ImportMappingsByProjectSource(ctx context.Context, projectID int64, source string) ([]ImportMapping, error) {
	rows, err := d.QueryContext(ctx, importMappingSelect+` WHERE project_id = ? AND source = ? ORDER BY id ASC`, projectID, source)
	if err != nil {
		return nil, fmt.Errorf("list import mappings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ImportMapping
	for rows.Next() {
		m, err := scanImportMapping(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

const importMappingSelect = `SELECT id, source, external_id, object_type, project_id, issue_id, comment_id, link_id, label, source_updated_at, imported_at FROM import_mappings`

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type execQuerier interface {
	queryer
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func scanImportMapping(r rowScanner) (ImportMapping, error) {
	var m ImportMapping
	var issueID, commentID, linkID sql.NullInt64
	var label sql.NullString
	var sourceUpdated sql.NullTime
	err := r.Scan(&m.ID, &m.Source, &m.ExternalID, &m.ObjectType, &m.ProjectID,
		&issueID, &commentID, &linkID, &label, &sourceUpdated, &m.ImportedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ImportMapping{}, ErrNotFound
	}
	if err != nil {
		return ImportMapping{}, fmt.Errorf("scan import mapping: %w", err)
	}
	if issueID.Valid {
		m.IssueID = &issueID.Int64
	}
	if commentID.Valid {
		m.CommentID = &commentID.Int64
	}
	if linkID.Valid {
		m.LinkID = &linkID.Int64
	}
	if label.Valid {
		m.Label = &label.String
	}
	if sourceUpdated.Valid {
		m.SourceUpdatedAt = &sourceUpdated.Time
	}
	return m, nil
}
