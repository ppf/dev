//go:build !darwin

package desktop

import "time"

// procStart is unknown off macOS: a live file is checked by its pid alone.
func procStart(int) (time.Time, bool) { return time.Time{}, false }
