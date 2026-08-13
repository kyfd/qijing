//go:build !windows

package tray

import "errors"

type Tray struct{}

func New(Actions) (*Tray, error) {
	return nil, errors.New("notification-area tray is only available on Windows")
}
func (t *Tray) Start() {}
func (t *Tray) Close() {}
