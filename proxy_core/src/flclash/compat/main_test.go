package compat

import (
	"fmt"
	"os"
	"testing"

	"github.com/metacubex/mihomo/component/profile/cachefile"
	C "github.com/metacubex/mihomo/constant"
)

func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "clashbox-compat-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	C.SetHomeDir(home)
	// Initialize the singleton in our disposable home, never in the user's home.
	cache := cachefile.Cache()
	code := m.Run()
	if cache.DB != nil {
		_ = cache.Close()
	}
	if err := os.RemoveAll(home); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}
