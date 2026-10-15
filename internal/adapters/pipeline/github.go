package pipeline

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/rules"
)

// GitHub publishes to GitHub Actions: each finding that fails or warns as
// an annotation on its line, the report as the job's summary, and the map's
// path as the step output map, for the Treaty action to keep.
type GitHub struct {
	env    func(string) string
	prefix string
	out    io.Writer
}

func (this *GitHub) Name() string { return "GitHub Actions" }

// Target is the pull request's base branch, empty for other events.
func (this *GitHub) Target() string { return this.env("GITHUB_BASE_REF") }

// Publish writes the annotations to the log and appends the summary and
// the outputs to the files GitHub names.
func (this *GitHub) Publish(review app.Review) error {
	for _, finding := range review.Report.Findings {
		level := map[string]string{rules.VerdictFail: "error", rules.VerdictWarn: "warning"}[finding.Verdict]
		if level == "" {
			continue
		}

		var properties []string
		if file := checkout(this.prefix, finding.File); file != "" {
			properties = append(properties, "file="+property(file))
			if finding.Line > 0 {
				properties = append(properties, fmt.Sprintf("line=%d", finding.Line))
			}
		}

		properties = append(properties, "title="+property("Treaty: "+finding.Title))
		if _, err := fmt.Fprintf(this.out, "::%s %s::%s\n", level, strings.Join(properties, ","), message(finding.Detail)); err != nil {
			return err
		}
	}

	mapPath := checkout(this.prefix, review.Map)
	summary := review.Report.Markdown() + "\nThe map of this change is in the `treaty-map` artifact: open `map.html` in a browser.\n"
	if err := appendTo(this.env("GITHUB_STEP_SUMMARY"), summary); err != nil {
		return err
	}

	result := "pass"
	if len(review.Report.Failures) > 0 {
		result = "fail"
	}

	return appendTo(this.env("GITHUB_OUTPUT"), fmt.Sprintf("map=%s\nresult=%s\n", mapPath, result))
}

// appendTo appends text to the file GitHub named, when it named one.
func appendTo(name, text string) error {
	if name == "" {
		return nil
	}

	file, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}

	if _, err := io.WriteString(file, text); err != nil {
		_ = file.Close()
		return err
	}

	return file.Close()
}

// message escapes a workflow command's message.
func message(text string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(text)
}

// property escapes a workflow command's property value.
func property(text string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(text)
}
