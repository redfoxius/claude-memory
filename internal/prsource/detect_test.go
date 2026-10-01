package prsource

import "testing"

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
			name:     "self-hosted gitlab https form",
			remote:   "https://gitlab.example.com/some-group/some-repo.git",
			wantProv: ProviderGitLab,
			wantOrg:  "some-group",
			wantName: "some-repo",
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
