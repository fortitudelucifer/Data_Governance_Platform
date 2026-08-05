package util

import (
	"fmt"
	"sync/atomic"
	"time"
)

// DefaultTimezone is the fallback IANA location used when no timezone is
// configured. Asia/Shanghai matches historical hard-coded behavior so deploys
// that omit TZ keep the same wall-clock display.
const DefaultTimezone = "Asia/Shanghai"

// appLocation holds the process-wide timezone used by all user-facing time
// formatting code (JSONTime, dataset MarshalJSON, etc.).
//
// Stored via atomic.Value so SetAppLocation can replace it after init without
// requiring a mutex on every read. Hot paths in time formatting just call
// AppLocation() and get the current pointer.
var appLocation atomic.Value // *time.Location

func init() {
	loc, err := time.LoadLocation(DefaultTimezone)
	if err != nil {
		// tzdata missing in the binary or container — fall back to a fixed
		// CST+8 zone so behavior matches the legacy hard-coded path.
		loc = time.FixedZone("CST", 8*3600)
	}
	appLocation.Store(loc)
}

// AppLocation returns the timezone used for user-facing time rendering.
// Safe to call from any goroutine.
func AppLocation() *time.Location {
	return appLocation.Load().(*time.Location)
}

// SetAppLocation loads the named IANA timezone (e.g. "Asia/Shanghai", "UTC")
// and installs it as the process-wide AppLocation. An empty name is a no-op.
// Returns an error if the timezone database does not contain the given name.
func SetAppLocation(name string) error {
	if name == "" {
		return nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return fmt.Errorf("load timezone %q: %w", name, err)
	}
	appLocation.Store(loc)
	return nil
}
