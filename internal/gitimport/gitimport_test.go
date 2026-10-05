package gitimport

import "testing"

func TestCommitURL(t *testing.T) {
	cases := map[string]string{
		"git@github.com:Unscheduled-Maintenance/Holocron.git":     "https://github.com/Unscheduled-Maintenance/Holocron/commit/abc",
		"https://github.com/Unscheduled-Maintenance/Holocron.git": "https://github.com/Unscheduled-Maintenance/Holocron/commit/abc",
		"https://github.com/Unscheduled-Maintenance/Holocron":     "https://github.com/Unscheduled-Maintenance/Holocron/commit/abc",
		"ssh://git@gitlab.com/group/sub/project.git":              "https://gitlab.com/group/sub/project/-/commit/abc",
		"git@gitlab.example.com:team/app.git":                     "https://gitlab.example.com/team/app/-/commit/abc",
		"https://user@bitbucket.org/team/repo.git":                "https://bitbucket.org/team/repo/commits/abc",
		"https://git.internal.example/some/repo.git":              "",
		"/srv/git/repo.git":                                       "",
		"":                                                        "",
	}
	for remote, want := range cases {
		if got := CommitURL(remote, "abc"); got != want {
			t.Errorf("CommitURL(%q) = %q, want %q", remote, got, want)
		}
	}
}
