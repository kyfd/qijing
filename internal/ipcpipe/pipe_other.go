//go:build !windows

package ipcpipe

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

var ErrListenerClosed = errors.New("listener closed")

func pipeName(unique string) string {
	return filepath.Join(os.TempDir(), unique+".sock")
}

type socketListener struct {
	*net.UnixListener
	name string
}

// Listen keeps non-Windows development builds compiling. The product only
// ships the Windows path; this fallback stores no state beyond a socket file
// in the user's temp directory.
func Listen() (Listener, error) {
	name, err := RandomName()
	if err != nil {
		return nil, err
	}
	return ListenOn(name)
}

func ListenOn(name string) (Listener, error) {
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: name, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("create local socket: %w", err)
	}
	return &socketListener{UnixListener: listener, name: name}, nil
}

func (l *socketListener) Accept() (Conn, error) {
	conn, err := l.UnixListener.AcceptUnix()
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (l *socketListener) Name() string { return l.name }

// Dial connects to the local socket, retrying until timeout.
func Dial(name string, timeout time.Duration) (Conn, error) {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: name, Net: "unix"})
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("connect to scanner socket: %w", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
