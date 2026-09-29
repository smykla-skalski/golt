//go:build unix

package commands

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fatih/color"
	"github.com/mattn/go-isatty"
	"golang.org/x/sys/unix"

	"github.com/golangci/golangci-lint/v2/internal/processexit"
	"github.com/golangci/golangci-lint/v2/pkg/exitcodes"
	"github.com/golangci/golangci-lint/v2/pkg/lint/workerprotocol"
)

const (
	envDaemon      = "GOLT_DAEMON"
	envDaemonServe = "GOLT_DAEMON_SERVE"
	envDaemonID    = "GOLT_DAEMON_FINGERPRINT"
	envDaemonIdle  = "GOLT_DAEMON_IDLE"

	daemonDefaultIdle   = 15 * time.Minute
	daemonStartTimeout  = 5 * time.Second
	daemonStartPoll     = 10 * time.Millisecond
	daemonMaxRequest    = 4 << 20
	daemonSocketNameLen = 16
	daemonStdioCount    = 3
	daemonDirPerm       = 0o700
	daemonLockPerm      = 0o600
	daemonHeaderSize    = 4
	daemonFDSize        = 4
)

var daemonConfigNames = []string{".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json"}

// daemonIdentityEnv selects the variables read once per process (runtime, go
// command, caches, debug switches). Each run still gets the client's full
// environment.
var (
	daemonIdentityEnvPrefixes = []string{"GO", "CGO_", "GL_", "LOG_", "XDG_", "PKG_CONFIG"}
	daemonIdentityEnvKeys     = []string{"PATH", "HOME", "TMPDIR", "CC", "CXX"}
)

func isDaemonIdentityEnv(key string) bool {
	return slices.Contains(daemonIdentityEnvKeys, key) ||
		slices.ContainsFunc(daemonIdentityEnvPrefixes, func(prefix string) bool { return strings.HasPrefix(key, prefix) })
}

type daemonRequest struct {
	Fingerprint string
	Args        []string
	Dir         string
	Env         []string
}

type daemonReply struct {
	ExitCode int
	Error    string
	// Rejected means the run did not start; the client runs it in-process.
	Rejected bool
}

// TryExecuteDaemon serves runs from a long-lived process when GOLT_DAEMON=1.
// It also starts the daemon side when this process was spawned as one.
func TryExecuteDaemon(info BuildInfo) (handled bool, exitCode int, err error) {
	if socket, ok := os.LookupEnv(envDaemonServe); ok {
		return true, exitcodes.Success, serveDaemon(info, socket)
	}

	if os.Getenv(envDaemon) != "1" || !isRunCommand(info, os.Args[1:]) {
		return false, 0, nil
	}

	return runViaDaemon()
}

func isRunCommand(info BuildInfo, args []string) bool {
	root := newRootCommandWithRunOptions(info, &runCommandOptions{})

	return isWorkerRun(root, args)
}

// daemonIdentity names the daemon for everything a run reads once per process
// or keeps in process-global state: the binary, directory, arguments, relevant
// environment and configuration files. Any change starts a separate daemon.
type daemonIdentity struct {
	exe         string
	dir         string
	fingerprint string
	socket      string
}

func newDaemonIdentity() (*daemonIdentity, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}

	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	h := sha256.New()

	writeStamp := func(path string) {
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(h, "%s missing\n", path)

			return
		}

		fmt.Fprintf(h, "%s %d %d\n", path, info.Size(), info.ModTime().UnixNano())
	}

	writeStamp(exe)
	fmt.Fprintf(h, "dir %s\nargs %q\n", dir, os.Args[1:])

	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		key, _, _ := strings.Cut(kv, "=")

		return !isDaemonIdentityEnv(key)
	})
	slices.Sort(env)
	fmt.Fprintf(h, "env %q\n", env)

	for _, path := range daemonConfigCandidates(dir, os.Args[1:]) {
		writeStamp(path)
	}

	fingerprint := hex.EncodeToString(h.Sum(nil))

	socketDir := filepath.Join(os.TempDir(), fmt.Sprintf("golt-%d", os.Getuid()))

	return &daemonIdentity{
		exe:         exe,
		dir:         dir,
		fingerprint: fingerprint,
		socket:      filepath.Join(socketDir, fingerprint[:daemonSocketNameLen]+".sock"),
	}, nil
}

// daemonConfigCandidates lists every file the config loader may read: an
// explicit --config, and the default names from dir up to the root and in home.
func daemonConfigCandidates(dir string, args []string) []string {
	var paths []string

	for i, arg := range args {
		switch {
		case (arg == "-c" || arg == "--config") && i+1 < len(args):
			paths = append(paths, absFrom(dir, args[i+1]))
		case strings.HasPrefix(arg, "--config="):
			paths = append(paths, absFrom(dir, strings.TrimPrefix(arg, "--config=")))
		}
	}

	dirs := []string{}
	for d := dir; ; d = filepath.Dir(d) {
		dirs = append(dirs, d)
		if filepath.Dir(d) == d {
			break
		}
	}

	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, home)
	}

	for _, d := range dirs {
		for _, name := range daemonConfigNames {
			paths = append(paths, filepath.Join(d, name))
		}
	}

	return paths
}

