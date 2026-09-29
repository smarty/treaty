package injection

import (
	"context"

	"github.com/smarty/injection/internal/graph"
)

type Key struct {
	Name string
	kind int
}

type Container interface {
	Resolve(ctx context.Context, k Key) (any, error)
}

type container struct {
	g *graph.Graph
}

func New() Container {
	return &container{g: graph.New()}
}

func (c *container) Resolve(ctx context.Context, k Key) (any, error) {
	order, err := c.g.Order()
	if err != nil {
		return nil, err
	}

	return resolveLocked(order, k), nil
}

func resolveLocked(order []graph.Binding, k Key) any {
	for _, binding := range order {
		if binding.Name == k.Name {
			return binding
		}
	}

	return nil
}
