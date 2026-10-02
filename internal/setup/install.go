package setup

// InstallSteps is the step registry of `claude-memory install` (AC-7), in
// execution order. This is slice 2a: platform, binary, prereqs, topology,
// envfile, database, migrate, ollama, namespaces. The Claude integration and
// jobs steps (hooks.scripts, hooks.settings, mcp, skills, claude-md, jobs)
// arrive in slice 2b together with the WritePorts they need (ClaudeCLI,
// Jobs): in 2a those ports are nil, so no 2a step may dereference them and
// the registry must not contain an mcp or jobs step (registry test).
//
// version is the running binary's version; red is the run's Redactor, which
// the steps that handle passwords or error text from the network need.
//
// WI-S2-14b hook point: the final doctor step (AC-62) is registered here
// after namespaces. It is deliberately not part of 2a.
func InstallSteps(version string, red *Redactor) []Step {
	return []Step{
		PlatformStep{},
		BinaryStep{Version: version},
		PrereqsStep{},
		TopologyStep{},
		EnvFileStep{Version: version},
		DatabaseStep{Version: version, Redactor: red},
		MigrateStep{Redactor: red},
		OllamaStep{Redactor: red},
		NamespacesStep{Version: version},
	}
}