func absFrom(dir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}

	return filepath.Join(dir, path)
}

// runViaDaemon forwards this run to the daemon, starting one if needed. It
// reports handled=false when no daemon could be reached before the request was
// sent, so the caller can run in-process instead.
func runViaDaemon() (handled bool, exitCode int, err error) {
	id, err := newDaemonIdentity()
	if err != nil {
		return false, 0, nil
	}

	conn, err := dialDaemon(id)
	if err != nil {
		return false, 0, nil
	}
	defer conn.Close()

	request := daemonRequest{
		Fingerprint: id.fingerprint,
		Args:        os.Args[1:],
		Dir:         id.dir,
		Env:         os.Environ(),
	}

	stdio := []int{int(os.Stdin.Fd()), int(os.Stdout.Fd()), int(os.Stderr.Fd())}

	if err := writeDaemonRequest(conn, &request, stdio); err != nil {
		return false, 0, nil
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	go func() {
		sig := <-signals
		_ = conn.Close()

		code := exitcodes.Failure
		if s, ok := sig.(syscall.Signal); ok {
			code = 128 + int(s)
		}
		os.Exit(code)
	}()

	var reply daemonReply
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		return true, exitcodes.Failure, fmt.Errorf("golt daemon connection lost: %w", err)
	}

	if reply.Rejected {
		return false, 0, nil
	}

	if reply.Error != "" {
		return true, reply.ExitCode, errors.New(reply.Error)
	}

	return true, reply.ExitCode, nil
}

func dialDaemon(id *daemonIdentity) (*net.UnixConn, error) {
	if conn, err := dialDaemonSocket(id.socket); err == nil {
		return conn, nil
	}

	if err := ensureDaemonDir(filepath.Dir(id.socket)); err != nil {
		return nil, err
	}

	if err := startDaemon(id); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(daemonStartTimeout)
	for {
		conn, err := dialDaemonSocket(id.socket)
		if err == nil {
			return conn, nil
		}

		if time.Now().After(deadline) {
			return nil, err
		}

		time.Sleep(daemonStartPoll)
	}
}

func dialDaemonSocket(socket string) (*net.UnixConn, error) {
	return net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
}

// ensureDaemonDir creates the per-user socket directory and refuses one that
// another user could write to.
func ensureDaemonDir(dir string) error {
	if err := os.Mkdir(dir, daemonDirPerm); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}

	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || int(stat.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("unsafe daemon directory %s", dir)
	}

	return nil
}

func startDaemon(id *daemonIdentity) error {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()

	cmd := &exec.Cmd{
		Path:        id.exe,
		Args:        []string{id.exe},
		Dir:         id.dir,
		Env:         append(os.Environ(), envDaemonServe+"="+id.socket, envDaemonID+"="+id.fingerprint),
		Stdin:       devNull,
		Stdout:      devNull,
		Stderr:      devNull,
		SysProcAttr: &syscall.SysProcAttr{Setsid: true},
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	return cmd.Process.Release()
}

func writeDaemonRequest(conn *net.UnixConn, request *daemonRequest, stdio []int) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}

	size := len(payload)
	if size < 0 || size > daemonMaxRequest {
		return fmt.Errorf("request too large: %d bytes", size)
	}

	message := binary.BigEndian.AppendUint32(nil, uint32(size))
	message = append(message, payload...)

	rights := unix.UnixRights(stdio...)

	n, _, err := conn.WriteMsgUnix(message, rights, nil)
	if err != nil {
		return err
	}

	_, err = conn.Write(message[n:])

	return err
}

// readDaemonRequest reads a request and the client's stdin, stdout and stderr.
func readDaemonRequest(conn *net.UnixConn) (*daemonRequest, []int, error) {
	header := make([]byte, daemonHeaderSize)
	oob := make([]byte, unix.CmsgSpace(daemonStdioCount*daemonFDSize))

	n, oobn, _, _, err := conn.ReadMsgUnix(header, oob)
	if err != nil {
		return nil, nil, err
	}

	fds, err := parseDaemonRights(oob[:oobn])
	if err != nil {
		return nil, nil, err
	}

	closeAll := func() {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}

	if len(fds) != daemonStdioCount {
		closeAll()

		return nil, nil, fmt.Errorf("expected %d descriptors, got %d", daemonStdioCount, len(fds))
	}

	reader := bufio.NewReader(conn)

	if _, err := io.ReadFull(reader, header[n:]); err != nil {
		closeAll()

		return nil, nil, err
	}

	size := binary.BigEndian.Uint32(header)
	if size > daemonMaxRequest {
		closeAll()

		return nil, nil, fmt.Errorf("request too large: %d bytes", size)
	}

	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		closeAll()

		return nil, nil, err
	}

	var request daemonRequest
	if err := json.Unmarshal(payload, &request); err != nil {
		closeAll()

		return nil, nil, err
	}

	return &request, fds, nil
}

