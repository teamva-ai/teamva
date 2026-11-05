package store

func cyclePath(adjacency map[string][]string, source string, newTargets []string) []string {
	for _, target := range newTargets {
		path, ok := pathBetween(adjacency, target, source, map[string]bool{})
		if ok {
			return append([]string{source}, path...)
		}
	}
	return nil
}

func pathBetween(adjacency map[string][]string, current, goal string, visited map[string]bool) ([]string, bool) {
	if current == goal {
		return []string{current}, true
	}
	if visited[current] {
		return nil, false
	}
	visited[current] = true
	for _, next := range adjacency[current] {
		if path, ok := pathBetween(adjacency, next, goal, visited); ok {
			return append([]string{current}, path...), true
		}
	}
	return nil, false
}
