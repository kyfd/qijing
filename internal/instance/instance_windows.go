//go:build windows

package instance

import (
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sys/windows"
)

const (
	mutexName = `Local\FileEcosystem.Desktop.Singleton`
	eventName = `Local\FileEcosystem.Desktop.Activate`
)

// Lock owns the per-user process mutex and receives activation requests from
// later processes through a named event.
type Lock struct {
	mutex windows.Handle
	event windows.Handle
	done  chan struct{}
	once  sync.Once

	mu       sync.Mutex
	activate func()
	pending  bool
}

// Acquire returns (nil, false, nil) in a later process after signalling the
// existing process. A true result owns the mutex until Close is called.
func Acquire() (*Lock, bool, error) {
	mutexName16, err := windows.UTF16PtrFromString(mutexName)
	if err != nil {
		return nil, false, err
	}
	mutex, mutexErr := windows.CreateMutex(nil, false, mutexName16)
	if mutex == 0 {
		return nil, false, fmt.Errorf("create single-instance mutex: %w", mutexErr)
	}
	if errors.Is(mutexErr, windows.ERROR_ALREADY_EXISTS) {
		_ = windows.CloseHandle(mutex)
		if err := signalExisting(); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}

	eventName16, err := windows.UTF16PtrFromString(eventName)
	if err != nil {
		_ = windows.CloseHandle(mutex)
		return nil, false, err
	}
	event, eventErr := windows.CreateEvent(nil, 0, 0, eventName16)
	if event == 0 {
		_ = windows.CloseHandle(mutex)
		return nil, false, fmt.Errorf("create activation event: %w", eventErr)
	}
	lock := &Lock{mutex: mutex, event: event, done: make(chan struct{})}
	go lock.listen()
	return lock, true, nil
}

func signalExisting() error {
	name, err := windows.UTF16PtrFromString(eventName)
	if err != nil {
		return err
	}
	event, eventErr := windows.CreateEvent(nil, 0, 0, name)
	if event == 0 {
		return fmt.Errorf("open activation event: %w", eventErr)
	}
	defer windows.CloseHandle(event)
	if err := windows.SetEvent(event); err != nil {
		return fmt.Errorf("signal existing instance: %w", err)
	}
	return nil
}

// OnActivate installs the callback used to restore the desktop window.
func (l *Lock) OnActivate(callback func()) {
	l.mu.Lock()
	l.activate = callback
	pending := l.pending
	l.pending = false
	l.mu.Unlock()
	if pending && callback != nil {
		go callback()
	}
}

func (l *Lock) listen() {
	for {
		result, err := windows.WaitForSingleObject(l.event, windows.INFINITE)
		if err != nil || result != windows.WAIT_OBJECT_0 {
			return
		}
		select {
		case <-l.done:
			return
		default:
		}
		l.mu.Lock()
		callback := l.activate
		if callback == nil {
			l.pending = true
		}
		l.mu.Unlock()
		if callback != nil {
			go callback()
		}
	}
}

func (l *Lock) Close() error {
	var closeErr error
	l.once.Do(func() {
		close(l.done)
		_ = windows.SetEvent(l.event)
		if err := windows.CloseHandle(l.event); err != nil {
			closeErr = err
		}
		if err := windows.CloseHandle(l.mutex); err != nil && closeErr == nil {
			closeErr = err
		}
	})
	return closeErr
}
