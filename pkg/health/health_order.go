package health

// SortOrder sorts the nodes in the list from least to most health.
type SortOrder struct {
	Nodes  []string
	Health map[string]Result
}

func (s SortOrder) Len() int {
	return len(s.Nodes)
}

func (s SortOrder) Swap(i, j int) {
	s.Nodes[i], s.Nodes[j] = s.Nodes[j], s.Nodes[i]
}

func (s SortOrder) Less(i, j int) bool {
	iHealth := Unknown
	if res, ok := s.Health[s.Nodes[i]]; ok {
		iHealth = res.Status
	}

	jHealth := Unknown
	if res, ok := s.Health[s.Nodes[j]]; ok {
		jHealth = res.Status
	}

	return Compare(iHealth, jHealth) < 0
}