func parseDaemonRights(oob []byte) ([]int, error) {
	messages, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, err
	}

	var fds []int

	for i := range messages {
		rights, err := unix.ParseUnixRights(&messages[i])
		if err != nil {
			return nil, err
		}

		fds = append(fds, rights...)
	}

	return fds, nil
}

type daemonServer struct {
	info        BuildInfo
	fingerprint string
	baseEnv     []string
	devNull     int
	active      atomic.Pointer[net.UnixConn]
}

// serveDaemon answers runs one at a time until idle. The lock file makes a
// single daemon own the socket, so stale sockets can be replaced safely.
func serveDaemon(info BuildInfo, socket string) error {
	fingerprint := os.Getenv(envDaemonID)

	_ = os.Unsetenv(envDaemonServe)
	_ = os.Unsetenv(envDaemonID)

	lock, err := os.OpenFile(socket+".lock", os.O_CREATE|os.O_RDWR, daemonLockPerm)
	if err != nil {
		return err
	}
	defer lock.Close()

	if unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return nil
	}

	_ = os.Remove(socket)

	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return err
	}
	defer listener.Close()

	devNull, err := unix.Open(os.DevNull, unix.O_RDWR, 0)
	if err != nil {
		return err
	}

	server := &daemonServer{
		info:        info,
		fingerprint: fingerprint,
		baseEnv:     os.Environ(),
		devNull:     devNull,
	}

	processexit.Set(server.exitMidRun)

	idle := daemonDefaultIdle
	if value, err := time.ParseDuration(os.Getenv(envDaemonIdle)); err == nil && value > 0 {
		idle = value
	}

	for {
		if err := listener.SetDeadline(time.Now().Add(idle)); err != nil {
			return err
		}

		conn, err := listener.AcceptUnix()
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				_ = os.Remove(socket)

				return nil
			}

			return err
		}

		server.handle(conn)
	}
}

func (s *daemonServer) handle(conn *net.UnixConn) {
	defer conn.Close()

	request, fds, err := readDaemonRequest(conn)
	if err != nil {
		return
	}

	if request.Fingerprint != s.fingerprint {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}

		s.reply(conn, daemonReply{Rejected: true})

		return
	}

	s.active.Store(conn)

	go s.exitOnDisconnect(conn)

	restore := s.attach(request, fds)

	_, exitCode, fatal, err := executeInProcessRun(s.info, workerprotocol.RunPayload{
		Args:             request.Args,
		WorkingDirectory: request.Dir,
	})
	if fatal {
		s.exitMidRun(exitCode)
	}

	var reply daemonReply

	reply.ExitCode = exitCode
	if err != nil {
		reply.Error = err.Error()
	}

	restore()

	s.active.Store(nil)
	s.reply(conn, reply)

	debug.FreeOSMemory()
}

// attach makes the client's stdio, environment and terminal state this
// process's own for the duration of a run.
func (s *daemonServer) attach(request *daemonRequest, fds []int) func() {
	for i, fd := range fds {
		_ = unix.Dup2(fd, i)
		_ = unix.Close(fd)
	}

	setEnviron(request.Env)

	color.NoColor = os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" ||
		(!isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd()))

	return func() {
		for i := range daemonStdioCount {
			_ = unix.Dup2(s.devNull, i)
		}

		setEnviron(s.baseEnv)
	}
}

// exitOnDisconnect stops the daemon when the client goes away mid-run
// (e.g. Ctrl-C): analysis cannot be canceled, and its output has no reader.
func (s *daemonServer) exitOnDisconnect(conn *net.UnixConn) {
	buf := make([]byte, 1)

	_, err := conn.Read(buf)
	if err != nil && s.active.Load() == conn {
		os.Exit(exitcodes.Failure)
	}
}

// exitMidRun handles a process exit requested during a run: the run cannot be
// unwound, so the daemon answers the client and exits.
func (s *daemonServer) exitMidRun(code int) {
	if conn := s.active.Load(); conn != nil {
		s.reply(conn, daemonReply{ExitCode: code})
	}

	os.Exit(code)
}

func (*daemonServer) reply(conn *net.UnixConn, reply daemonReply) {
	_ = json.NewEncoder(conn).Encode(reply)
}

func setEnviron(env []string) {
	os.Clearenv()

	for _, kv := range env {
		key, value, ok := strings.Cut(kv, "=")
		if ok && key != "" {
			_ = os.Setenv(key, value)
		}
	}
}
