package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrProjectMergeSameProject is returned when source and target are the
	// same project row.
	ErrProjectMergeSameProject = errors.New("cannot merge a project into itself")

	// ErrProjectMergeIssueNumberCollision is returned when moving source issues
	// would violate the target's UNIQUE(project_id, number) constraint.
	ErrProjectMergeIssueNumberCollision = errors.New("project merge issue number collision")

	// ErrProjectMergeImportMappingCollision is returned when moving source import
	// mappings would violate the target's source identity uniqueness.
	ErrProjectMergeImportMappingCollision = errors.New("project merge import mapping collision")

	// ErrProjectMergeArchivedSource is returned when MergeProjects is asked
	// to merge from a project that's been archived via RemoveProject (#24).
	// Merging out of an archive is a restore-then-merge flow that doesn't
	// exist yet; rather than silently undoing the archive we refuse here.
	ErrProjectMergeArchivedSource = errors.New("source project is archived")

	// ErrProjectMergeArchivedTarget is returned when the target project is
	// archived. Folding live work into an archived project would resurrect
	// the archive's identity, which is exactly what archival is meant to
	// prevent.
	ErrProjectMergeArchivedTarget = errors.New("target project is archived")
)

// ProjectMergeCollisionError carries the issue numbers that blocked a merge.
type ProjectMergeCollisionError struct {
	Numbers []int64
}

func (e *ProjectMergeCollisionError) Error() string {
	return fmt.Sprintf("%v: %v", ErrProjectMergeIssueNumberCollision, e.Numbers)
}

func (e *ProjectMergeCollisionError) Unwrap() error {
	return ErrProjectMergeIssueNumberCollision
}

// ProjectMergeImportMappingCollision identifies one import mapping identity
// that already exists on the target project.
type ProjectMergeImportMappingCollision struct {
	Source     string
	ExternalID string
	ObjectType string
}

// ProjectMergeImportMappingCollisionError carries the import mapping
// identities that blocked a merge.
type ProjectMergeImportMappingCollisionError struct {
	Mappings []ProjectMergeImportMappingCollision
}

func (e *ProjectMergeImportMappingCollisionError) Error() string {
	parts := make([]string, 0, len(e.Mappings))
	for _, m := range e.Mappings {
		parts = append(parts, fmt.Sprintf("%s/%s/%s", m.Source, m.ObjectType, m.ExternalID))
	}
	return fmt.Sprintf("%v: %s", ErrProjectMergeImportMappingCollision, strings.Join(parts, ", "))
}

func (e *ProjectMergeImportMappingCollisionError) Unwrap() error {
	return ErrProjectMergeImportMappingCollision
}

// MergeProjectsParams identifies a source project to fold into a surviving
// target project. The target keeps its id and identity.
type MergeProjectsParams struct {
	SourceProjectID int64
	TargetProjectID int64
	TargetName      *string
}

// ProjectMergeResult summarizes the rows moved by MergeProjects.
type ProjectMergeResult struct {
	Source         Project `json:"source"`
	Target         Project `json:"target"`
	IssuesMoved    int64   `json:"issues_moved"`
	AliasesMoved   int64   `json:"aliases_moved"`
	EventsMoved    int64   `json:"events_moved"`
	PurgeLogsMoved int64   `json:"purge_logs_moved"`
}

