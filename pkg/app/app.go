package app

// App is the top-level application container. Downstream epics extend this.
type App struct{}

// Run executes the App lifecycle. Stub returns nil for now.
func (a *App) Run() error {
	return nil
}
