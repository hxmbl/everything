// Persisted user preferences (color on/off, theme name), stored under the
// user cache dir so a flag sticks across runs.

package main

import (
	"os"
	"path/filepath"
	"strings"
)

// colorCachePath returns the path to the color preference cache file.
func colorCachePath() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cacheDir, "everything", "color")
}

// loadSavedColor loads the saved color preference from the cache file.
// Returns false if the file doesn't exist or cannot be read.
func loadSavedColor() bool {
	data, err := os.ReadFile(colorCachePath())
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "true"
}

// saveColor saves the color preference to the cache file.
// Returns an error if the cache directory cannot be created or the file cannot be written.
func saveColor(on bool) error {
	path := colorCachePath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	val := "false"
	if on {
		val = "true"
	}
	return os.WriteFile(path, []byte(val), 0644)
}

// themeCachePath returns the path to the theme preference cache file.
func themeCachePath() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cacheDir, "everything", "theme")
}

// loadSavedTheme loads the saved theme preference from the cache file.
// Returns an empty string if the file doesn't exist or cannot be read.
func loadSavedTheme() string {
	data, err := os.ReadFile(themeCachePath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// saveTheme saves the theme preference to the cache file.
// Returns an error if the cache directory cannot be created or the file cannot be written.
func saveTheme(name string) error {
	path := themeCachePath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(name), 0644)
}
