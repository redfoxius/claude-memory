package prsource

import (
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	testCases := []struct {
		name     string
		remote   string
		wantProv Provider
		wantOrg  string
		wantProj string
		wantName string
	}{
		{
			name:     "azure ssh v3 form",
			remote:   "git@ssh.dev.azure.com:v3/acme/Marketplace/billing-service",
			wantProv: ProviderAzureDevOps,
			wantOrg:  "acme",
			wantProj: "Marketplace",
			wantName: "billing-service",
		},
		{
			name:     "azure https dev.azure.com form",
			remote:   "https://dev.azure.com/acme/Marketplace/_git/billing-service",
			wantProv: ProviderAzureDevOps,
			wantOrg:  "acme",
			wantProj: "Marketplace",
			wantName: "billing-service",
		},
		{
			name:     "azure https visualstudio.com form",
			remote:   "https://acme.visualstudio.com/Marketplace/_git/billing-service",
			wantProv: ProviderAzureDevOps,
			wantOrg:  "acme",
			wantProj: "Marketplace",
			wantName: "billing-service",
		},
		{
			name:     "github ssh form",
			remote:   "git@github.com:example-user/claude-memory.git",
			wantProv: ProviderGitHub,
			wantOrg:  "example-user",
			wantName: "claude-memory",
		},
		{
			name:     "github https form",
			remote:   "https://github.com/example-user/claude-memory.git",
			wantProv: ProviderGitHub,
			wantOrg:  "example-user",
			wantName: "claude-memory",
		},
		{
			name:     "gitlab ssh form",
			remote:   "git@gitlab.com:some-group/some-repo.git",
			wantProv: ProviderGitLab,
			wantOrg:  "some-group",
			wantName: "some-repo",
		},
		{
			name:     "self-hosted gitlab is not auto-detected",
			remote:   "https://gitlab.example.com/some-group/some-repo.git",
			wantProv: ProviderUnknown,
		},
		{
			name:     "github ssh on 443",
			remote:   "ssh://git@ssh.github.com:443/example-user/claude-memory.git",
			wantProv: ProviderGitHub,
			wantOrg:  "example-user",
			wantName: "claude-memory",
		},
		{
			name:     "github www https with port and trailing slash",
			remote:   "https://www.github.com:443/example-user/claude-memory/",
			wantProv: ProviderGitHub,
			wantOrg:  "example-user",
			wantName: "claude-memory",
		},
		{
			name:     "gitlab.com nested groups",
			remote:   "git@gitlab.com:group/sub/project.git",
			wantProv: ProviderGitLab,
			wantOrg:  "group",
			wantName: "sub",
		},
		{
			name:     "unknown host",
			remote:   "git@bitbucket.org:owner/repo.git",
			wantProv: ProviderUnknown,
		},
		{
			name:     "empty remote",
			remote:   "",
			wantProv: ProviderUnknown,
		},
		{
			name:     "unparseable remote",
			remote:   "not a url at all",
			wantProv: ProviderUnknown,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			provider, ref, err := Detect(tc.remote)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if provider != tc.wantProv {
				t.Errorf("provider: got %v, want %v", provider, tc.wantProv)
			}
			if tc.wantProv == ProviderUnknown {
				return
			}
			if ref.Org != tc.wantOrg {
				t.Errorf("org: got %q, want %q", ref.Org, tc.wantOrg)
			}
			if ref.Project != tc.wantProj {
				t.Errorf("project: got %q, want %q", ref.Project, tc.wantProj)
			}
			if ref.Name != tc.wantName {
				t.Errorf("name: got %q, want %q", ref.Name, tc.wantName)
			}
		})
	}
}

func TestDetectFillsHostPathAndRedactsRemote(t *testing.T) {
	p, ref, _ := Detect("https://user:s3cret@GitLab.com:443/group/sub/project.git")
	if p != ProviderGitLab || ref.Host != "gitlab.com" || ref.Path != "group/sub/project" {
		t.Fatalf("got %v host=%q path=%q", p, ref.Host, ref.Path)
	}
	if strings.Contains(ref.Remote, "s3cret") || strings.Contains(ref.Remote, "user") {
		t.Errorf("Remote leaks userinfo: %q", ref.Remote)
	}
}

func TestRedactRemote(t *testing.T) {
	for in, want := range map[string]string{
		"https://user:tok@github.com/a/b.git":     "https://github.com/a/b.git",
		"https://tok@github.com/a/b":              "https://github.com/a/b",
		"git@github.com:a/b.git":                  "github.com:a/b.git",
		"https://github.com/a/b":                  "https://github.com/a/b",
		"https://u:t@github.com/a/b?token=x#frag": "https://github.com/a/b",
		"evil.com:x@github.com:a/b":               "evil.com:x@github.com:a/b",
	} {
		if got := RedactRemote(in); got != want {
			t.Errorf("RedactRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseRemoteOverrideHost(t *testing.T) {
	h, p, ok := ParseRemote("git@git.example.org:team/sub/proj.git")
	if !ok || h != "git.example.org" || p != "team/sub/proj" {
		t.Errorf("got %q %q %v", h, p, ok)
	}
}

func TestValidAPIPath(t *testing.T) {
	bad := []string{"{owner}/{repo}", "a/:id", "a/..", "./b", "a/b%2Fc", "a/b?x", "a/b#c", "a//b", "a/ b", "a/b:c", ""}
	for _, p := range bad {
		if ValidAPIPath(ProviderGitLab, p) == nil {
			t.Errorf("GitLab path %q must be rejected", p)
		}
	}
	if ValidAPIPath(ProviderGitHub, "a/b/c") == nil || ValidAPIPath(ProviderGitHub, "a") == nil {
		t.Error("GitHub needs exactly 2 segments")
	}
	if ValidAPIPath(ProviderGitLab, "a") == nil {
		t.Error("GitLab needs >= 2 segments")
	}
	for _, p := range []string{"owner/repo", "my.org/re-po_1"} {
		if err := ValidAPIPath(ProviderGitHub, p); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
	if err := ValidAPIPath(ProviderGitLab, "g/s.s/p"); err != nil {
		t.Error(err)
	}
}

func TestValidHost(t *testing.T) {
	for h, ok := range map[string]bool{"gitlab.com": true, "git.example.org": true, "evil.com --x": false, "-x": false, "a/b": false, "": false, "A.com": false} {
		if (ValidHost(h) == nil) != ok {
			t.Errorf("ValidHost(%q) ok=%v, want %v", h, !ok, ok)
		}
	}
}

func TestBots(t *testing.T) {
	if !IsGitHubBot("dependabot[bot]", "Bot") || !IsGitHubBot("x[bot]", "User") || !IsGitHubBot("y", "Bot") || IsGitHubBot("alice", "User") {
		t.Error("github bot table")
	}
	for u, want := range map[string]bool{"project_12_bot_abc": true, "group_3_bot": true, "renovate-bot": true, "x[bot]": true, "alice": false, "robotics": false} {
		if IsGitLabBot(u) != want {
			t.Errorf("IsGitLabBot(%q) = %v", u, !want)
		}
	}
}

func TestSCPUserSplitFollowsGit(t *testing.T) {
	h, p, ok := ParseRemote("evil.com:x@github.com:a/b")
	if !ok || h != "evil.com" {
		t.Errorf("host = %q (%v), want evil.com", h, ok)
	}
	_ = p
	if prov, _, _ := Detect("evil.com:x@github.com:a/b"); prov != ProviderUnknown {
		t.Errorf("provider = %v, want unknown", prov)
	}
}
