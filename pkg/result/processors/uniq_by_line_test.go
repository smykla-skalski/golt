package processors

import (
	"go/token"
	"testing"

	"github.com/golangci/golangci-lint/v2/pkg/result"
)

func newULIssue(file string, line int) *result.Issue {
	return &result.Issue{
		Pos: token.Position{
			Filename: file,
			Line:     line,
		},
	}
}

func TestUniqByLine(t *testing.T) {
	p := NewUniqByLine(true)
	i1 := newULIssue("f1", 1)

	processAssertSame(t, p, i1)
	processAssertEmpty(t, p, i1) // check skipping
	processAssertEmpty(t, p, i1) // check accumulated error

	processAssertSame(t, p, newULIssue("f1", 2)) // another line
	processAssertSame(t, p, newULIssue("f2", 1)) // another file
}

func TestUniqByLineDisabled(t *testing.T) {
	p := NewUniqByLine(false)
	i1 := newULIssue("f1", 1)

	processAssertSame(t, p, i1)
	processAssertSame(t, p, i1) // check the same issue passed twice
}

func TestUniqByLineKeepsSameIssueRegardlessOfOrder(t *testing.T) {
	newIssue := func(linter, text string, column int) *result.Issue {
		return &result.Issue{
			FromLinter: linter,
			Text:       text,
			Pos:        token.Position{Filename: "f1", Line: 3, Column: column},
		}
	}

	g304 := newIssue("gosec", "G304: Potential file inclusion via variable", 12)
	g302 := newIssue("gosec", "G302: Expect file permissions to be 0600 or less", 12)
	later := newIssue("errcheck", "Error return value is not checked", 20)
	other := newULIssue("f2", 1)

	for _, order := range [][]*result.Issue{
		{g304, g302, later, other},
		{later, g302, other, g304},
		{other, later, g304, g302},
	} {
		got, err := NewUniqByLine(true).Process(order)
		if err != nil {
			t.Fatal(err)
		}

		var kept []*result.Issue
		for _, issue := range got {
			if issue.FilePath() == "f1" {
				kept = append(kept, issue)
			}
		}
		if len(kept) != 1 || kept[0] != g302 {
			t.Fatalf("kept %v for order %v, want only the G302 issue", kept, order)
		}
		if len(got) != 2 {
			t.Fatalf("got %d issues, want 2", len(got))
		}
	}
}

func TestUniqByLineDuplicateIssueInOneBatch(t *testing.T) {
	issue := newULIssue("f1", 1)

	got, err := NewUniqByLine(true).Process([]*result.Issue{issue, issue})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d issues, want 1", len(got))
	}
}
