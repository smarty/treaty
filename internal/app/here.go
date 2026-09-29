package app

// HereReport is what treaty here did.
type HereReport struct {
	Registration Registration

	// Configured is false when the repository has no treaty.yaml yet.
	Configured bool
}

// Here registers treaty as an MCP server of the repository, so that coding
// agents started in it get the live graph and the person gets the live map.
//
// Parameters:
//   - replace: overwrite a different treaty entry.
//
// Returns:
//   - result: where the server was registered, and whether the repository
//     has a layer config yet.
//   - err: the agent config could not be read or written, or holds a
//     different treaty entry and replace is false.
func (this *Service) Here(replace bool) (result HereReport, err error) {
	registration, err := this.agents.Register("treaty", MCPServer{Command: "treaty", Args: []string{"mcp"}}, replace)
	if err != nil {
		return HereReport{}, err
	}

	_, configured, err := this.config.Load()
	return HereReport{Registration: registration, Configured: configured}, err
}
