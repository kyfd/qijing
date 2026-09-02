//go:build windows

package ipcpipe

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func pipeName(unique string) string { return `\\.\pipe\` + unique }

const (
	pipeOpenMode = windows.PIPE_ACCESS_DUPLEX | windows.FILE_FLAG_OVERLAPPED
	pipeMode     = windows.PIPE_TYPE_BYTE | windows.PIPE_READMODE_BYTE | windows.PIPE_WAIT
	// pipeInstances is one: a scanner pipe serves exactly one conversation.
	pipeInstances  = 1
	pipeBufferSize = 1 << 20
)

var ErrListenerClosed = errors.New("listener closed")

// currentUserSDDL builds a security descriptor string granting only the
// current user generic access. Everyone, other users and low-privilege
// processes are denied by absence.
func currentUserSDDL() (string, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return "", fmt.Errorf("open process token: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read token user: %w", err)
	}
	return "D:P(A;;GA;;;" + user.User.Sid.String() + ")", nil
}

// pipeConn is an overlapped-I/O stream around a duplex pipe handle. The
// pipe is opened with FILE_FLAG_OVERLAPPED because the protocol reads
// (cancel reader) and writes (progress, heartbeat) concurrently; on a
// synchronous handle Windows would serialize those into a deadlock.
type pipeConn struct {
	handle     windows.Handle
	readEvent  windows.Handle
	writeEvent windows.Handle
	closeOnce  sync.Once
}

func wrapHandle(handle windows.Handle) (*pipeConn, error) {
	readEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("create read event: %w", err)
	}
	writeEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(readEvent)
		return nil, fmt.Errorf("create write event: %w", err)
	}
	return &pipeConn{handle: handle, readEvent: readEvent, writeEvent: writeEvent}, nil
}

// overlappedIO runs one overlapped operation and waits for its completion
// event. Direction serialization happens through the caller's mutex. The
// done pointer is passed into the call as well because overlapped
// operations may complete immediately, in which case GetOverlappedResult
// would never see the transfer length.
func (c *pipeConn) overlappedIO(call func(buf []byte, done *uint32, overlapped *windows.Overlapped) error, buf []byte, event windows.Handle) (int, error) {
	// The event is manual-reset, so a previously completed operation on the
	// same direction would otherwise let the next WaitForSingleObject
	// return before its own I/O finished.
	if err := windows.ResetEvent(event); err != nil {
		return 0, fmt.Errorf("reset pipe I/O event: %w", err)
	}
	var overlapped windows.Overlapped
	overlapped.HEvent = event
	var done uint32
	err := call(buf, &done, &overlapped)
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		if _, err := windows.WaitForSingleObject(event, windows.INFINITE); err != nil {
			return 0, fmt.Errorf("wait for pipe I/O: %w", err)
		}
		err = windows.GetOverlappedResult(c.handle, &overlapped, &done, false)
	}
	if err != nil {
		// The peer going away is an EOF for our purposes.
		if errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, windows.ERROR_OPERATION_ABORTED) {
			return 0, io.EOF
		}
		return 0, err
	}
	return int(done), nil
}

func (c *pipeConn) Read(buf []byte) (int, error) {
	return c.overlappedIO(func(b []byte, done *uint32, o *windows.Overlapped) error {
		return windows.ReadFile(c.handle, b, done, o)
	}, buf, c.readEvent)
}

func (c *pipeConn) Write(buf []byte) (int, error) {
	return c.overlappedIO(func(b []byte, done *uint32, o *windows.Overlapped) error {
		return windows.WriteFile(c.handle, b, done, o)
	}, buf, c.writeEvent)
}

// Close cancels any pending I/O first so a blocked Read or Write returns,
// then closes the handle.
func (c *pipeConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		_ = windows.CancelIoEx(c.handle, nil)
		err = windows.CloseHandle(c.handle)
		_ = windows.CloseHandle(c.readEvent)
		_ = windows.CloseHandle(c.writeEvent)
	})
	return err
}

type pipeListener struct {
	name   string
	handle windows.Handle
	event  windows.Handle
	closed chan struct{}
	once   sync.Once
}

// Listen creates the pipe with a current-user-only DACL before any client
// can connect.
func Listen() (Listener, error) {
	name, err := RandomName()
	if err != nil {
		return nil, err
	}
	return ListenOn(name)
}

// ListenOn creates the listener on an explicit name (used by tests).
func ListenOn(name string) (Listener, error) {
	sddl, err := currentUserSDDL()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("build pipe DACL: %w", err)
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	nameUTF16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateNamedPipe(nameUTF16, pipeOpenMode, pipeMode, pipeInstances,
		pipeBufferSize, pipeBufferSize, 0, sa)
	if err != nil {
		return nil, fmt.Errorf("create named pipe: %w", err)
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("create accept event: %w", err)
	}
	return &pipeListener{name: name, handle: handle, event: event, closed: make(chan struct{})}, nil
}

func isErrno(err error, target windows.Errno) bool {
	errno, ok := err.(windows.Errno)
	return ok && errno == target
}

// Accept blocks until a client connects. The wait is cancellable: Close
// aborts the pending ConnectNamedPipe via CancelIoEx.
func (l *pipeListener) Accept() (Conn, error) {
	var overlapped windows.Overlapped
	overlapped.HEvent = l.event
	err := windows.ConnectNamedPipe(l.handle, &overlapped)
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		_, err = windows.WaitForSingleObject(l.event, windows.INFINITE)
		if err == nil {
			var done uint32
			err = windows.GetOverlappedResult(l.handle, &overlapped, &done, false)
		}
	}
	if err != nil && !isErrno(err, windows.ERROR_PIPE_CONNECTED) {
		_ = windows.CloseHandle(l.handle)
		_ = windows.CloseHandle(l.event)
		if isErrno(err, windows.ERROR_OPERATION_ABORTED) {
			return nil, ErrListenerClosed
		}
		return nil, fmt.Errorf("accept named pipe connection: %w", err)
	}
	select {
	case <-l.closed:
		_ = windows.CloseHandle(l.handle)
		_ = windows.CloseHandle(l.event)
		return nil, ErrListenerClosed
	default:
	}
	conn, err := wrapHandle(l.handle)
	if err != nil {
		_ = windows.CloseHandle(l.handle)
		_ = windows.CloseHandle(l.event)
		return nil, err
	}
	return conn, nil
}

// Close cancels a pending Accept and releases the pipe instance.
func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	_ = windows.CancelIoEx(l.handle, nil)
	return nil
}

func (l *pipeListener) Name() string { return l.name }

// Dial connects to the named pipe, retrying a busy server until timeout.
func Dial(name string, timeout time.Duration) (Conn, error) {
	deadline := time.Now().Add(timeout)
	nameUTF16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	for {
		handle, err := windows.CreateFile(nameUTF16, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
		if err == nil {
			return wrapHandle(handle)
		}
		if isErrno(err, windows.ERROR_PIPE_BUSY) {
			waitNamedPipe(nameUTF16, 1000)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("connect to scanner pipe: %w", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

var (
	kernel32       = windows.NewLazySystemDLL("kernel32.dll")
	waitNamedPipeW = kernel32.NewProc("WaitNamedPipeW")
)

// waitNamedPipe wraps WaitNamedPipeW, which x/sys/windows does not declare.
// A zero result with a timeout means the pipe stayed busy.
func waitNamedPipe(name *uint16, timeoutMS uint32) bool {
	result, _, _ := waitNamedPipeW.Call(uintptr(unsafe.Pointer(name)), uintptr(timeoutMS))
	return result != 0
}
