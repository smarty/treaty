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
	pipeline    Pipeline
	renderer    MapRenderer
	preferences PreferenceStore
	suites      []TestSuite
	themes      ThemeSource
	vcs         VersionControl
	workspace   Workspace
}

// analysis is everything one run computes, kept in memory only.
type analysis struct {
	config  Config
	head    *graph.Graph
	base    *graph.Graph
	baseRef string

	// dialects are the languages' readers, by id prefix, for what the
	// tools learn from symbols beyond their edges, such as aliases.
	dialects map[string]Dialect

	// previewed marks an analysis under an architecture treaty.yaml does
	// not declare, whose placements no line of the file explains.
	previewed   bool
	changes     []rules.Change
	violations  []rules.Violation
	metrics     map[string]rules.Metric
	baseMetrics map[string]rules.Metric
	findings    []rules.Finding

	// stale are the entries of rules.ignore whose target is gone.
	stale []Ignore

	// baseMisfit is the first directory a snowflake's stars do not fit in
	// the base tree, which its binding looked through.
	baseMisfit string

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

	result, err := this.analyzeGraphs(config, revision{graph: head, tests: this.discover(this.root, head)}, base, baseRef)
	if err != nil {
		return nil, err
	}

	result.baseSources = base.sources
	return result, nil
}

// analyzeGraphs scores a head graph, and its diff from base when base has a
// graph. It assigns layers to both graphs. A snowflake is bound to the head
// strictly, so stars that do not fit the working tree fail the analysis as
// an invalid treaty.yaml does, and to the base leniently, which notes a
// misfit and goes on; the analysis keeps the head's binding for every later
// placement.
func (this *Service) analyzeGraphs(config Config, head, base revision, baseRef string) (*analysis, error) {
	bound, err := config.Architecture.Bind(modulePaths(head.graph))
	if err != nil {
		return nil, fmt.Errorf("treaty.yaml: %w", err)
	}

	config.Architecture = bound
	result := &analysis{config: config, head: head.graph, base: base.graph, baseRef: baseRef, dialects: this.dialects}
	measured := map[string]bool{}
	for _, suite := range this.suites {
		measured[suite.Language()] = true
	}

	config.Architecture.Assign(head.graph)
	result.violations = config.Architecture.Violations(head.graph)
	result.metrics = rules.Metrics(head.graph)
	result.proofs = rules.Proof(head.graph, head.tests, measured, result.metrics)
	if base.graph != nil {
		baseBound, _ := config.Architecture.Bind(modulePaths(base.graph))
		result.baseMisfit = baseBound.Misfit()
		config.Architecture.Assign(base.graph)
		result.baseMetrics = rules.Metrics(base.graph)
		rules.Proof(base.graph, base.tests, measured, result.baseMetrics)
		result.changes = rules.Classify(base.graph, head.graph, this.compatible)
		rules.MarkExisting(result.violations, config.Architecture.Violations(base.graph))
	}

	result.findings, result.stale = applyIgnores(rules.Rank(head.graph, base.graph, result.changes, result.violations), config.Ignores, head.graph)
	return result, nil
}

// modulePaths lists the paths of a graph's modules.
func modulePaths(g *graph.Graph) []string {
	result := make([]string, 0, len(g.Modules))
	for _, module := range g.Modules {
		result = append(result, module.Path)
	}

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

// failures says what fails the check: each kind of finding fail_on lists,
// counted without what the base already had.
func (this *analysis) failures() []string {
	return this.problems(this.config.FailOn)
}

// warnings says what warn_on lists and fail_on does not, counted the same
// way. Warnings never fail the check.
func (this *analysis) warnings() []string {
	var kinds []string
	for _, kind := range this.config.WarnOn {
		if !slices.Contains(this.config.FailOn, kind) {
			kinds = append(kinds, kind)
		}
	}

	return this.problems(kinds)
}

// problems describes, for each of the given kinds of finding, how many the
// change brought: violations and cycle edges the base did not have,
// breaking changes, and modules newly unclassified or claimed by two
// boundaries, leaving out what treaty.yaml ignores. They are counted from
// the findings, so the counts and the findings that fail or warn agree.
func (this *analysis) problems(kinds []string) []string {
	var result []string
	counts := map[string]int{}
	moved := 0
	for _, finding := range this.findings {
		kind := this.counted(finding)
		counts[kind]++
		if kind == rules.FindingBreaking && finding.Kind == rules.FindingMoved {
			moved++
		}
	}

	for _, kind := range kinds {
		count := counts[kind]
		if count == 0 {
			continue
		}

		switch kind {
		case rules.FindingLayerViolation:
			result = append(result, fmt.Sprintf("%d layer violation(s)", count))
		case rules.FindingCycle:
			result = append(result, fmt.Sprintf("%d cycle edge(s) between boundaries", count))
		case rules.FindingUnclassified:
			result = append(result, fmt.Sprintf("%d unclassified module(s)", count))
		case rules.FindingAmbiguous:
			result = append(result, fmt.Sprintf("%d module(s) claimed equally by two or more boundaries", count))
		case rules.FindingImpure:
			result = append(result, fmt.Sprintf("%d impurity(ies) in contracts", count))
		case rules.FindingBreaking:
			result = append(result, breakingText(count-moved, moved))
		}
	}

	return result
}

// breakingText counts the breaking changes, saying how many are moved
// contracts so it matches what the findings list.
func breakingText(changed, moved int) string {
	switch {
	case moved == 0:
		return fmt.Sprintf("%d breaking change(s)", changed)
	case changed == 0:
		return fmt.Sprintf("%d breaking change(s), all moved contracts", moved)
	}

	return fmt.Sprintf("%d breaking change(s): %d changed or removed, %d moved", changed+moved, changed, moved)
}

// counted names the kind of problem a finding counts as, the kind fail_on
// and warn_on name, or empty when it counts as none: a violation or
// unplaced module the base already had, a moved contract that left an
// entry module nothing could import, anything treaty.yaml ignores, and
// every kind that is no problem, such as a new contract.
func (this *analysis) counted(finding rules.Finding) string {
	if finding.Ignored != "" || finding.Existing {
		return ""
	}

	switch finding.Kind {
	case rules.FindingBreaking, rules.FindingImplementers:
		return rules.FindingBreaking
	case rules.FindingMoved:
		for _, change := range this.changes {
			if change.Symbol == finding.Target && change.Kind == rules.ChangeMoved && this.fromEntry(change) {
				return ""
			}
		}

		return rules.FindingBreaking
	case rules.FindingLayerViolation, rules.FindingCycle, rules.FindingUnclassified, rules.FindingAmbiguous, rules.FindingImpure:
		return finding.Kind
	}

	return ""
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
