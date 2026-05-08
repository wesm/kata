package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// IdentityChoice is the (identity, name) pair resolved by PickInitIdentity.
type IdentityChoice struct {
	Identity string
	Name     string
}

// ErrIdentityConflict signals that an existing .kata.toml declares an
// identity different from the one the caller supplied. Callers must
// require an explicit replace=true to override; daemon maps this to
// project_binding_conflict (409), CLI maps it to ExitConflict.
var ErrIdentityConflict = errors.New(".kata.toml declares a different identity")

// ErrNoIdentitySource signals that no project identity could be
// derived: there is no .kata.toml, no caller-supplied identity, and no
// git workspace to read a remote from. Daemon and CLI both surface
// this as a validation error.
var ErrNoIdentitySource = errors.New("cannot derive project identity outside a git workspace")

// PickInitIdentity decides the (identity, name) pair for kata init.
// The same logic runs on the daemon (path-based init, where the daemon
// reads .kata.toml from its own filesystem) and on the client (path-
// free init, where the client reads .kata.toml from its workspace and
// sends only the resolved identity to the daemon).
//
// Resolution order:
//
//  1. Existing .kata.toml + conflicting input identity (no replace) →
//     ErrIdentityConflict.
//  2. Existing .kata.toml → use its identity; explicit inputName wins
//     over the toml name; fall back to last identity segment when the
//     toml name is empty.
//  3. Caller-supplied input identity → use it; name from inputName or
//     last identity segment.
//  4. Discovered git root → derive identity via ComputeAliasIdentity.
//  5. Otherwise → ErrNoIdentitySource.
func PickInitIdentity(disc DiscoveredPaths, tomlCfg *ProjectConfig, inputIdentity, inputName string, replace bool) (IdentityChoice, error) {
	switch {
	case tomlCfg != nil && inputIdentity != "" && tomlCfg.Project.Identity != inputIdentity:
		if !replace {
			return IdentityChoice{}, ErrIdentityConflict
		}
		return IdentityChoice{
			Identity: inputIdentity,
			Name:     PickName(inputName, inputIdentity),
		}, nil
	case tomlCfg != nil:
		identity := tomlCfg.Project.Identity
		name := PickName(inputName, tomlCfg.Project.Name)
		if name == "" {
			name = PickName("", identity)
		}
		return IdentityChoice{Identity: identity, Name: name}, nil
	case inputIdentity != "":
		return IdentityChoice{
			Identity: inputIdentity,
			Name:     PickName(inputName, inputIdentity),
		}, nil
	default:
		if disc.GitRoot == "" {
			return IdentityChoice{}, ErrNoIdentitySource
		}
		info, err := ComputeAliasIdentity(disc)
		if err != nil {
			return IdentityChoice{}, err
		}
		return IdentityChoice{
			Identity: info.Identity,
			Name:     PickName(inputName, info.Identity),
		}, nil
	}
}

// PickName returns explicit if non-empty, otherwise the last `/` or
// `:`-separated segment of identity (so "github.com/wesm/kata" → "kata").
func PickName(explicit, identity string) string {
	if explicit != "" {
		return explicit
	}
	return lastSegment(identity)
}

// WriteDestination returns the directory where .kata.toml should be
// written for a kata init invocation: workspace root if discovered,
// else git root, else the absolute start path.
func WriteDestination(disc DiscoveredPaths, startPath string) string {
	if disc.WorkspaceRoot != "" {
		return disc.WorkspaceRoot
	}
	if disc.GitRoot != "" {
		return disc.GitRoot
	}
	return startPath
}

// DiscoveredPaths is the result of walking upward from a start path.
// Both fields may be empty (no .kata.toml and no .git ancestor).
type DiscoveredPaths struct {
	WorkspaceRoot string // first ancestor with .kata.toml (inclusive)
	GitRoot       string // first ancestor with .git (inclusive)
}

// AliasInfo is the alias-identity record derived from a workspace.
type AliasInfo struct {
	Identity string // git remote (normalized) or "local://<abs path>"
	Kind     string // "git" | "local"
	RootPath string // GitRoot when present, else WorkspaceRoot
}

