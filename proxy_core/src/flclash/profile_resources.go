package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The only provider mutation is resolving a portable path against the imported
// profile directory. URLs, health checks, intervals, format and behavior stay intact.
func resolveProviderResources(base string, providers map[string]map[string]any) error {
	for name, mapping := range providers {
		value, present := mapping["path"]
		kind, _ := mapping["type"].(string)
		if !present {
			if kind == "file" {
				return fmt.Errorf("file provider %q has no path", name)
			}
			continue
		}
		relative, ok := value.(string)
		if !ok {
			return fmt.Errorf("provider %q path must be a string", name)
		}
		path, err := profileResourcePath(base, relative)
		if err != nil {
			return fmt.Errorf("provider %q: %w", name, err)
		}
		if kind == "file" {
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("file provider %q is missing; import its resource bundle", name)
			}
		} else if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return fmt.Errorf("provider %q cache directory cannot be created", name)
		}
		mapping["path"] = path
	}
	return nil
}

func profileResourcePath(base, relative string) (string, error) {
	if relative == "" || strings.HasPrefix(relative, "/") || filepath.IsAbs(relative) || strings.ContainsAny(relative, "\\:\x00") {
		return "", errors.New("resource path must be relative to the profile")
	}
	for _, part := range strings.Split(relative, "/") {
		if part == ".." {
			return "", errors.New("resource path cannot leave the profile")
		}
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || strings.EqualFold(clean, "config.yaml") {
		return "", errors.New("resource path conflicts with the configuration")
	}
	return filepath.Join(base, clean), nil
}
