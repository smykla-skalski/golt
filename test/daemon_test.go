//go:build unix

package test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/golangci/golangci-lint/v2/test/testshared"
)

type daemonRun struct {
	output   string
	exitCode int
}

// TestDaemonMatchesDirectRuns checks that runs served by GOLT_DAEMON=1 print the
// same output and exit with the same code as direct runs, across edits.
func TestDaemonMatchesDirectRuns(t *testing.T) {
	binPath := testshared.InstallGolangciLint(t)

	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod": "module example.com/d\n\ngo 1.22\n",
		"p.go":   "package p\n\nimport \"os\"\n\nfunc F() { os.Remove(\"x\") }\n",
	})

	socketRoot := shortTempDir(t)
	cacheDir := t.TempDir()

	run := func(daemon bool, args ...string) daemonRun {
		t.Helper()

		environ := []string{"GOFLAGS=", "TMPDIR=" + socketRoot, "GOLT_DAEMON_IDLE=10s"}
		if daemon {
			environ = append(environ, "GOLT_DAEMON=1")
		}

		cmd := testshared.NewRunnerBuilder(t).
			WithBinPath(binPath).
			WithNoConfig().
			WithEnviron(environ...).
			WithArgs(append([]string{"--default=none", "-Eerrcheck,unused"}, args...)...).
			Runner().
			Command()
		cmd.Dir = dir
		cmd.Env = append(cmd.Env, "GOLANGCI_LINT_CACHE="+cacheDir)

		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out

		err := cmd.Run()

		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			t.Fatalf("run failed: %v\n%s", err, out.String())
		}

		return daemonRun{output: out.String(), exitCode: cmd.ProcessState.ExitCode()}
	}

	compare := func(desc string, args ...string) {
		t.Helper()

		direct := run(false, args...)
		assert.Equal(t, direct, run(true, args...), desc+": first daemon run")
		assert.Equal(t, direct, run(true, args...), desc+": repeated daemon run")
	}

	compare("issues", "./...")

	sockets, err := filepath.Glob(filepath.Join(socketRoot, "golt-*", "*.sock"))
	require.NoError(t, err)
	assert.NotEmpty(t, sockets, "daemon socket")

	past := time.Now().Add(-time.Minute)
	writeFiles(t, dir, map[string]string{
		"p.go": "package p\n\nimport \"os\"\n\nfunc F() { _ = os.Remove(\"x\") }\n\nfunc unused() {}\n",
	})
	require.NoError(t, os.Chtimes(filepath.Join(dir, "p.go"), past, past))

	compare("body edit", "./...")

	compare("config error", "-c", "missing.yml", "./...")
}

// shortTempDir returns a directory under /tmp: unix socket paths are limited to
// about 104 bytes, which t.TempDir can exceed.
func shortTempDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("/tmp", "golt")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return dir
}