// DiscoverPaths walks upward from startPath looking for .kata.toml (W) and
// .git (G). Both lookups are independent and inclusive of startPath itself.
// startPath must point at an existing file or directory; a missing start path
// is reported as a resolution error so a typo like `--workspace /no/such/dir`
// fails loud instead of silently resolving an unrelated ancestor.
// Errors from os.Stat that aren't "not exist" (permission denied, etc.) on
// ancestor lookups also propagate.
func DiscoverPaths(startPath string) (DiscoveredPaths, error) {
	abs, err := filepath.Abs(startPath)
	if err != nil {
		return DiscoveredPaths{}, fmt.Errorf("abs %s: %w", startPath, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return DiscoveredPaths{}, fmt.Errorf("stat %s: %w", abs, err)
	}
	// If the start path is a file, walk from its parent directory so we don't
	// stat <file>/.kata.toml (which fails with ENOTDIR rather than not-found).
	walkRoot := abs
	if !info.IsDir() {
		walkRoot = filepath.Dir(abs)
	}
	d := DiscoveredPaths{}
	if d.WorkspaceRoot, err = walkUp(walkRoot, ProjectConfigFilename, false); err != nil {
		return DiscoveredPaths{}, fmt.Errorf("discover %s: %w", ProjectConfigFilename, err)
	}
	if d.GitRoot, err = walkUp(walkRoot, ".git", true); err != nil {
		return DiscoveredPaths{}, fmt.Errorf("discover .git: %w", err)
	}
	return d, nil
}

// walkUp returns the first ancestor (inclusive) containing the named entry,
// or "" if none. allowDir lets the entry be either a file or directory.
// os.IsNotExist failures are normal during traversal; other errors surface so
// callers see e.g. permission-denied instead of treating it as "not found".
func walkUp(start, entry string, allowDir bool) (string, error) {
	dir := start
	for {
		path := filepath.Join(dir, entry)
		info, err := os.Stat(path)
		switch {
		case err == nil:
			if info.IsDir() {
				if allowDir {
					return dir, nil
				}
			} else {
				return dir, nil
			}
		case !os.IsNotExist(err):
			return "", fmt.Errorf("stat %s: %w", path, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// ComputeAliasIdentity derives the alias for a workspace per spec §2.4. Order:
// 1. GitRoot with remote → normalized origin URL
// 2. GitRoot without remote → local://<abs(GitRoot)>
// 3. WorkspaceRoot only → local://<abs(WorkspaceRoot)>
// 4. Neither → error
func ComputeAliasIdentity(d DiscoveredPaths) (AliasInfo, error) {
	if d.GitRoot != "" {
		remote, err := readGitRemote(d.GitRoot)
		if err != nil {
			return AliasInfo{}, err
		}
		if remote != "" {
			id, err := NormalizeRemoteURL(remote)
			if err != nil {
				return AliasInfo{}, err
			}
			return AliasInfo{Identity: id, Kind: "git", RootPath: d.GitRoot}, nil
		}
		return AliasInfo{
			Identity: "local://" + d.GitRoot,
			Kind:     "local",
			RootPath: d.GitRoot,
		}, nil
	}
	if d.WorkspaceRoot != "" {
		return AliasInfo{
			Identity: "local://" + d.WorkspaceRoot,
			Kind:     "local",
			RootPath: d.WorkspaceRoot,
		}, nil
	}
	return AliasInfo{}, fmt.Errorf("no workspace or git root discovered")
}

// readGitRemote returns the URL of "origin" (or the first remote listed by
// `git remote` when no origin exists). Returns ("", nil) if no remotes.
func readGitRemote(gitRoot string) (string, error) {
	out, err := runGit(gitRoot, "remote")
	if err != nil {
		return "", fmt.Errorf("git remote: %w", err)
	}
	remotes := strings.Fields(strings.TrimSpace(out))
	if len(remotes) == 0 {
		return "", nil
	}
	target := "origin"
	hasOrigin := false
	for _, r := range remotes {
		if r == "origin" {
			hasOrigin = true
			break
		}
	}
	if !hasOrigin {
		target = remotes[0]
	}
	url, err := runGit(gitRoot, "remote", "get-url", target)
	if err != nil {
		return "", fmt.Errorf("git remote get-url %s: %w", target, err)
	}
	return strings.TrimSpace(url), nil
}

func runGit(dir string, args ...string) (string, error) {
	//nolint:gosec // git binary is fixed; args are caller-supplied subcommand flags.
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// scpLikeRE matches "user@host:path[/...]" SCP-style git URLs.
var scpLikeRE = regexp.MustCompile(`^([^@\s]+)@([^:\s]+):(.+)$`)

// NormalizeRemoteURL strips credentials, normalizes SSH↔HTTPS, drops trailing
// .git, and returns "host/path" form (e.g. "github.com/wesm/kata").
// Percent-encoded characters are decoded and spaces replaced with hyphens so
// the result satisfies the identity charset (Azure DevOps remotes commonly
// contain %20 in org/project names).
func NormalizeRemoteURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty remote url")
	}
	var result string
	if m := scpLikeRE.FindStringSubmatch(raw); m != nil {
		host := m[2]
		path := strings.TrimSuffix(m[3], ".git")
		result = host + "/" + path
	} else {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return "", fmt.Errorf("parse remote url %q: not a recognized form", raw)
		}
		host := u.Host
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		path := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
		if path == "" {
			return host, nil
		}
		result = host + "/" + path
	}
	if decoded, err := url.PathUnescape(result); err == nil {
		result = decoded
	}
	result = strings.ReplaceAll(result, " ", "-")
	return result, nil
}

var identityCharsetRE = regexp.MustCompile(`^[A-Za-z0-9._:/\-]+$`)

// ValidateIdentity enforces the spec §2.4 charset and forbids whitespace and
// embedded URL credentials.
func ValidateIdentity(id string) error {
	if id == "" {
		return fmt.Errorf("identity must be non-empty")
	}
	for _, r := range id {
		if unicode.IsSpace(r) {
			return fmt.Errorf("identity contains whitespace: %q", id)
		}
	}
	if strings.HasPrefix(id, "http://") || strings.HasPrefix(id, "https://") {
		// reject embedded credentials.
		if strings.Contains(id, "@") {
			return fmt.Errorf("identity must not embed credentials: %q", id)
		}
	}
	if !identityCharsetRE.MatchString(stripLocalScheme(id)) {
		return fmt.Errorf("identity contains disallowed characters: %q", id)
	}
	return nil
}

// stripLocalScheme allows local://<abs path> identities through the charset
// check by ignoring the scheme prefix and validating the remainder.
func stripLocalScheme(id string) string {
	const prefix = "local://"
	if strings.HasPrefix(id, prefix) {
		return strings.ReplaceAll(id[len(prefix):], "/", "")
	}
	return id
}

// ValidateAliasInfo enforces wire-level rules on alias metadata
// supplied by remote clients. Unlike ValidateIdentity (which gates a
// project's canonical identity on a strict charset), aliases of kind
// "local" carry workspace paths and must be allowed to contain
// spaces and other characters real filesystems use.
//
//   - kind: "git" or "local".
//   - root_path: non-empty (an alias with no anchor is meaningless
//     and would block path-anchored operations later).
//   - identity (kind=git): apply ValidateIdentity, since a git alias
//     is a normalized remote URL and obeys the same rules as project
//     identity.
//   - identity (kind=local): must start with "local://" and have a
//     non-empty path component. No charset check — the path can
//     contain anything the filesystem accepts.
func ValidateAliasInfo(info AliasInfo) error {
	if info.Kind != "git" && info.Kind != "local" {
		return fmt.Errorf("alias.kind must be \"git\" or \"local\", got %q", info.Kind)
	}
	if strings.TrimSpace(info.RootPath) == "" {
		return fmt.Errorf("alias.root_path must be non-empty")
	}
	if info.Identity == "" {
		return fmt.Errorf("alias.identity must be non-empty")
	}
	switch info.Kind {
	case "git":
		if err := ValidateIdentity(info.Identity); err != nil {
			return fmt.Errorf("alias.identity: %w", err)
		}
	case "local":
		const prefix = "local://"
		if !strings.HasPrefix(info.Identity, prefix) || info.Identity == prefix {
			return fmt.Errorf("alias.identity for kind=local must be %s<path>", prefix)
		}
	}
	return nil
}
