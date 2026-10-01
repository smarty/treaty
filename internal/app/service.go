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
	agents      AgentConfig
	root        string
	config      ConfigSource
	dialects    map[string]Dialect
	extractor   SourceExtractor
	renderer    MapRenderer
	preferences PreferenceStore
	themes      ThemeSource
	vcs         VersionControl
	workspace   Workspace
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
//   - themes: the map's color themes.
//   - preferences: where a person's choices on the map are kept.
//
// Returns:
//   - result: the service.
func NewService(root string, config ConfigSource, extractor SourceExtractor, dialects []Dialect, vcs VersionControl, workspace Workspace, renderer MapRenderer, agents AgentConfig, themes ThemeSource, preferences PreferenceStore) *Service {
	byLanguage := map[string]Dialect{}
	for _, dialect := range dialects {
		byLanguage[dialect.Language()] = dialect
	}

	return &Service{
		agents:      agents,
		root:        root,
		config:      config,
		dialects:    byLanguage,
		extractor:   extractor,
		renderer:    renderer,
		themes:      themes,
		preferences: preferences,
		vcs:         vcs,
		workspace:   workspace,
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
	config.Architecture.Assign(head)
	result.violations = config.Architecture.Violations(head)
	result.metrics = rules.Metrics(head)
	if base != nil {
		config.Architecture.Assign(base)
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

// fromEntry reports whether a change moved a symbol out of an entry module,
// which nothing could import, so the move breaks no one.
func (this *analysis) fromEntry(change rules.Change) bool {
	moduleID, _ := graph.SplitSymbolID(change.From)
	module := this.base.Module(moduleID)
	return module != nil && module.Entry
}

func (this *analysis) failures() []string {
	var result []string
	counts := map[string]int{}
	for _, violation := range this.violations {
		counts[violation.Kind]++
	}

	if slices.Contains(this.config.FailOn, rules.FindingLayerViolation) && counts[rules.FindingLayerViolation] > 0 {
		result = append(result, fmt.Sprintf("%d layer violation(s)", counts[rules.FindingLayerViolation]))
	}

	if slices.Contains(this.config.FailOn, rules.FindingCycle) && counts[rules.FindingCycle] > 0 {
		result = append(result, fmt.Sprintf("%d cycle edge(s) between contexts", counts[rules.FindingCycle]))
	}

	if slices.Contains(this.config.FailOn, rules.FindingBreaking) {
		changed, moved := 0, 0
		for _, change := range this.changes {
			switch {
			case change.Kind == rules.ChangeBreaking || change.Kind == rules.ChangeRemoved:
				changed++
			case change.Kind == rules.ChangeMoved && !this.fromEntry(change):
				moved++
			}
		}

		// A moved contract breaks its importers like a removed one, so it
		// counts, and the count says how many of each so it matches what
		// the findings list.
		switch {
		case moved == 0 && changed > 0:
			result = append(result, fmt.Sprintf("%d breaking change(s)", changed))
		case moved > 0 && changed == 0:
			result = append(result, fmt.Sprintf("%d breaking change(s), all moved contracts", moved))
		case moved > 0:
			result = append(result, fmt.Sprintf("%d breaking change(s): %d changed or removed, %d moved", changed+moved, changed, moved))
		}
	}

	return result
}
