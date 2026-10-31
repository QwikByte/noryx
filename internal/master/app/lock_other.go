//go:build !unix

package app

// lockDataDir can't tell whether a master runs on other systems than Unix, where the master
// only runs for development.
func lockDataDir(dataDir string) (func() error, error) {
	return func() error { return nil }, checkDataDir(dataDir)
}
