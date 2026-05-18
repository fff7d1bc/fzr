package main

// CommonIgnoredDirNames is intentionally centralized because the practical
// opt-in ignore list will grow as fzr is used across more repositories.
var CommonIgnoredDirNames = []string{
	".git",
	".terraform",
	"node_modules",
	"venv",
	".venv",
	"__pycache__",
	".tox",
	".cache",
}

func ignoredDirSet(names []string) map[string]struct{} {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}
