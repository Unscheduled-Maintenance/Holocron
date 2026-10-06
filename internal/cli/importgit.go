package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Unscheduled-Maintenance/Holocron/internal/app"
	"github.com/Unscheduled-Maintenance/Holocron/internal/gitimport"
	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
	"github.com/Unscheduled-Maintenance/Holocron/internal/tui"
)

func newImportCmd(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import records from other sources",
	}
	cmd.AddCommand(newImportGitCmd(e), newImportJSONCmd(e))
	return cmd
}

type gitImportFlags struct {
	repos       []string
	allProjects bool
	since       string
	authors     []string
	project     string
	yes         bool
	dryRun      bool
	asJSON      bool
}

type gitCandidate struct {
	commit  gitimport.Commit
	project string
}

func newImportGitCmd(e *env) *cobra.Command {
	f := &gitImportFlags{}
	cmd := &cobra.Command{
		Use:   "git",
		Short: "Import your own commits from local Git repositories",
		Long: `Find commits you authored in local Git repositories and import the ones you
choose as journal entries.

Your commits are identified by git's user.email in each repository, plus any
git.author_emails in the Holocron config and --author flags. Commits already
imported are skipped, so running the import repeatedly never duplicates
anything. Each entry keeps the commit hash, a link when the remote is on
GitHub, GitLab or Bitbucket, and the import time.

At a terminal you pick commits from a checklist. Without a terminal, the
candidates are listed and nothing is imported unless --yes is given.

Entries get the project whose repository path contains the repository
(holocron project edit NAME --path DIR), or --project.`,
		Example: `  holocron import git --since 7d
  holocron import git --repo ~/src/infra --repo ~/src/site --since last-week
  holocron import git --projects --since 30d --dry-run
  holocron import git --since yesterday --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := e.open(ctx)
			if err != nil {
				return err
			}
			rng, err := a.Clock().Parse(f.since)
			if err != nil {
				return usageError{err}
			}
			repos, err := gitRepos(cmd, a, f)
			if err != nil {
				return err
			}
			var cands []gitCandidate
			seen := map[string]bool{}
			for _, repo := range repos {
				top, err := gitimport.TopLevel(ctx, repo)
				if err != nil {
					return err
				}
				authors := append(append([]string{}, f.authors...), a.Config.Git.AuthorEmails...)
				if email := gitimport.UserEmail(ctx, top); email != "" {
					authors = append(authors, email)
				}
				if len(authors) == 0 {
					return fmt.Errorf("cannot tell which commits are yours in %s: set git's user.email, git.author_emails in the config, or pass --author", top)
				}
				commits, err := gitimport.Log(ctx, top, gitimport.Options{Since: rng.Start, Until: rng.End, Authors: authors})
				if err != nil {
					return err
				}
				project := f.project
				if project == "" {
					if p, ok, err := a.Store.ProjectForPath(ctx, top); err != nil {
						return err
					} else if ok {
						project = p.Name
					}
				}
				for _, c := range commits {
					if !seen[c.Hash] {
						seen[c.Hash] = true
						cands = append(cands, gitCandidate{commit: c, project: project})
					}
				}
			}
			hashes := make([]string, len(cands))
			for i, c := range cands {
				hashes[i] = c.commit.Hash
			}
			done, err := a.Store.ImportedIDs(ctx, gitimport.SourceType, hashes)
			if err != nil {
				return err
			}
			fresh := cands[:0]
			for _, c := range cands {
				if _, ok := done[c.commit.Hash]; !ok {
					fresh = append(fresh, c)
				}
			}
			already := len(cands) - len(fresh)
			cands = fresh

			if len(cands) == 0 {
				where := "the repository"
				if len(repos) > 1 {
					where = fmt.Sprintf("%d repositories", len(repos))
				}
				msg := fmt.Sprintf("No new commits of yours in %s (%s)", where, rng.Label)
				if already > 0 {
					msg += fmt.Sprintf("; %d already imported", already)
				}
				e.note("%s.", msg)
				return nil
			}

			selected := cands
			interactive := e.io.InTTY && e.io.OutTTY && !f.yes && !f.dryRun && !f.asJSON
			switch {
			case interactive:
				items := make([]tui.SelectItem, len(cands))
				for i, c := range cands {
					label := c.commit.Subject
					meta := c.commit.When.In(a.Loc).Format("Mon 2 Jan "+a.TimeLayout()) + "  " + c.commit.RepoName + " " + c.commit.Short()
					if c.project != "" {
						meta += "  → " + c.project
					}
					items[i] = tui.SelectItem{Label: label, Detail: meta, Selected: true}
				}
				title := fmt.Sprintf("Import %d commits from %s", len(cands), rng.Label)
				picked, ok, err := tui.Select(ctx, title, items)
				if err != nil {
					return err
				}
				if !ok {
					e.note("Import cancelled; nothing was imported.")
					return nil
				}
				selected = nil
				for _, i := range picked {
					selected = append(selected, cands[i])
				}
			case f.yes && !f.dryRun:
			default:
				if f.asJSON {
					out := make([]map[string]any, len(cands))
					for i, c := range cands {
						out[i] = map[string]any{"hash": c.commit.Hash, "subject": c.commit.Subject, "authored_at": c.commit.When.UTC().Format(time.RFC3339),
							"repository": c.commit.Repo, "project": c.project, "url": c.commit.URL}
					}
					return writeJSON(e.io.Out, out)
				}
				st := e.out()
				for _, c := range cands {
					proj := ""
					if c.project != "" {
						proj = "  " + st.Project(c.project)
					}
					fmt.Fprintf(e.io.Out, "%s  %s  %s%s  %s\n", st.Dim(c.commit.When.In(a.Loc).Format("2006-01-02 "+a.TimeLayout())),
						st.Faint(c.commit.Short()), st.Dim(c.commit.RepoName), proj, c.commit.Subject)
				}
				e.note("%d new %s; nothing imported. Re-run with --yes to import them, or at a terminal to choose.", len(cands), plural(len(cands), "commit", "commits"))
				return nil
			}
			if len(selected) == 0 {
				e.note("No commits selected; nothing was imported.")
				return nil
			}
			now := a.Now()
			items := make([]journal.NewEntry, len(selected))
			for i, c := range selected {
				items[i] = journal.NewEntry{
					Body: c.commit.Subject, OccurredAt: c.commit.When, Type: journal.TypeWork,
					Project: c.project, CreateProject: true,
					Source: &journal.Source{Type: gitimport.SourceType, ID: c.commit.Hash, URL: c.commit.URL, ImportedAt: now},
				}
			}
			created, skipped, err := a.Store.ImportEntries(ctx, items)
			if err != nil {
				return err
			}
			if f.asJSON {
				return writeJSON(e.io.Out, entriesJSON(created))
			}
			st := e.out()
			fmt.Fprintf(e.io.Out, "%s %d %s", st.Success("Imported"), len(created), plural(len(created), "commit", "commits"))
			if skipped+already > 0 {
				fmt.Fprintf(e.io.Out, " %s", st.Dim(fmt.Sprintf("(%d already in the archive)", skipped+already)))
			}
			fmt.Fprintln(e.io.Out)
			return nil
		},
	}
	fs := cmd.Flags()
	fs.StringSliceVar(&f.repos, "repo", nil, "repository directory (repeatable; default: the current directory)")
	fs.BoolVar(&f.allProjects, "projects", false, "scan every repository path registered on a project")
	fs.StringVar(&f.since, "since", "7d", "how far back to look: 7d, yesterday, last-week, this-month, YYYY-MM-DD …")
	fs.StringSliceVar(&f.authors, "author", nil, "additional author email to treat as yours (repeatable)")
	fs.StringVarP(&f.project, "project", "p", "", "assign every imported commit to this project")
	fs.BoolVarP(&f.yes, "yes", "y", false, "import every new commit without asking")
	fs.BoolVar(&f.dryRun, "dry-run", false, "list the new commits but import nothing")
	fs.BoolVar(&f.asJSON, "json", false, "print candidates (or imported entries with --yes) as JSON")
	_ = cmd.RegisterFlagCompletionFunc("project", projectCompletion(e))
	return cmd
}

func gitRepos(cmd *cobra.Command, a *app.App, f *gitImportFlags) ([]string, error) {
	var repos []string
	for _, r := range f.repos {
		repos = append(repos, expandPath(r))
	}
	if f.allProjects {
		ps, err := a.Store.ListProjects(cmd.Context(), false)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			for _, path := range p.Paths {
				if st, err := os.Stat(path); err == nil && st.IsDir() {
					repos = append(repos, path)
				}
			}
		}
		if len(repos) == 0 {
			return nil, usagef("no project has a repository path; add one with `holocron project edit NAME --path DIR`")
		}
	}
	if len(repos) == 0 {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		repos = []string{wd}
	}
	seen := map[string]bool{}
	out := repos[:0]
	for _, r := range repos {
		key := strings.ToLower(filepath.Clean(r))
		if !seen[key] {
			seen[key] = true
			out = append(out, r)
		}
	}
	return out, nil
}
