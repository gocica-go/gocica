// Command lint is the canonical linter for the gocica repository.
//
// It bundles, into a single binary:
//
//   - the analyzer suite that "go vet" runs
//     (golang.org/x/tools/go/analysis/suite/vet),
//   - staticcheck's SA* checks (honnef.co/go/tools/staticcheck),
//   - stylecheck's ST* checks (honnef.co/go/tools/stylecheck),
//   - modernize, the suite "go fix" runs
//     (golang.org/x/tools/go/analysis/passes/modernize).
//
// Diagnostics in generated files ("Code generated ... DO NOT EDIT." — the
// protoc-gen-go and kessoku output under internal/) are dropped: they cannot
// be fixed in this repository, only regenerated.
//
// Analyzers that staticcheck marks as non-default (opt-in, e.g. ST1000
// "at least one file in a package should have a package comment") are
// skipped so that the default behaviour matches upstream staticcheck.
//
// It lives in the repository's tools module rather than in the root one, so
// that staticcheck and its dependencies stay out of the graph of anyone
// importing gocica. The repository's go.work is what still lets the root
// module's `tool` shorthand reach it:
//
//	go tool lint ./... ./tools/...
package main

import (
	"go/ast"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/multichecker"
	"golang.org/x/tools/go/analysis/passes/modernize"
	"golang.org/x/tools/go/analysis/suite/vet"

	"honnef.co/go/tools/analysis/lint"
	"honnef.co/go/tools/staticcheck"
	"honnef.co/go/tools/stylecheck"
)

func main() {
	analyzers := make([]*analysis.Analyzer, 0, len(vet.Suite)+len(modernize.Suite)+len(staticcheck.Analyzers)+len(stylecheck.Analyzers))

	// go vet's analyzers, kept in sync with the toolchain by x/tools.
	analyzers = append(analyzers, vet.Suite...)

	// modernize's analyzers: the suite "go fix" runs, minus the rest of
	// suite/fix, whose buildtag and hostport are already in vet.Suite and
	// would make multichecker reject the duplicate names.
	analyzers = append(analyzers, modernize.Suite...)

	// staticcheck + stylecheck, minus the checks upstream disables by default.
	analyzers = append(analyzers, defaultAnalyzers(staticcheck.Analyzers)...)
	analyzers = append(analyzers, defaultAnalyzers(stylecheck.Analyzers)...)

	for i, a := range analyzers {
		analyzers[i] = skipGenerated(a)
	}

	multichecker.Main(analyzers...)
}

// skipGenerated wraps a so that diagnostics pointing at a generated file are
// discarded. The analysis API has no notion of excluded paths, so the filter
// has to sit on the pass's Report hook.
func skipGenerated(a *analysis.Analyzer) *analysis.Analyzer {
	run := a.Run
	a.Run = func(pass *analysis.Pass) (any, error) {
		generated := make(map[string]bool, len(pass.Files))
		for _, f := range pass.Files {
			if tf := pass.Fset.File(f.FileStart); tf != nil {
				generated[tf.Name()] = ast.IsGenerated(f)
			}
		}

		report := pass.Report
		pass.Report = func(d analysis.Diagnostic) {
			// d.Pos is NoPos for package-level diagnostics, which no
			// generated file can be blamed for.
			if tf := pass.Fset.File(d.Pos); tf != nil && generated[tf.Name()] {
				return
			}
			report(d)
		}

		return run(pass)
	}
	return a
}

// defaultAnalyzers unwraps honnef.co analyzers, dropping the ones that are
// not enabled in staticcheck's default configuration.
func defaultAnalyzers(as []*lint.Analyzer) []*analysis.Analyzer {
	out := make([]*analysis.Analyzer, 0, len(as))
	for _, a := range as {
		if a.Doc != nil && a.Doc.NonDefault {
			continue
		}
		out = append(out, a.Analyzer)
	}
	return out
}
