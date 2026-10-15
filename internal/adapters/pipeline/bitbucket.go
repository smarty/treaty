package pipeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/rules"
)

const (
	// reportID names Treaty's Code Insights report on each commit; a new
	// build replaces it.
	reportID = "treaty"

	// Bitbucket's limits: annotations per request and per report, and the
	// length of a report's details, an annotation's summary and its details.
	annotationBatch = 100
	maxAnnotations  = 1000
	maxDetails      = 2000
	maxSummary      = 450
)

var ErrBitbucket = errors.New("bitbucket refused the report")

// Bitbucket publishes to Bitbucket Pipelines as a Code Insights report on
// the built commit, which a pull request shows with each finding that
// fails or warns annotated on its line.
type Bitbucket struct {
	env    func(string) string
	prefix string
	client *http.Client
	api    string
}

func (this *Bitbucket) Name() string { return "Bitbucket Pipelines" }

// Target is the pull request's destination branch, empty for builds of a
// branch alone.
func (this *Bitbucket) Target() string { return this.env("BITBUCKET_PR_DESTINATION_BRANCH") }

type insightsReport struct {
	Title      string         `json:"title"`
	Details    string         `json:"details"`
	ReportType string         `json:"report_type"`
	Reporter   string         `json:"reporter"`
	Link       string         `json:"link,omitempty"`
	Result     string         `json:"result"`
	Data       []insightDatum `json:"data"`
}

type insightDatum struct {
	Title string `json:"title"`
	Type  string `json:"type"`
	Value any    `json:"value"`
}

type annotation struct {
	ExternalID string `json:"external_id"`
	Type       string `json:"annotation_type"`
	Summary    string `json:"summary"`
	Details    string `json:"details,omitempty"`
	Path       string `json:"path,omitempty"`
	Line       int    `json:"line,omitempty"`
	Severity   string `json:"severity"`
	Result     string `json:"result,omitempty"`
}

// Publish replaces the commit's Treaty report and adds its annotations.
func (this *Bitbucket) Publish(review app.Review) error {
	report := review.Report
	reportURL := fmt.Sprintf("%s/repositories/%s/%s/commit/%s/reports/%s", this.api, this.env("BITBUCKET_WORKSPACE"), this.env("BITBUCKET_REPO_SLUG"), this.env("BITBUCKET_COMMIT"), reportID)

	// Deleting first drops the annotations of an earlier build of the
	// same commit, which replacing the report would keep.
	if err := this.send(http.MethodDelete, reportURL, nil, http.StatusNotFound); err != nil {
		return err
	}

	failures, warnings := 0, 0
	var annotations []annotation
	for _, finding := range report.Findings {
		item := annotation{
			ExternalID: fmt.Sprintf("treaty-%d", len(annotations)+1),
			Type:       "CODE_SMELL",
			Summary:    clip(finding.Title, maxSummary),
			Details:    clip(finding.Detail, maxDetails),
			Path:       checkout(this.prefix, finding.File),
			Line:       finding.Line,
		}

		switch finding.Verdict {
		case rules.VerdictFail:
			failures++
			item.Type, item.Severity, item.Result = "BUG", "HIGH", "FAILED"
		case rules.VerdictWarn:
			warnings++
			item.Severity = "MEDIUM"
		default:
			continue
		}

		if item.Path == "" {
			item.Line = 0
		}

		annotations = append(annotations, item)
	}

	result := "PASSED"
	if len(report.Failures) > 0 {
		result = "FAILED"
	}

	body := insightsReport{
		Title: "Treaty", Details: clip(details(review), maxDetails), ReportType: "BUG", Reporter: "Treaty", Result: result,
		Data: []insightDatum{
			{Title: "Failing findings", Type: "NUMBER", Value: failures},
			{Title: "Warnings", Type: "NUMBER", Value: warnings},
			{Title: "Compared with", Type: "TEXT", Value: review.Target},
		},
	}

	if build := this.env("BITBUCKET_BUILD_NUMBER"); build != "" && this.env("BITBUCKET_GIT_HTTP_ORIGIN") != "" {
		body.Link = fmt.Sprintf("%s/pipelines/results/%s", this.env("BITBUCKET_GIT_HTTP_ORIGIN"), build)
	}

	if err := this.send(http.MethodPut, reportURL, body); err != nil {
		return err
	}

	if len(annotations) > maxAnnotations {
		annotations = annotations[:maxAnnotations]
	}

	for start := 0; start < len(annotations); start += annotationBatch {
		end := min(start+annotationBatch, len(annotations))
		if err := this.send(http.MethodPost, reportURL+"/annotations", annotations[start:end]); err != nil {
			return err
		}
	}

	return nil
}

// details summarizes the review in the report's plain text.
func details(review app.Review) string {
	var lines []string
	if len(review.Report.Failures) > 0 {
		lines = append(lines, "Fails: "+strings.Join(review.Report.Failures, "; "))
	} else {
		lines = append(lines, "Passes.")
	}

	if len(review.Report.Warnings) > 0 {
		lines = append(lines, "Warns: "+strings.Join(review.Report.Warnings, "; "))
	}

	lines = append(lines, review.Report.Notes...)
	lines = append(lines, "The map of this change is the build's artifact "+review.Map+".")
	return strings.Join(lines, "\n")
}

// send makes one call to the API, with body as JSON when it is not nil. A
// status other than 2xx fails unless it is one of allowed.
func (this *Bitbucket) send(method, url string, body any, allowed ...int) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}

		reader = bytes.NewReader(data)
	}

	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		return err
	}

	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := this.client.Do(request)
	if err != nil {
		return err
	}

	defer func() { _ = response.Body.Close() }()
	text, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
	if response.StatusCode/100 == 2 {
		return nil
	}

	for _, status := range allowed {
		if response.StatusCode == status {
			return nil
		}
	}

	return fmt.Errorf("%w: %s %s: %s: %s", ErrBitbucket, method, url, response.Status, strings.TrimSpace(string(text)))
}

// clip shortens text to at most limit characters.
func clip(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}

	return string(runes[:limit-1]) + "…"
}
