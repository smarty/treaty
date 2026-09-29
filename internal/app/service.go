package app

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/smarty/treaty/internal/graph"
	"github.com/smarty/treaty/internal/rules"
)

var ErrUnknownLanguage = errors.New("unknown language")

// Service runs every Treaty use case against one repository.
type Service struct {
	agents    AgentConfig
	root      string
	config    ConfigSource
	dialects  map[string]Dialect
	extractor SourceExtractor
	renderer  MapRenderer
	vcs       VersionControl
	workspace Workspace
}

// analysis is everything one run computes, kept in memory only.
type analysis struct {
	config      Config
	head        *graph.Graph
	base        *graph.Graph
	baseRef     string
	changes     []rules.Change
	violations  []rules.Violation
	metrics     map[string]rules.Metric
	baseMetrics map[string]rules.Metric
	findings    []rules.Finding
}

// NewService wires the use cases to their adapters.
//
// Parameters:
//   - root: the repository root.
//   - config: the layer config source.
//   - extractor: the source extractor.
//   - dialects: one dialect per supported language.
//   - vcs: access to other revisions.
//   - workspace: the .treaty directory.
//   - renderer: the map renderer.
//   - agents: where coding agents find the repository's MCP servers.
//
// Returns:
//   - result: the service.
func NewService(root string, config ConfigSource, extractor SourceExtractor, dialects []Dialect, vcs VersionControl, workspace Workspace, renderer MapRenderer, agents AgentConfig) *Service {
	byLanguage := map[string]Dialect{}
	for _, dialect := range dialects {
		byLanguage[dialect.Language()] = dialect
	}

	return &Service{
		agents:    agents,
		root:      root,
		config:    config,
		dialects:  byLanguage,
		extractor: extractor,
		renderer:  renderer,
		vcs:       vcs,
		workspace: workspace,
	}
}

func (this *Service) analyze(baseRef string) (*analysis, error) {
	config, _, err := this.config.Load()
	if err != nil {
		return nil, err
	}

	head, err := this.extractor.Extract(this.root)
	if err != nil {
		return nil, err
	}

	var base *graph.Graph
	if baseRef != "" {
		if base, err = this.graphAt(baseRef); err != nil {
			return nil, err
		}
	}

	return this.analyzeGraphs(config, head, base, baseRef), nil
}

// analyzeGraphs scores a head graph, and its diff from base when base is
// not nil. It assigns layers to both graphs.
func (this *Service) analyzeGraphs(config Config, head, base *graph.Graph, baseRef string) *analysis {
	result := &analysis{config: config, head: head, base: base, baseRef: baseRef}
	config.Layers.Assign(head)
	result.violations = rules.Violations(head)
	result.metrics = rules.Metrics(head)
	if base != nil {
		config.Layers.Assign(base)
		result.baseMetrics = rules.Metrics(base)
		result.changes = rules.Classify(base, head, this.compatible)
	}

	result.findings = rules.Rank(head, base, result.changes, result.violations)
	return result
}

func (this *Service) compatible(before, after *graph.Symbol) bool {
	module, _ := graph.SplitSymbolID(after.ID)
	language, _, _ := strings.Cut(module, ":")
	if dialect, ok := this.dialects[language]; ok {
		return dialect.Compatible(before, after)
	}

	return rules.DefaultCompatible(before, after)
}

func (this *Service) dialect(language string) (Dialect, error) {
	dialect, ok := this.dialects[language]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownLanguage, language)
	}

	return dialect, nil
}

func (this *Service) graphAt(ref string) (*graph.Graph, error) {
	if ref == "" {
		return this.extractor.Extract(this.root)
	}

	dir, cleanup, err := this.vcs.Materialize(ref)
	if err != nil {
		return nil, err
	}

	defer cleanup()
	return this.extractor.Extract(dir)
}

func (this *analysis) failures() []string {
	var result []string
	if slices.Contains(this.config.FailOn, rules.FindingLayerViolation) && len(this.violations) > 0 {
		result = append(result, fmt.Sprintf("%d layer violation(s)", len(this.violations)))
	}

	if slices.Contains(this.config.FailOn, rules.FindingBreaking) {
		count := 0
		for _, change := range this.changes {
			if change.Kind == rules.ChangeBreaking || change.Kind == rules.ChangeRemoved || change.Kind == rules.ChangeMoved {
				count++
			}
		}

		if count > 0 {
			result = append(result, fmt.Sprintf("%d breaking change(s)", count))
		}
	}

	return result
}
