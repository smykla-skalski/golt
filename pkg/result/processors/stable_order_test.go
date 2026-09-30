package processors

import (
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/golangci/golangci-lint/v2/pkg/config"
	"github.com/golangci/golangci-lint/v2/pkg/logutils"
	"github.com/golangci/golangci-lint/v2/pkg/result"
)

func TestStableOrder_capsKeepSameIssues(t *testing.T) {
	issue := func(file string) *result.Issue {
		return &result.Issue{FromLinter: "gofumpt", Text: "File is not properly formatted", Pos: token.Position{Filename: file, Line: 1}}
	}

	kept := func(order ...string) []string {
		var issues []*result.Issue
		for _, f := range order {
			issues = append(issues, issue(f))
		}

		sorted, err := NewStableOrder().Process(issues)
		require.NoError(t, err)

		limited, err := NewMaxSameIssues(2, logutils.NewStderrLog(logutils.DebugKeyEmpty), &config.Config{}).Process(sorted)
		require.NoError(t, err)

		var files []string
		for _, i := range limited {
			files = append(files, i.Pos.Filename)
		}

		return files
	}

	assert.Equal(t, []string{"a.go", "b.go"}, kept("c.go", "a.go", "b.go"))
	assert.Equal(t, kept("c.go", "a.go", "b.go"), kept("b.go", "c.go", "a.go"))
}
