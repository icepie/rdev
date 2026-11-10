package server

// Each resource owns its input lock and cached connections. The root retains
// the latest UI view and coordinates cancellation for the whole conversation.
func (t automationTool) forResource(key string) automationTool {
	root := t.state.root()
	root.mu.Lock()
	defer root.mu.Unlock()
	if root.resource == "" {
		root.resource = key
	}
	if root.resource == key {
		t.state = root
		return t
	}
	if root.resources == nil {
		root.resources = make(map[string]*agentAutomation)
	}
	state := root.resources[key]
	if state == nil {
		state = &agentAutomation{parent: root, resource: key, paused: root.paused, pages: make(map[string]automationPage)}
		root.resources[key] = state
	}
	t.state = state
	return t
}
func (state *agentAutomation) root() *agentAutomation {
	if state.parent != nil {
		return state.parent
	}
	return state
}
func (state *agentAutomation) group() []*agentAutomation {
	root := state.root()
	root.mu.Lock()
	defer root.mu.Unlock()
	states := []*agentAutomation{root}
	for _, child := range root.resources {
		states = append(states, child)
	}
	return states
}
