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
//   - GitHub: github.com, www.github.com, ssh.github.com.
//   - GitLab: gitlab.com only. A self-hosted GitLab is never auto-detected
//     (credentials must not flow to a host a remote merely resembles); it is
//     reached only through an explicit provider override (see ParseRemote).
//
// RepoRef.Remote holds the remote with any userinfo removed, so it is safe
// to log. Any other host, or a remote that can't be parsed at all, resolves to
// ProviderUnknown with no error — an unrecognized provider is a normal,
// expected outcome the caller skips (AC-58), not a failure. The error
// return exists for a genuinely malformed/empty remote string; it is
// never a signal to retry.
func Detect(remoteURL string) (Provider, RepoRef, error) {
	remote := strings.TrimSpace(remoteURL)
	if remote == "" {
		return ProviderUnknown, RepoRef{Remote: remote}, nil
	}

	safe := RedactRemote(remote)
	hostLower, path, ok := ParseRemote(remote)
	if !ok {
		return ProviderUnknown, RepoRef{Remote: safe}, nil
	}

	var ref RepoRef
	var provider Provider
	switch {
	case isAzureHost(hostLower):
		provider, ref = ProviderAzureDevOps, parseAzureRef(hostLower, path, safe)
	case IsGitHubHost(hostLower):
		provider, ref = ProviderGitHub, parseTwoSegmentRef(path, safe)
	case hostLower == "gitlab.com":
		provider, ref = ProviderGitLab, parseTwoSegmentRef(path, safe)
	default:
		return ProviderUnknown, RepoRef{Remote: safe}, nil
	}
	ref.Provider = provider
	ref.RemoteName = ref.Name
	ref.Host = hostLower
	ref.Path = path
	return provider, ref, nil
}

// ParseRemote returns the lower-case host (no port, no userinfo) and the
// path (no leading/trailing "/", no ".git" suffix) of a URL-style or
// SCP-style remote. It is exported for the explicit provider-override path,
// where the host is not one Detect recognises.
func ParseRemote(remote string) (host, path string, ok bool) {
	h, p, ok := splitHostPath(strings.TrimSpace(remote))
	if !ok {
		return "", "", false
	}
	path = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	return strings.ToLower(h), path, true
}

// RedactRemote removes userinfo ("user:token@") from a remote URL so it is
// safe to log or print. SCP-style remotes ("git@host:path") only carry a
// user name, which is dropped as well. An unparseable remote is returned
// with everything up to the last "@" removed.
func RedactRemote(remote string) string {
	remote = strings.TrimSpace(remote)
	if strings.Contains(remote, "://") {
		if u, err := url.Parse(remote); err == nil {
			u.User, u.RawQuery, u.Fragment, u.ForceQuery = nil, "", "", false
			return u.String()
		}
	}
	if cut := strings.IndexAny(remote, "?#"); cut >= 0 {
		remote = remote[:cut]
	}
	if colon := strings.Index(remote, ":"); colon >= 0 {
		if at := strings.Index(remote[:colon], "@"); at >= 0 {
			return remote[at+1:]
		}
		return remote
	}
	if i := strings.LastIndex(remote, "@"); i >= 0 {
		return remote[i+1:]
	}
	return remote
}

// IsGitHubHost reports whether host is one of the GitHub cloud hosts.
func IsGitHubHost(host string) bool {
	return host == "github.com" || host == "www.github.com" || host == "ssh.github.com"
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
		return u.Hostname(), u.Path, true
	}

	// SCP-style, with git's semantics: the user ends at the first "@" that
	// precedes the first ":" ("evil.com:x@github.com:a/b" has host evil.com).
	colon := strings.Index(remote, ":")
	if colon < 0 {
		return "", "", false
	}
	host = remote[:colon]
	if at := strings.Index(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	return host, remote[colon+1:], true
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
