package prsource

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrPageCap is wrapped by adapters when a listing hits the page cap before
// reaching the cursor; ingest-pr adds the remedy naming the cursor file.
var ErrPageCap = errors.New("too many PRs since the cursor (page cap reached)")

// ChildEnvDrop lists the variables removed from the environment of every
// gh/glab child (ingest-pr and the doctor check alike): credentials, host
// redirects and debug switches. The CLIs must use their own keyring login.
var ChildEnvDrop = []string{
	"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_PERSONAL_ACCESS_TOKEN",
	"GITLAB_TOKEN", "GITLAB_ACCESS_TOKEN", "OAUTH_TOKEN", "CI_JOB_TOKEN",
	"GLAB_ENABLE_CI_AUTOLOGIN", "GITLAB_URI", "GITLAB_HOST", "GH_HOST",
	"GLAB_DEBUG", "GH_DEBUG",
}

var (
	segmentRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	hostRE    = regexp.MustCompile(`^[a-z0-9.-]+$`)
)

// ValidAPIPath checks a remote path before it reaches gh/glab: every segment
// must match ^[A-Za-z0-9_.-]+$ and not be "." or ".." (so no braces, colons,
// "?", "#" or "%", which gh and glab expand or interpret). GitHub needs
// exactly two segments, GitLab at least two.
func ValidAPIPath(p Provider, path string) error {
	segs := strings.Split(path, "/")
	for _, s := range segs {
		if s == "." || s == ".." || !segmentRE.MatchString(s) {
			return fmt.Errorf("invalid path segment %q", s)
		}
	}
	switch p {
	case ProviderGitHub:
		if len(segs) != 2 {
			return fmt.Errorf("a GitHub path needs exactly 2 segments, got %d", len(segs))
		}
	case ProviderGitLab:
		if len(segs) < 2 {
			return fmt.Errorf("a GitLab path needs at least 2 segments, got %d", len(segs))
		}
	}
	return nil
}

// ValidHost reports whether host is safe to pass as --hostname=<host>.
func ValidHost(host string) error {
	if !hostRE.MatchString(host) || strings.HasPrefix(host, "-") {
		return fmt.Errorf("invalid host %q", host)
	}
	return nil
}

// IsGitHubBot reports whether a GitHub user is a bot.
func IsGitHubBot(login, userType string) bool {
	return userType == "Bot" || strings.HasSuffix(strings.ToLower(login), "[bot]")
}

var gitlabBotRE = regexp.MustCompile(`^(project|group)_\d+_bot`)

// GitHubTrusted reports whether an author_association marks a trusted author
// (repository owner, organisation member or collaborator).
func GitHubTrusted(association string) bool {
	return association == "OWNER" || association == "MEMBER" || association == "COLLABORATOR"
}

// IsGitLabBot reports whether a GitLab username is a bot.
func IsGitLabBot(username string) bool {
	u := strings.ToLower(username)
	return gitlabBotRE.MatchString(u) || strings.HasSuffix(u, "-bot") || strings.HasSuffix(u, "[bot]")
}
