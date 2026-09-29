package graph

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
	return bs, nil
}
