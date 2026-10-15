//go:build !linux

package discovery

import "portman/internal/model"

func New(inspector model.Inspector) model.Discoverer {
	return &Lsof{Inspector: inspector, Run: runLsof}
}
