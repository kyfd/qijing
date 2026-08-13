//go:build !windows

package instance

import "errors"

type Lock struct{}

func Acquire() (*Lock, bool, error) {
	return nil, false, errors.New("desktop single-instance lock is only available on Windows")
}
func (l *Lock) OnActivate(func()) {}
func (l *Lock) Close() error      { return nil }