// MergeProjects moves every project-scoped row from SourceProjectID into
// TargetProjectID, then deletes the source project. It never renumbers issues;
// callers must resolve any issue-number collisions before retrying.
func (d *DB) MergeProjects(ctx context.Context, p MergeProjectsParams) (ProjectMergeResult, error) {
	if p.SourceProjectID == p.TargetProjectID {
		return ProjectMergeResult{}, ErrProjectMergeSameProject
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return ProjectMergeResult{}, fmt.Errorf("begin merge projects: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	source, err := scanProject(tx.QueryRowContext(ctx, projectSelect+` WHERE id = ?`, p.SourceProjectID))
	if err != nil {
		return ProjectMergeResult{}, fmt.Errorf("load source project: %w", err)
	}
	target, err := scanProject(tx.QueryRowContext(ctx, projectSelect+` WHERE id = ?`, p.TargetProjectID))
	if err != nil {
		return ProjectMergeResult{}, fmt.Errorf("load target project: %w", err)
	}
	if source.DeletedAt != nil {
		return ProjectMergeResult{}, ErrProjectMergeArchivedSource
	}
	if target.DeletedAt != nil {
		return ProjectMergeResult{}, ErrProjectMergeArchivedTarget
	}

	collisions, err := projectMergeIssueNumberCollisions(ctx, tx, p.SourceProjectID, p.TargetProjectID)
	if err != nil {
		return ProjectMergeResult{}, err
	}
	if len(collisions) > 0 {
		return ProjectMergeResult{}, &ProjectMergeCollisionError{Numbers: collisions}
	}
	mappingCollisions, err := projectMergeImportMappingCollisions(ctx, tx, p.SourceProjectID, p.TargetProjectID)
	if err != nil {
		return ProjectMergeResult{}, err
	}
	if len(mappingCollisions) > 0 {
		return ProjectMergeResult{}, &ProjectMergeImportMappingCollisionError{Mappings: mappingCollisions}
	}

	kind, rootPath := aliasDefaultsForMergedIdentity(source.Identity)
	var existingAliasProjectID int64
	err = tx.QueryRowContext(ctx,
		`SELECT project_id FROM project_aliases WHERE alias_identity = ?`, source.Identity).Scan(&existingAliasProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO project_aliases(project_id, alias_identity, alias_kind, root_path)
			 VALUES(?, ?, ?, ?)`, source.ID, source.Identity, kind, rootPath); err != nil {
			return ProjectMergeResult{}, fmt.Errorf("preserve source identity alias: %w", err)
		}
	} else if err != nil {
		return ProjectMergeResult{}, fmt.Errorf("check source identity alias: %w", err)
	} else if existingAliasProjectID != source.ID && existingAliasProjectID != target.ID {
		return ProjectMergeResult{}, fmt.Errorf("preserve source identity alias: alias %q belongs to project %d", source.Identity, existingAliasProjectID)
	}

	issuesMoved, err := countProjectRows(ctx, tx, "issues", source.ID)
	if err != nil {
		return ProjectMergeResult{}, err
	}
	aliasesMoved, err := countProjectRows(ctx, tx, "project_aliases", source.ID)
	if err != nil {
		return ProjectMergeResult{}, err
	}
	eventsMoved, err := countProjectRows(ctx, tx, "events", source.ID)
	if err != nil {
		return ProjectMergeResult{}, err
	}
	purgeLogsMoved, err := countProjectRows(ctx, tx, "purge_log", source.ID)
	if err != nil {
		return ProjectMergeResult{}, err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE issues SET project_id = ? WHERE project_id = ?`, target.ID, source.ID); err != nil {
		return ProjectMergeResult{}, fmt.Errorf("move issues: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE links SET project_id = ? WHERE project_id = ?`, target.ID, source.ID); err != nil {
		return ProjectMergeResult{}, fmt.Errorf("move links: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE events SET project_id = ?, project_identity = ? WHERE project_id = ?`,
		target.ID, target.Identity, source.ID); err != nil {
		return ProjectMergeResult{}, fmt.Errorf("move events: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE purge_log SET project_id = ?, project_identity = ? WHERE project_id = ?`,
		target.ID, target.Identity, source.ID); err != nil {
		return ProjectMergeResult{}, fmt.Errorf("move purge log: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE import_mappings SET project_id = ? WHERE project_id = ?`, target.ID, source.ID); err != nil {
		return ProjectMergeResult{}, fmt.Errorf("move import mappings: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE project_aliases SET project_id = ? WHERE project_id = ?`, target.ID, source.ID); err != nil {
		return ProjectMergeResult{}, fmt.Errorf("move aliases: %w", err)
	}

	nextIssueNumber, err := mergedNextIssueNumber(ctx, tx, source, target)
	if err != nil {
		return ProjectMergeResult{}, err
	}
	if p.TargetName != nil {
		if _, err := tx.ExecContext(ctx,
			`UPDATE projects SET name = ?, next_issue_number = ? WHERE id = ?`,
			*p.TargetName, nextIssueNumber, target.ID); err != nil {
			return ProjectMergeResult{}, fmt.Errorf("update target project: %w", err)
		}
	} else if _, err := tx.ExecContext(ctx,
		`UPDATE projects SET next_issue_number = ? WHERE id = ?`,
		nextIssueNumber, target.ID); err != nil {
		return ProjectMergeResult{}, fmt.Errorf("update target next issue number: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, source.ID); err != nil {
		return ProjectMergeResult{}, fmt.Errorf("delete source project: %w", err)
	}

	mergedTarget, err := scanProject(tx.QueryRowContext(ctx, projectSelect+` WHERE id = ?`, target.ID))
	if err != nil {
		return ProjectMergeResult{}, fmt.Errorf("reload target project: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ProjectMergeResult{}, fmt.Errorf("commit merge projects: %w", err)
	}

	return ProjectMergeResult{
		Source:         source,
		Target:         mergedTarget,
		IssuesMoved:    issuesMoved,
		AliasesMoved:   aliasesMoved,
		EventsMoved:    eventsMoved,
		PurgeLogsMoved: purgeLogsMoved,
	}, nil
}

func projectMergeIssueNumberCollisions(ctx context.Context, tx *sql.Tx, sourceID, targetID int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT s.number
		FROM issues s
		INNER JOIN issues t ON t.project_id = ? AND t.number = s.number
		WHERE s.project_id = ?
		ORDER BY s.number
		LIMIT 20`, targetID, sourceID)
	if err != nil {
		return nil, fmt.Errorf("check project merge issue number collisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("scan project merge collision: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func projectMergeImportMappingCollisions(
	ctx context.Context,
	tx *sql.Tx,
	sourceID, targetID int64,
) ([]ProjectMergeImportMappingCollision, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT s.source, s.external_id, s.object_type
		FROM import_mappings s
		INNER JOIN import_mappings t
		  ON t.project_id = ?
		 AND t.source = s.source
		 AND t.external_id = s.external_id
		 AND t.object_type = s.object_type
		WHERE s.project_id = ?
		ORDER BY s.source, s.object_type, s.external_id
		LIMIT 20`, targetID, sourceID)
	if err != nil {
		return nil, fmt.Errorf("check project merge import mapping collisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ProjectMergeImportMappingCollision
	for rows.Next() {
		var c ProjectMergeImportMappingCollision
		if err := rows.Scan(&c.Source, &c.ExternalID, &c.ObjectType); err != nil {
			return nil, fmt.Errorf("scan project merge import mapping collision: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func aliasDefaultsForMergedIdentity(identity string) (kind, rootPath string) {
	if strings.HasPrefix(identity, "local://") {
		return "local", strings.TrimPrefix(identity, "local://")
	}
	return "git", "merged:" + identity
}

func countProjectRows(ctx context.Context, tx *sql.Tx, table string, projectID int64) (int64, error) {
	var n int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE project_id = ?`, projectID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count %s rows: %w", table, err)
	}
	return n, nil
}

func mergedNextIssueNumber(ctx context.Context, tx *sql.Tx, source, target Project) (int64, error) {
	next := target.NextIssueNumber
	if source.NextIssueNumber > next {
		next = source.NextIssueNumber
	}
	var maxNumber sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(number) FROM issues WHERE project_id = ?`, target.ID).Scan(&maxNumber); err != nil {
		return 0, fmt.Errorf("read merged max issue number: %w", err)
	}
	if maxNumber.Valid && maxNumber.Int64+1 > next {
		next = maxNumber.Int64 + 1
	}
	return next, nil
}
