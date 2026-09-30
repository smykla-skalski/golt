package processors

import (
	"cmp"
	"slices"

	"github.com/golangci/golangci-lint/v2/pkg/result"
)

var _ Processor = (*StableOrder)(nil)

// StableOrder sorts issues by position, linter and text before the processors
// that keep only the first N of a group (max-same-issues and the per-linter
// limits). Issues arrive in an order that depends on package scheduling, so
// without it the kept subset changes from run to run.
type StableOrder struct{}

func NewStableOrder() *StableOrder {
	return &StableOrder{}
}

func (*StableOrder) Name() string {
	return "stable_order"
}

func (*StableOrder) Process(issues []*result.Issue) ([]*result.Issue, error) {
	slices.SortStableFunc(issues, func(a, b *result.Issue) int {
		return cmp.Or(
			cmp.Compare(a.Pos.Filename, b.Pos.Filename),
			cmp.Compare(a.Pos.Line, b.Pos.Line),
			cmp.Compare(a.Pos.Column, b.Pos.Column),
			cmp.Compare(a.FromLinter, b.FromLinter),
			cmp.Compare(a.Text, b.Text),
		)
	})

	return issues, nil
}

func (*StableOrder) Finish() {}
