package store

import (
	"fmt"
	"sync"
)

// DriverFunc creates a Store from a DSN string.
// Each backend (sqlite, postgres, etc.) registers its constructor.
type DriverFunc func(dsn string) (Store, error)

var (
	driversMu sync.RWMutex
	drivers   = make(map[string]DriverFunc)
)

// Register makes a driver available by the provided name.
// If Register is called twice with the same name or if driver
// is nil, it panics. Safe to call from init().
func Register(name string, driver DriverFunc) {
	driversMu.Lock()
	defer driversMu.Unlock()
	if driver == nil {
		panic("store: Register driver is nil")
	}
	if _, dup := drivers[name]; dup {
		panic("store: Register called twice for driver " + name)
	}
	drivers[name] = driver
}

// Open creates a Store using the driver registered under name.
// name is matched case-insensitively. The default (empty string
// or "sqlite") opens the built-in SQLite driver.
func Open(name, dsn string) (Store, error) {
	if name == "" {
		name = "sqlite"
	}
	driversMu.RLock()
	d, ok := drivers[name]
	driversMu.RUnlock()
	if !ok {
		// Check case-insensitive match.
		for k, v := range drivers {
			if equalFold(k, name) {
				d = v
				ok = true
				break
			}
		}
	}
	if !ok {
		return nil, fmt.Errorf("store: unknown driver %q (registered: %v)", name, driverNames())
	}
	return d(dsn)
}

// driverNames returns a sorted-ish list for the error message.
func driverNames() []string {
	driversMu.RLock()
	defer driversMu.RUnlock()
	names := make([]string, 0, len(drivers))
	for k := range drivers {
		names = append(names, k)
	}
	return names
}

// equalFold is ASCII-only case-insensitive comparison (driver
// names are always lowercase ASCII).
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func init() {
	Register("sqlite", func(dsn string) (Store, error) {
		return OpenSQLite(dsn)
	})
}
