package goanalysis

import (
	"os"

	"golang.org/x/tools/go/packages"
)

// EnvDepsFacts selects which dependencies facts are computed for.
// "project" limits them to packages of the main module: other dependencies
// are loaded from export data and contribute no facts, which is much faster
// on a cold cache but misses findings that need them (e.g. SA1019 on
// deprecated dependency APIs, printf wrappers defined in dependencies).
const EnvDepsFacts = "GOLT_DEPS_FACTS"

const depsFactsProject = "project"

func projectFactsOnly() bool {
	return os.Getenv(EnvDepsFacts) == depsFactsProject
}

func isMainModulePackage(pkg *packages.Package) bool {
	return pkg.Module != nil && pkg.Module.Main
}
