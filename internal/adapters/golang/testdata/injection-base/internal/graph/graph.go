package graph

import "github.com/smarty/injection/internal/sorting"

type Lifetime struct {
	Scope string
}

type Binding struct {
	Name string
}

type Graph struct {
	bindings []Binding
}

func New() *Graph {
	return &Graph{}
}

func (g *Graph) Order() ([]Binding, error) {
	return topoSort(g.bindings)
}

func topoSort(bs []Binding) ([]Binding, error) {
	names := make([]string, len(bs))
	sorting.Stable(names)
	return bs, nil
}
