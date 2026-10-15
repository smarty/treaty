package app

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/smarty/treaty/internal/rules"
)

var (
	ErrNoTarget    = errors.New("the target branch is not in this clone")
	ErrNoMergeBase = errors.New("no merge base")
)

// Pipeline is the continuous integration service a check runs in, which
// shows the check on the pull request it builds.
type Pipeline interface {
	// Name names the service, such as GitHub Actions, or is empty outside
	// one.
	Name() string

	// Target names the branch the pull request merges into, or is empty
	// when the build is not for a pull request.
	Target() string

	// Publish shows the review where the pull request's reviewers see it.
	Publish(review Review) error
}

// Review is a pull request's check: its report, the branch it merges into
// and the static map of the change, a path relative to the repository.
type Review struct {
	Report Report
	Target string
	Map    string
}

// UsePipeline sets the continuous integration service Review publishes to.
//
// Parameters:
//   - pipeline: the service the build runs in.
func (this *Service) UsePipeline(pipeline Pipeline) {
	this.pipeline = pipeline
}

// PullRequestBase finds the commit a pull request into target is compared
// with: the merge base of HEAD and the target, so that every commit on the
// branch counts and nothing the target gained since does.
//
// Parameters:
//   - target: the branch the pull request merges into, preferably its
//     remote branch, such as origin/master for master; empty for the
//     default branch.
//
// Returns:
//   - commit: the merge base.
//   - ref: the target as resolved, such as origin/master.
//   - err: the target or the merge base is not in this clone.
//
// Errors:
//   - ErrNoTarget: no ref names the target; a clone of one branch lacks it.
//   - ErrNoMergeBase: HEAD and the target share no history here, as in a
//     shallow clone.
func (this *Service) PullRequestBase(target string) (commit, ref string, err error) {
	if this.vcs == nil {
		return "", "", ErrNoTarget
	}

	if target == "" {
		if target, err = this.vcs.DefaultBranch(); err != nil {
			return "", "", fmt.Errorf("%w: %w", ErrNoTarget, err)
		}
	}

	for _, candidate := range []string{"origin/" + strings.TrimPrefix(target, "origin/"), target} {
		if _, err := this.vcs.Resolve(candidate); err == nil {
			ref = candidate
			break
		}
	}

	if ref == "" {
		branch := strings.TrimPrefix(target, "origin/")
		return "", "", fmt.Errorf("%w: %s; fetch it with git fetch origin %s:refs/remotes/origin/%s", ErrNoTarget, target, branch, branch)
	}

	commit, err = this.vcs.MergeBase("HEAD", ref)
	if err != nil {
		return "", "", fmt.Errorf("%w: HEAD and %s share no history in this clone; fetch the whole history (git fetch --unshallow)", ErrNoMergeBase, ref)
	}

	return commit, ref, nil
}

// Review checks a pull request against its merge base with the target,
// draws its static map, and publishes both to the pipeline.
//
// Parameters:
//   - target: the branch the pull request merges into; empty for the
//     pipeline's target, else the default branch.
//
// Returns:
//   - result: the review; result.Report.Failures is non-empty when the
//     check fails.
//   - err: the tree, config or base could not be read, or publishing failed.
func (this *Service) Review(target string) (result Review, err error) {
	if target == "" && this.pipeline != nil {
		target = this.pipeline.Target()
	}

	commit, ref, err := this.PullRequestBase(target)
	if err != nil {
		return Review{}, err
	}

	analysis, err := this.analyze(commit)
	if err != nil {
		return Review{}, err
	}

	result.Target = ref
	result.Report = this.checkReport(commit, analysis)
	result.Report.Notes = append([]string{fmt.Sprintf("compared with %s, the merge base of HEAD and %s", short(commit), ref)}, result.Report.Notes...)
	path, err := this.renderMap(analysis, nil)
	if err != nil {
		return Review{}, err
	}

	if result.Map, err = filepath.Rel(this.root, path); err != nil {
		result.Map = path
	}

	if this.pipeline != nil {
		if err := this.pipeline.Publish(result); err != nil {
			return result, fmt.Errorf("publishing to %s: %w", this.pipeline.Name(), err)
		}
	}

	return result, nil
}

// maxMarkdownFindings bounds each table Markdown writes, so a large
// refactor still fits a pull request's summary.
const maxMarkdownFindings = 40

// Markdown renders the report for a pull request: the verdict, what fails
// and warns, and the notes; a table of the findings that fail or warn; and,
// folded away, the other high and medium findings, with low ones counted.
//
// Returns:
//   - result: the Markdown.
func (this Report) Markdown() string {
	var builder strings.Builder
	if len(this.Failures) > 0 {
		builder.WriteString("### Treaty: ❌ fails\n\n")
		fmt.Fprintf(&builder, "**Fails:** %s  \n", strings.Join(this.Failures, "; "))
	} else {
		builder.WriteString("### Treaty: ✅ passes\n\n")
	}

	if len(this.Warnings) > 0 {
		fmt.Fprintf(&builder, "**Warns:** %s  \n", strings.Join(this.Warnings, "; "))
	}

	for _, note := range this.Notes {
		fmt.Fprintf(&builder, "Note: %s  \n", note)
	}

	var judged, rest []rules.Finding
	low := 0
	for _, finding := range this.Findings {
		switch {
		case finding.Verdict != "":
			judged = append(judged, finding)
		case finding.Severity == rules.SeverityLow:
			low++
		default:
			rest = append(rest, finding)
		}
	}

	if len(judged) > 0 {
		builder.WriteString("\n| | Finding | Where |\n|---|---|---|\n")
		writeRows(&builder, judged, func(finding rules.Finding) string {
			return map[string]string{rules.VerdictFail: "❌ | ", rules.VerdictWarn: "⚠️ | "}[finding.Verdict]
		})
	}

	if len(rest) > 0 {
		fmt.Fprintf(&builder, "\n<details><summary>%d other finding(s), such as new contracts and modules</summary>\n\n| Finding | Where |\n|---|---|\n", len(rest))
		writeRows(&builder, rest, func(rules.Finding) string { return "" })
		builder.WriteString("\n</details>\n")
	}

	if low > 0 {
		fmt.Fprintf(&builder, "\n%d low finding(s), such as compatible or implementation-only changes, are not listed.\n", low)
	}

	return builder.String()
}

// writeRows writes one table row per finding, after the cells lead gives
// it, up to maxMarkdownFindings.
func writeRows(builder *strings.Builder, findings []rules.Finding, lead func(rules.Finding) string) {
	for i, finding := range findings {
		if i == maxMarkdownFindings {
			fmt.Fprintf(builder, "\n…and %d more; treaty check lists them all.\n", len(findings)-i)
			return
		}

		where := ""
		if finding.File != "" {
			where = "`" + finding.File + "`"
			if finding.Line > 0 {
				where = fmt.Sprintf("`%s:%d`", finding.File, finding.Line)
			}
		}

		fmt.Fprintf(builder, "| %s**%s**<br>%s | %s |\n", lead(finding), cell(finding.Title), cell(finding.Detail), where)
	}
}

// cell makes text safe in one cell of a Markdown table.
func cell(text string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ", "\r", "", "<", "&lt;", ">", "&gt;").Replace(text)
}
