//go:build windows

package tray

import (
	"sync"

	"fileecosystem/internal/appicon"

	"fyne.io/systray"
)

type Tray struct {
	actions Actions
	start   func()
	end     func()
	once    sync.Once
}

func New(actions Actions) (*Tray, error) {
	t := &Tray{actions: actions}
	t.start, t.end = systray.RunWithExternalLoop(t.ready, nil)
	return t, nil
}

func (t *Tray) Start() { t.start() }

func (t *Tray) Close() {
	t.once.Do(func() { t.end() })
}

func (t *Tray) ready() {
	systray.SetIcon(appicon.ICO)
	systray.SetTooltip("栖境 · 文件生态系统")
	systray.SetOnTapped(t.actions.Show)
	show := systray.AddMenuItem("显示栖境", "显示主窗口")
	systray.AddSeparator()
	quit := systray.AddMenuItem("退出", "完全退出栖境")
	go func() {
		for {
			select {
			case _, ok := <-show.ClickedCh:
				if !ok {
					return
				}
				if t.actions.Show != nil {
					t.actions.Show()
				}
			case _, ok := <-quit.ClickedCh:
				if !ok {
					return
				}
				if t.actions.Quit != nil {
					t.actions.Quit()
				}
			}
		}
	}()
}
