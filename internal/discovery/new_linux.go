package discovery

import "portman/internal/model"

func New(inspector model.Inspector) model.Discoverer {
	return &ProcFS{Inspector: inspector, Root: "/proc"}
}
