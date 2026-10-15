// Package platform provides operating-system-specific process inspection.
package platform

import "errors"

var ErrGone = errors.New("process no longer exists")

type Inspector struct{}
