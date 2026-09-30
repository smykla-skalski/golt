//go:build !unix

package commands

// TryExecuteDaemon is unsupported without unix sockets and descriptor passing.
func TryExecuteDaemon(BuildInfo) (handled bool, exitCode int, err error) {
	return false, 0, nil
}
