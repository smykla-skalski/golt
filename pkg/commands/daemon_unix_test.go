//go:build unix

package commands

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func newDaemonConnPair(t *testing.T) (client, server *net.UnixConn) {
	t.Helper()

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	require.NoError(t, err)

	toConn := func(fd int, name string) *net.UnixConn {
		f := os.NewFile(uintptr(fd), name)
		defer f.Close()

		conn, err := net.FileConn(f)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		return conn.(*net.UnixConn)
	}

	return toConn(fds[0], "client"), toConn(fds[1], "server")
}

func TestDaemonRequestRoundTrip(t *testing.T) {
	client, server := newDaemonConnPair(t)

	dir := t.TempDir()

	var stdio []int
	for _, name := range []string{"in", "out", "err"} {
		f, err := os.Create(filepath.Join(dir, name))
		require.NoError(t, err)
		t.Cleanup(func() { _ = f.Close() })

		stdio = append(stdio, int(f.Fd()))
	}

	want := daemonRequest{
		Fingerprint: "abc",
		Args:        []string{"run", "./..."},
		Dir:         dir,
		Env:         []string{"A=1", "HUGE=" + string(make([]byte, 256<<10))},
	}

	errs := make(chan error, 1)
	go func() { errs <- writeDaemonRequest(client, &want, stdio) }()

	got, fds, err := readDaemonRequest(server)
	require.NoError(t, err)
	require.NoError(t, <-errs)
	assert.Equal(t, want, *got)
	require.Len(t, fds, daemonStdioCount)

	out := os.NewFile(uintptr(fds[1]), "out")
	_, err = out.WriteString("hello")
	require.NoError(t, err)
	require.NoError(t, out.Close())
	_ = unix.Close(fds[0])
	_ = unix.Close(fds[2])

	data, err := os.ReadFile(filepath.Join(dir, "out"))
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
}

func TestReadDaemonRequestWithoutDescriptors(t *testing.T) {
	client, server := newDaemonConnPair(t)

	_, err := client.Write([]byte{0, 0, 0, 2, '{', '}'})
	require.NoError(t, err)

	_, _, err = readDaemonRequest(server)
	assert.Error(t, err)
}

func TestDaemonConfigCandidates(t *testing.T) {
	paths := daemonConfigCandidates("/a/b", []string{"run", "-c", "cfg.yml", "--config=/etc/x.yml"})

	assert.Contains(t, paths, "/a/b/cfg.yml")
	assert.Contains(t, paths, "/etc/x.yml")
	assert.Contains(t, paths, "/a/b/.golangci.yml")
	assert.Contains(t, paths, "/a/.golangci.toml")
	assert.Contains(t, paths, "/.golangci.json")
}

func TestEnsureDaemonDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")

	require.NoError(t, ensureDaemonDir(dir))
	require.NoError(t, ensureDaemonDir(dir))

	require.NoError(t, os.Chmod(dir, 0o755))
	require.Error(t, ensureDaemonDir(dir))

	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(t.TempDir(), link))
	assert.Error(t, ensureDaemonDir(link))
}

func TestIsRunCommand(t *testing.T) {
	assert.True(t, isRunCommand(BuildInfo{}, []string{"run", "./..."}))
	assert.True(t, isRunCommand(BuildInfo{}, []string{"--color=never", "run", "-v"}))
	assert.True(t, isRunCommand(BuildInfo{}, []string{"-v", "run"}))
	assert.False(t, isRunCommand(BuildInfo{}, []string{"fmt"}))
	assert.False(t, isRunCommand(BuildInfo{}, []string{"--color=never", "linters"}))
	assert.False(t, isRunCommand(BuildInfo{}, nil))
}

func TestIsDaemonIdentityEnv(t *testing.T) {
	for _, key := range []string{"GOFLAGS", "GOLANGCI_LINT_CACHE", "GOLT_GC", "GOGC", "CGO_ENABLED", "GL_DEBUG", "LOG_LEVEL", "PATH", "HOME"} {
		assert.True(t, isDaemonIdentityEnv(key), key)
	}

	for _, key := range []string{"PWD", "OLDPWD", "SHLVL", "_", "TERM_SESSION_ID", "BENCHMARK_MARKER"} {
		assert.False(t, isDaemonIdentityEnv(key), key)
	}
}
