package prsource

import (
	"net/url"
	"strings"
)

// Detect determines which PR-hosting provider a repo's git origin remote
// points at, and parses out whatever org/project/repo segments the URL
// form carries (AC-58). It understands both URL-style and SCP-style
// ("git@host:path") remotes, over https and ssh, for:
//   - Azure DevOps: dev.azure.com, ssh.dev.azure.com, and *.visualstudio.com
//     (https and the "v3/org/project/repo" ssh form).
//   - GitHub: github.com.
//   - GitLab: gitlab.com or any self-hosted gitlab.* host.
//
// Any other host, or a remote that can't be parsed at all, resolves to
// ProviderUnknown with no error — an unrecognized provider is a normal,
// expected outcome the caller skips (AC-58), not a failure. The error
// return exists for a genuinely malformed/empty remote string; it is
// never a signal to retry.
func Detect(remoteURL string) (Provider, RepoRef, error) {
	remote := strings.TrimSpace(remoteURL)
	if remote == "" {
		return ProviderUnknown, RepoRef{Remote: remote}, nil
	}

	host, path, ok := splitHostPath(remote)
	if !ok {
		return ProviderUnknown, RepoRef{Remote: remote}, nil
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	hostLower := strings.ToLower(host)

	switch {
	case isAzureHost(hostLower):
		return ProviderAzureDevOps, parseAzureRef(hostLower, path, remote), nil
	case hostLower == "github.com":
		return ProviderGitHub, parseTwoSegmentRef(path, remote), nil
	case hostLower == "gitlab.com" || strings.HasPrefix(hostLower, "gitlab."):
		return ProviderGitLab, parseTwoSegmentRef(path, remote), nil
	default:
		return ProviderUnknown, RepoRef{Remote: remote}, nil
	}
}

func isAzureHost(host string) bool {
	return host == "dev.azure.com" ||
		host == "ssh.dev.azure.com" ||
		strings.HasSuffix(host, ".visualstudio.com")
}

// splitHostPath extracts the host and path from either a URL-style remote
// (scheme://host/path) or an SCP-style remote ([user@]host:path).
func splitHostPath(remote string) (host, path string, ok bool) {
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil || u.Host == "" {
			return "", "", false
		}
		return u.Host, u.Path, true
	}

	rest := remote
	if idx := strings.Index(rest, "@"); idx >= 0 {
		rest = rest[idx+1:]
	}
	idx := strings.Index(rest, ":")
	if idx < 0 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
}

// parseAzureRef parses an Azure DevOps path into org/project/repo.
// Supported forms (after stripping a leading "v3" or any "_git" segment):
//   - "{org}/{project}/{repo}"       (ssh v3 form: v3/org/project/repo)
//   - "{org}/{project}/{repo}"       (https dev.azure.com: org/project/_git/repo)
//   - "{project}/{repo}"             (https *.visualstudio.com: project/_git/repo;
//     org is recovered from the subdomain)
func parseAzureRef(host, path, remote string) RepoRef {
	segs := strings.Split(path, "/")
	ref := RepoRef{Provider: ProviderAzureDevOps, Remote: remote}

	cleaned := make([]string, 0, len(segs))
	for _, s := range segs {
		if s == "" || s == "v3" || s == "_git" {
			continue
		}
		cleaned = append(cleaned, s)
	}

	switch len(cleaned) {
	case 3:
		ref.Org, ref.Project, ref.Name = cleaned[0], cleaned[1], cleaned[2]
	case 2:
		ref.Project, ref.Name = cleaned[0], cleaned[1]
		ref.Org = strings.TrimSuffix(host, ".visualstudio.com")
	case 1:
		ref.Name = cleaned[0]
	}
	return ref
}

// parseTwoSegmentRef parses a GitHub/GitLab-style "{owner}/{repo}" path.
func parseTwoSegmentRef(path, remote string) RepoRef {
	segs := strings.Split(path, "/")
	ref := RepoRef{Remote: remote}

	cleaned := make([]string, 0, len(segs))
	for _, s := range segs {
		if s != "" {
			cleaned = append(cleaned, s)
		}
	}

	if len(cleaned) >= 2 {
		ref.Org = cleaned[0]
		ref.Name = cleaned[1]
	} else if len(cleaned) == 1 {
		ref.Name = cleaned[0]
	}
	return ref
}
