package processors

import (
	"cmp"

	"github.com/golangci/golangci-lint/v2/pkg/result"
)

const uniqByLineLimit = 1

var _ Processor = (*UniqByLine)(nil)

// UniqByLine filters reports to keep only one report by line of code.
//
// Linters finish in a nondeterministic order, so the kept report is the one
// that sorts first by column, linter and text rather than the first received;
// the same issues then always produce the same output.
type UniqByLine struct {
	fileLineCounter fileLineCounter
	enabled         bool
}

func NewUniqByLine(enable bool) *UniqByLine {
	return &UniqByLine{
		fileLineCounter: fileLineCounter{},
		enabled:         enable,
	}
}

func (*UniqByLine) Name() string {
	return "uniq_by_line"
}

func (p *UniqByLine) Process(issues []*result.Issue) ([]*result.Issue, error) {
	if !p.enabled {
		return issues, nil
	}

	kept := map[fileLine]*result.Issue{}
	for _, issue := range issues {
		if p.fileLineCounter.GetCount(issue) >= uniqByLineLimit {
			continue
		}

		key := fileLineOf(issue)
		if current, ok := kept[key]; !ok || compareUniqCandidates(issue, current) < 0 {
			kept[key] = issue
		}
	}

	for _, issue := range kept {
		p.fileLineCounter.Increment(issue)
	}

	return filterIssuesUnsafe(issues, func(issue *result.Issue) bool {
		key := fileLineOf(issue)
		if kept[key] != issue {
			return false
		}

		delete(kept, key)

		return true
	}), nil
}

func (*UniqByLine) Finish() {}

type fileLine struct {
	file string
	line int
}

func fileLineOf(issue *result.Issue) fileLine {
	return fileLine{file: issue.FilePath(), line: issue.Line()}
}

func compareUniqCandidates(a, b *result.Issue) int {
	return cmp.Or(
		cmp.Compare(a.Column(), b.Column()),
		cmp.Compare(a.FromLinter, b.FromLinter),
		cmp.Compare(a.Text, b.Text),
	)
}

type fileLineCounter map[string]map[int]int

func (f fileLineCounter) GetCount(issue *result.Issue) int {
	return f.getCounter(issue)[issue.Line()]
}

func (f fileLineCounter) Increment(issue *result.Issue) {
	f.getCounter(issue)[issue.Line()]++
}

func (f fileLineCounter) getCounter(issue *result.Issue) map[int]int {
	lc := f[issue.FilePath()]

	if lc == nil {
		lc = map[int]int{}
		f[issue.FilePath()] = lc
	}

	return lc
}
