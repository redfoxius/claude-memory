package setup

import "strings"

// Components the hint table covers (AC-18).
const (
	CompGit      = "git"
	CompClaude   = "claude"
	CompPsql     = "psql" // libpq client tools
	CompPostgres = "postgres"
	CompOllama   = "ollama"
	CompAz       = "az"
	CompGh       = "gh"
	CompGlab     = "glab"
)

// hints is the AC-18 table keyed by (package manager, component). The text is
// printed only, never executed (no step runs a package manager). "" is the
// fallback for an unknown or absent package manager.
var hints = map[string]map[string]string{
	"brew": {
		CompGit:      "brew install git",
		CompClaude:   "npm install -g @anthropic-ai/claude-code (or see https://docs.claude.com/en/docs/claude-code/setup)",
		CompPsql:     "brew install libpq && brew link --force libpq",
		CompPostgres: "brew install postgresql@16 pgvector && brew services start postgresql@16",
		CompOllama:   "brew install ollama (or download from https://ollama.com/download)",
		CompAz:       "brew install azure-cli",
		CompGh:       "brew install gh",
		CompGlab:     "brew install glab",
	},
	"apt-get": {
		CompGit:      "sudo apt-get install -y git",
		CompClaude:   "npm install -g @anthropic-ai/claude-code (or see https://docs.claude.com/en/docs/claude-code/setup)",
		CompPsql:     "sudo apt-get install -y postgresql-client",
		CompPostgres: "sudo apt-get install -y postgresql-16 postgresql-16-pgvector",
		CompOllama:   "see https://ollama.com/download/linux",
		CompAz:       "see https://learn.microsoft.com/cli/azure/install-azure-cli-linux",
		CompGh:       "see https://github.com/cli/cli/blob/trunk/docs/install_linux.md",
		CompGlab:     "see https://gitlab.com/gitlab-org/cli#installation",
	},
	"dnf": {
		CompGit:      "sudo dnf install -y git",
		CompClaude:   "npm install -g @anthropic-ai/claude-code (or see https://docs.claude.com/en/docs/claude-code/setup)",
		CompPsql:     "sudo dnf install -y postgresql",
		CompPostgres: "sudo dnf install -y postgresql16-server pgvector_16 (needs the PGDG repository)",
		CompOllama:   "see https://ollama.com/download/linux",
		CompAz:       "see https://learn.microsoft.com/cli/azure/install-azure-cli-linux",
		CompGh:       "see https://github.com/cli/cli/blob/trunk/docs/install_linux.md",
		CompGlab:     "see https://gitlab.com/gitlab-org/cli#installation",
	},
	"pacman": {
		CompGit:      "sudo pacman -S git",
		CompClaude:   "npm install -g @anthropic-ai/claude-code (or see https://docs.claude.com/en/docs/claude-code/setup)",
		CompPsql:     "sudo pacman -S postgresql-libs",
		CompPostgres: "sudo pacman -S postgresql (pgvector from the AUR)",
		CompOllama:   "sudo pacman -S ollama",
		CompAz:       "see https://learn.microsoft.com/cli/azure/install-azure-cli-linux",
		CompGh:       "see https://github.com/cli/cli/blob/trunk/docs/install_linux.md",
		CompGlab:     "see https://gitlab.com/gitlab-org/cli#installation",
	},
	"": {
		CompGit:      "install git from https://git-scm.com/downloads",
		CompClaude:   "see https://docs.claude.com/en/docs/claude-code/setup",
		CompPsql:     "install the PostgreSQL client tools (psql) for your system",
		CompPostgres: "install PostgreSQL 16 and the pgvector extension for your system",
		CompOllama:   "see https://ollama.com/download",
		CompAz:       "see https://learn.microsoft.com/cli/azure/install-azure-cli",
		CompGh:       "see https://cli.github.com",
		CompGlab:     "see https://gitlab.com/gitlab-org/cli#installation",
	},
}

// Hint returns the install hint for component on the platform's preferred
// package manager (the first of PlatformInfo.PackageManagers that the table
// knows), else the generic text. It returns "" for an unknown component.
func Hint(p PlatformInfo, component string) string {
	for _, pm := range p.PackageManagers {
		if t, ok := hints[pm]; ok {
			if h, ok := t[component]; ok {
				return h
			}
		}
	}
	return hints[""][component]
}

// hintList joins "<tool>: <hint>" lines for several components (one remedy
// string for a blocked Detection).
func hintList(p PlatformInfo, components ...string) string {
	parts := make([]string, 0, len(components))
	for _, c := range components {
		parts = append(parts, c+": "+Hint(p, c))
	}
	return strings.Join(parts, "; ")
}
