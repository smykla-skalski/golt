package processors

import (
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/golangci/golangci-lint/v2/pkg/logutils"
	"github.com/golangci/golangci-lint/v2/pkg/result"
)

func TestFilenameUnadjusterMapsOnlyLineDirectiveFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		return path
	}

	plain := write("plain.go", "package p\n\nfunc F() {}\n")
	slashLine := write("slash.go", "//line template.qtpl:10\npackage p\n\nfunc G() {}\n")
	blockLine := write("block.go", "/*line other.tmpl:5*/ package p\n\nfunc H() {}\n")

	pkg := &packages.Package{CompiledGoFiles: []string{plain, slashLine, blockLine}}
	unadjuster := NewFilenameUnadjuster([]*packages.Package{pkg}, logutils.NewStderrLog(logutils.DebugKeyEmpty))

	qtpl := filepath.Join(dir, "template.qtpl")

	assert.Len(t, unadjuster.m, 2)
	assert.Contains(t, unadjuster.m, qtpl)
	assert.Contains(t, unadjuster.m, filepath.Join(dir, "other.tmpl"))

	issues, err := unadjuster.Process([]*result.Issue{
		{Pos: token.Position{Filename: qtpl, Line: 12, Offset: len("//line template.qtpl:10\npackage p\n\n")}},
		{Pos: token.Position{Filename: plain, Line: 3}},
	})
	require.NoError(t, err)

	assert.Equal(t, slashLine, issues[0].Pos.Filename)
	assert.Equal(t, 4, issues[0].Pos.Line)
	assert.Equal(t, plain, issues[1].Pos.Filename)
}

func TestHasLineDirective(t *testing.T) {
	tests := []struct {
		src  string
		want bool
	}{
		{src: "package p\n", want: false},
		{src: "// a line of text\npackage p\n", want: false},
		{src: "//line a.go:1\npackage p\n", want: true},
		{src: "package p /*line a.go:1*/\n", want: true},
	}
	for _, test := range tests {
		assert.Equal(t, test.want, hasLineDirective([]byte(test.src)), test.src)
	}
}
