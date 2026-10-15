//go:build (!darwin && !linux) || (darwin && !cgo)

package platform

import (
	"context"
	"errors"
	"portman/internal/model"
)

func Supported() error {
	return errors.New("Portman supports Linux and macOS; macOS requires a build with CGO_ENABLED=1 (Xcode Command Line Tools)")
}
func (Inspector) Inspect(context.Context, int) (model.Process, error) {
	return model.Process{}, Supported()
}
