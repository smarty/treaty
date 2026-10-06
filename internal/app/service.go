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
	suites      []TestSuite
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

	// proofs is what the head tree's tests show about each contract.
	proofs map[string]rules.ContractProof

	// baseSources holds the text of the base tree's files, keyed by path,
	// so the map can show how a symbol's code changed.
	baseSources map[string]string
}

// revision is one tree's graph with what is read beside it: the text of its
// files and its tests.
type revision struct {
	graph   *graph.Graph
	sources map[string]string
	tests   []rules.TestUse
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

// UseTestSuites sets the test suites whose tests show what each contract
// proves. Without any, no module is measured.
//
// Parameters:
//   - suites: one suite per language whose tests can be found.
func (this *Service) UseTestSuites(suites ...TestSuite) {
	this.suites = suites
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

	var base revision
	if baseRef != "" {
		if base, err = this.graphAt(baseRef); err != nil {
			return nil, err
		}
	}

	result := this.analyzeGraphs(config, revision{graph: head, tests: this.discover(this.root, head)}, base, baseRef)
	result.baseSources = base.sources
	return result, nil
}

// analyzeGraphs scores a head graph, and its diff from base when base has a
// graph. It assigns layers to both graphs.
func (this *Service) analyzeGraphs(config Config, head, base revision, baseRef string) *analysis {
	result := &analysis{config: config, head: head.graph, base: base.graph, baseRef: baseRef}
	measured := map[string]bool{}
	for _, suite := range this.suites {
		measured[suite.Language()] = true
	}

	config.Architecture.Assign(head.graph)
	result.violations = config.Architecture.Violations(head.graph)
	result.metrics = rules.Metrics(head.graph)
	result.proofs = rules.Proof(head.graph, head.tests, measured, result.metrics)
	if base.graph != nil {
		config.Architecture.Assign(base.graph)
		result.baseMetrics = rules.Metrics(base.graph)
		rules.Proof(base.graph, base.tests, measured, result.baseMetrics)
		result.changes = rules.Classify(base.graph, head.graph, this.compatible)
	}

	result.findings = rules.Rank(head.graph, base.graph, result.changes, result.violations)
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

// discover finds the tests of the tree at root with every test suite. A
// suite that cannot read the tree finds nothing, which leaves its
// contracts without examples rather than failing the analysis.
func (this *Service) discover(root string, g *graph.Graph) (result []rules.TestUse) {
	for _, suite := range this.suites {
		cases, _ := suite.Discover(root, g)
		result = append(result, testUses(cases)...)
	}

	return result
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

// graphAt builds the graph of the tree at a ref, with its tests and the
// text of its files, which are gone once the materialized tree is cleaned
// up. The working tree, at an empty ref, needs neither.
func (this *Service) graphAt(ref string) (result revision, err error) {
	if ref == "" {
		result.graph, err = this.extractor.Extract(this.root)
		return result, err
	}

	dir, cleanup, err := this.vcs.Materialize(ref)
	if err != nil {
		return revision{}, err
	}

	defer cleanup()
	if result.graph, err = this.extractor.Extract(dir); err != nil {
		return revision{}, err
	}

	return revision{graph: result.graph, sources: this.sourcesAt(dir, result.graph), tests: this.discover(dir, result.graph)}, nil
}
