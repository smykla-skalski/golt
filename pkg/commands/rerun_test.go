package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/golangci/golangci-lint/v2/pkg/exitcodes"
	"github.com/golangci/golangci-lint/v2/pkg/lint/workerprotocol"
)

const rerunConfig = `version: "2"
linters:
  default: none
  enable:
    - errcheck
    - gocritic
    - govet
    - importas
    - staticcheck
    - unused
  settings:
    importas:
      alias:
        - pkg: strings
          alias: str
`

const rerunClean = `package p

import "strings"

func Upper(s string) string { return strings.ToUpper(s) }
`

const rerunBodyEdit = `package p

import "strings"

func Upper(s string) string { return strings.ToUpper(s) }

func unusedHelper() {}
`

const rerunDirty = `package p

import (
	"os"
	strs "strings"
)

func Upper(s string) string {
	os.Remove(s)
	return strs.ToUpper(s)
}

func unusedHelper() {}
`

// TestRunRepeatedInProcess guards the state a long-lived process keeps between
// runs: an edited file must be re-analyzed, and restoring it must reproduce the
// original report.
func TestRunRepeatedInProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("loads packages with go list")
	}

	dir := t.TempDir()
	writeRerunFile(t, dir, "go.mod", "module example.com/rerun\n\ngo 1.24\n")
	writeRerunFile(t, dir, ".golangci.yml", rerunConfig)

	var generation int

	run := func(content string) ([]string, int) {
		t.Helper()

		generation++
		writeRerunFileAt(t, dir, "p.go", content, time.Now().Add(-time.Hour).Add(time.Duration(generation)*time.Second))

		report := filepath.Join(t.TempDir(), "report.json")

		_, exitCode, err := executeWorkerRun(BuildInfo{}, workerprotocol.RunPayload{
			Args:             []string{"run", "--output.json.path", report, "--show-stats=false", "./..."},
			WorkingDirectory: dir,
		})
		require.NoError(t, err)

		return readRerunIssues(t, report), exitCode
	}

	clean, exitCode := run(rerunClean)
	assert.Empty(t, clean)
	assert.Equal(t, exitcodes.Success, exitCode)

	repeated, _ := run(rerunClean)
	assert.Equal(t, clean, repeated, "repeat")

	bodyEdit, _ := run(rerunBodyEdit)
	assert.Contains(t, bodyEdit, "unused", "body-only edit")

	dirty, exitCode := run(rerunDirty)
	assert.Contains(t, dirty, "errcheck")
	assert.Contains(t, dirty, "unused")
	assert.Contains(t, dirty, "importas")
	assert.Equal(t, exitcodes.IssuesFound, exitCode)

	restored, exitCode := run(rerunClean)
	assert.Equal(t, clean, restored, "restored")
	assert.Equal(t, exitcodes.Success, exitCode)
}

func writeRerunFile(t *testing.T, dir, name, content string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
}

// writeRerunFileAt pins the mtime so each generation is distinct even on
// filesystems with coarse timestamps.
func writeRerunFileAt(t *testing.T, dir, name, content string, modTime time.Time) {
	t.Helper()

	writeRerunFile(t, dir, name, content)
	require.NoError(t, os.Chtimes(filepath.Join(dir, name), modTime, modTime))
}

func readRerunIssues(t *testing.T, path string) []string {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var report struct {
		Issues []struct {
			FromLinter string
			Text       string
		}
	}
	require.NoError(t, json.Unmarshal(data, &report))

	issues := make([]string, 0, 2*len(report.Issues))
	for _, issue := range report.Issues {
		issues = append(issues, issue.FromLinter, issue.Text)
	}

	return issues
}
