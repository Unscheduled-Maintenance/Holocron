// Package gitimport reads commit history from local Git repositories by
// invoking the installed git executable, so that it sees exactly what the
// person's own git sees (config, worktrees, hooks-free plumbing output).
package gitimport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// SourceType is the provenance type recorded on imported entries.
const SourceType = "git"

// Commit is one candidate commit.
type Commit struct {
	Hash        string
	AuthorName  string
	AuthorEmail string
	When        time.Time
	Subject     string
	Repo        string // repository top-level directory
	RepoName    string
	URL         string // web URL for the commit, when the remote is recognised
}

// Short returns the abbreviated hash.
func (c Commit) Short() string {
	if len(c.Hash) > 8 {
		return c.Hash[:8]
	}
	return c.Hash
}

// ErrNoGit is returned when git is not installed.
var ErrNoGit = errors.New("git is not installed or not on PATH")

func git(ctx context.Context, dir string, args ...string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", ErrNoGit
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return out.String(), nil
}

// TopLevel returns the repository root containing dir.
func TopLevel(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		if errors.Is(err, ErrNoGit) {
			return "", err
		}
		return "", fmt.Errorf("%s is not inside a Git repository", dir)
	}
	return filepath.Clean(filepath.FromSlash(strings.TrimSpace(out))), nil
}

// UserEmail returns git's configured user.email for the repository.
func UserEmail(ctx context.Context, repo string) string {
	out, err := git(ctx, repo, "config", "user.email")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Options select commits.
type Options struct {
	Since, Until time.Time
	// Authors are emails (case-insensitive) whose commits are wanted.
	Authors []string
}

// Log returns non-merge commits on local branches authored by one of
// opts.Authors, oldest first.
func Log(ctx context.Context, repo string, opts Options) ([]Commit, error) {
	top, err := TopLevel(ctx, repo)
	if err != nil {
		return nil, err
	}
	args := []string{"log", "--branches", "--no-merges", "--format=%H%x1f%an%x1f%ae%x1f%aI%x1f%s%x1e"}
	if !opts.Since.IsZero() {
		args = append(args, "--since="+opts.Since.Format(time.RFC3339))
	}
	if !opts.Until.IsZero() {
		args = append(args, "--until="+opts.Until.Format(time.RFC3339))
	}
	out, err := git(ctx, top, args...)
	if err != nil {
		if strings.Contains(err.Error(), "does not have any commits") {
			return nil, nil
		}
		return nil, err
	}
	want := map[string]bool{}
	for _, a := range opts.Authors {
		if a = strings.ToLower(strings.TrimSpace(a)); a != "" {
			want[a] = true
		}
	}
	remote := remoteURL(ctx, top)
	var commits []Commit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		f := strings.Split(rec, "\x1f")
		if len(f) < 5 {
			continue
		}
		if len(want) > 0 && !want[strings.ToLower(f[2])] {
			continue
		}
		when, err := time.Parse(time.RFC3339, f[3])
		if err != nil {
			continue
		}
		// --since/--until filter on committer date; filter author date too
		// so rebased commits land in the range they were written.
		if (!opts.Since.IsZero() && when.Before(opts.Since)) || (!opts.Until.IsZero() && !when.Before(opts.Until)) {
			continue
		}
		c := Commit{Hash: f[0], AuthorName: f[1], AuthorEmail: f[2], When: when, Subject: strings.TrimSpace(f[4]),
			Repo: top, RepoName: filepath.Base(top)}
		c.URL = CommitURL(remote, c.Hash)
		commits = append(commits, c)
	}
	sort.SliceStable(commits, func(i, j int) bool { return commits[i].When.Before(commits[j].When) })
	return commits, nil
}

func remoteURL(ctx context.Context, repo string) string {
	out, err := git(ctx, repo, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

var (
	scpLike = regexp.MustCompile(`^(?:[\w.-]+@)?([\w.-]+):(.+?)(?:\.git)?/?$`)
	urlLike = regexp.MustCompile(`^(?:https?|ssh|git)://(?:[^@/]+@)?([\w.-]+)(?::\d+)?/(.+?)(?:\.git)?/?$`)
)

// CommitURL builds a browser URL for a commit on GitHub, GitLab, Bitbucket
// or Azure DevOps-style hosts, or returns "" when the remote is unknown.
func CommitURL(remote, hash string) string {
	if remote == "" || hash == "" {
		return ""
	}
	var host, path string
	if m := urlLike.FindStringSubmatch(remote); m != nil {
		host, path = m[1], m[2]
	} else if m := scpLike.FindStringSubmatch(remote); m != nil && !strings.Contains(remote, "://") {
		host, path = m[1], m[2]
	} else {
		return ""
	}
	switch {
	case strings.Contains(host, "github"):
		return fmt.Sprintf("https://%s/%s/commit/%s", host, path, hash)
	case strings.Contains(host, "gitlab"):
		return fmt.Sprintf("https://%s/%s/-/commit/%s", host, path, hash)
	case strings.Contains(host, "bitbucket"):
		return fmt.Sprintf("https://%s/%s/commits/%s", host, path, hash)
	}
	return ""
}
