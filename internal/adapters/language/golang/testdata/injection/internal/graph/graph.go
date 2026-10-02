package graph

import "github.com/smarty/injection/internal/sorting"

type Lifetime struct {
	Scope string
	TTL   int
}

type Binding struct {
	Name     string
	Lifetime Lifetime
}

type Graph struct {
	bindings []Binding
}

func New() *Graph {
	return &Graph{}
}

func (g *Graph) Order() ([]Binding, error) {
	if len(g.bindings) == 0 {
		return nil, nil
	}

	return topoSort(g.bindings)
}

func topoSort(bs []Binding) ([]Binding, error) {
	names := make([]string, len(bs))
	for i, b := range bs {
		names[i] = b.Name
	}

	sorting.Stable(names)
	return bs, nil
}
