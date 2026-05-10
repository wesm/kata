package main

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/wesm/kata/internal/shortid"
)

// ResolvedRef is what a CLI command passes to the API: the project
// identifier (always a name string; the daemon resolves it to a project_id)
// and the issue ref string suitable for use as the {ref} path component.
type ResolvedRef struct {
	ProjectName string
	RefForAPI   string
}

// ResolveRef parses a positional CLI argument as either a qualified short_id
// ("kata#abc4"), a bare short_id ("abc4"), or a 26-char ULID. workspaceProject
// is the project name read from .kata.toml; it is required for the bare and
// ULID forms.
func ResolveRef(arg, workspaceProject string) (ResolvedRef, error) {
	if _, err := strconv.Atoi(arg); err == nil {
		return ResolvedRef{}, fmt.Errorf("%q looks like a legacy issue number; use a short_id (e.g. abc4) or kata#abc4", arg)
	}
	parsed, err := shortid.Parse(arg)
	if err != nil {
		if errors.Is(err, shortid.ErrInvalidRef) {
			return ResolvedRef{}, fmt.Errorf("%q is not a valid issue ref: %w", arg, err)
		}
		return ResolvedRef{}, err
	}
	switch {
	case parsed.Project != "":
		return ResolvedRef{ProjectName: parsed.Project, RefForAPI: parsed.ShortID}, nil
	case workspaceProject == "":
		return ResolvedRef{}, fmt.Errorf("no project bound to this workspace; use a qualified ref (e.g. kata#abc4)")
	case parsed.ULID != "":
		return ResolvedRef{ProjectName: workspaceProject, RefForAPI: parsed.ULID}, nil
	default:
		return ResolvedRef{ProjectName: workspaceProject, RefForAPI: parsed.ShortID}, nil
	}
}
