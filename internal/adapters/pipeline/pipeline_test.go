package pipeline

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/smarty/treaty/internal/app"
	"github.com/smarty/treaty/internal/rules"
)

// review fails on a layer violation and warns of an unclassified module;
// the new contract neither fails nor warns.
func review() app.Review {
	return app.Review{
		Target: "origin/master",
		Map:    ".treaty/out/map.html",
		Report: app.Report{
			Failures: []string{"1 layer violation(s)"},
			Warnings: []string{"1 unclassified module(s)"},
			Notes:    []string{"compared with 1234567, the merge base of HEAD and origin/master"},
			Findings: []rules.Finding{
				{Severity: rules.SeverityHigh, Kind: rules.FindingLayerViolation, Title: "store → web breaks a layer rule", Detail: "store may not use web,\nfirst at 100%", File: "store/db.go", Line: 5, Verdict: rules.VerdictFail},
				{Severity: rules.SeverityMedium, Kind: rules.FindingUnclassified, Title: "go:tools matches no layer", Detail: "No glob places it.", File: "tools/tools.go", Verdict: rules.VerdictWarn},
				{Severity: rules.SeverityMedium, Kind: rules.FindingNewContract, Title: "New contract go:web:Page", File: "web/web.go", Line: 3},
			},
		},
	}
}

func environment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestDetect(t *testing.T) {
	prefix := func() string { return "services/api/" }
	cases := map[string]struct {
		env  map[string]string
		name string
	}{
		"github":    {map[string]string{"GITHUB_ACTIONS": "true"}, "GitHub Actions"},
		"bitbucket": {map[string]string{"BITBUCKET_BUILD_NUMBER": "7"}, "Bitbucket Pipelines"},
		"neither":   {map[string]string{}, ""},
	}

	for name, test := range cases {
		if got := Detect(environment(test.env), prefix, io.Discard).Name(); got != test.name {
			t.Errorf("%s: %q", name, got)
		}
	}
}

func TestGitHubAnnotatesAndSummarizes(t *testing.T) {
	dir := t.TempDir()
	summary, output := filepath.Join(dir, "summary.md"), filepath.Join(dir, "output")
	var log bytes.Buffer
	github := Detect(environment(map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_BASE_REF": "master", "GITHUB_STEP_SUMMARY": summary, "GITHUB_OUTPUT": output,
	}), func() string { return "services/api/" }, &log)

	if github.Target() != "master" {
		t.Fatalf("target %q", github.Target())
	}

	if err := github.Publish(review()); err != nil {
		t.Fatal(err)
	}

	expected := "::error file=services/api/store/db.go,line=5,title=Treaty%3A store → web breaks a layer rule::store may not use web,%0Afirst at 100%25\n" +
		"::warning file=services/api/tools/tools.go,title=Treaty%3A go%3Atools matches no layer::No glob places it.\n"
	if log.String() != expected {
		t.Fatalf("annotations:\n%s\nwant:\n%s", log.String(), expected)
	}

	text, _ := os.ReadFile(summary)
	if !strings.Contains(string(text), "### Treaty: ❌ fails") || !strings.Contains(string(text), "treaty-map") {
		t.Fatalf("summary:\n%s", text)
	}

	text, _ = os.ReadFile(output)
	if string(text) != "map=services/api/.treaty/out/map.html\nresult=fail\n" {
		t.Fatalf("outputs:\n%s", text)
	}
}

func TestBitbucketReports(t *testing.T) {
	type call struct {
		method, path string
		body         any
	}

	var mutex sync.Mutex
	var calls []call
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body any
		_ = json.NewDecoder(request.Body).Decode(&body)
		mutex.Lock()
		calls = append(calls, call{request.Method, request.URL.Path, body})
		mutex.Unlock()
		if request.Method == http.MethodDelete {
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	bitbucket := &Bitbucket{
		env: environment(map[string]string{
			"BITBUCKET_WORKSPACE": "smartystreets", "BITBUCKET_REPO_SLUG": "token-models", "BITBUCKET_COMMIT": "abc123",
			"BITBUCKET_BUILD_NUMBER": "9", "BITBUCKET_GIT_HTTP_ORIGIN": "http://bitbucket.org/smartystreets/token-models",
			"BITBUCKET_PR_DESTINATION_BRANCH": "master",
		}),
		client: server.Client(),
		api:    server.URL + "/2.0",
	}

	if bitbucket.Target() != "master" {
		t.Fatalf("target %q", bitbucket.Target())
	}

	if err := bitbucket.Publish(review()); err != nil {
		t.Fatal(err)
	}

	report := "/2.0/repositories/smartystreets/token-models/commit/abc123/reports/treaty"
	if len(calls) != 3 || calls[0].method != http.MethodDelete || calls[0].path != report || calls[1].method != http.MethodPut || calls[1].path != report || calls[2].method != http.MethodPost || calls[2].path != report+"/annotations" {
		t.Fatalf("calls: %+v", calls)
	}

	sent := calls[1].body.(map[string]any)
	if sent["result"] != "FAILED" || sent["link"] != "http://bitbucket.org/smartystreets/token-models/pipelines/results/9" || !strings.Contains(sent["details"].(string), "Fails: 1 layer violation(s)") {
		t.Fatalf("report: %+v", sent)
	}

	annotations := calls[2].body.([]any)
	if len(annotations) != 2 {
		t.Fatalf("annotations: %+v", annotations)
	}

	first, second := annotations[0].(map[string]any), annotations[1].(map[string]any)
	if first["path"] != "store/db.go" || first["line"] != 5.0 || first["severity"] != "HIGH" || first["result"] != "FAILED" || first["annotation_type"] != "BUG" {
		t.Fatalf("failing annotation: %+v", first)
	}

	if second["path"] != "tools/tools.go" || second["line"] != nil || second["severity"] != "MEDIUM" || second["result"] != nil {
		t.Fatalf("warning annotation: %+v", second)
	}
}

func TestBitbucketSaysWhatItRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPut {
			http.Error(writer, `{"error": {"message": "bad report"}}`, http.StatusBadRequest)
		}
	}))
	defer server.Close()

	bitbucket := &Bitbucket{env: environment(nil), client: server.Client(), api: server.URL}
	if err := bitbucket.Publish(review()); err == nil || !strings.Contains(err.Error(), "bad report") {
		t.Fatalf("error: %v", err)
	}
}
