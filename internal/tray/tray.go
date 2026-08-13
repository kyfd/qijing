package tray

// Actions are called asynchronously by the platform tray implementation.
type Actions struct {
	Show func()
	Quit func()
}
