// Package metrics is deliberate noise: the metrics-tracing heuristic should
// drop every call to it.
package metrics

type Counter struct{}

func (c *Counter) Inc()              {}
func (c *Counter) Add(delta float64) {}

var (
	Requests = &Counter{}
	Orders   = &Counter{}
)
