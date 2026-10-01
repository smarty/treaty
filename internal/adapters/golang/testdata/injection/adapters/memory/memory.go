package memory

import "github.com/smarty/injection/internal/graph"

type Store struct {
	bindings []graph.Binding
}

func (s *Store) Add(binding graph.Binding) {
	s.bindings = append(s.bindings, binding)
}
