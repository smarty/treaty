// Package pipeline is the driven adapter for continuous integration
// services: it shows a pull request's check where its reviewers look, as
// GitHub annotations and a job summary, or as a Bitbucket Code Insights
// report.
package pipeline

import (
	"io"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/smarty/treaty/internal/app"
)

// Detect finds the service the build runs in from its environment.
//
// Parameters:
//   - env: reads an environment variable, such as os.Getenv.
//   - prefix: finds the repository directory's path within its git
//     checkout, such as services/api/, since services name files from the
//     checkout; it is called only in a service.
//   - out: the build's log, where GitHub reads annotations.
//
// Returns:
//   - result: GitHub Actions, Bitbucket Pipelines, or a pipeline that
//     publishes nothing outside either.
func Detect(env func(string) string, prefix func() string, out io.Writer) app.Pipeline {
	switch {
	case env("GITHUB_ACTIONS") == "true":
		return &GitHub{env: env, prefix: prefix(), out: out}
	case env("BITBUCKET_BUILD_NUMBER") != "":
		// Inside a Pipelines step, this proxy authenticates calls to the
		// Bitbucket API for the repository being built, so no token is
		// needed.
		proxy, _ := url.Parse("http://localhost:29418")
		client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
		return &Bitbucket{env: env, prefix: prefix(), client: client, api: "http://api.bitbucket.org/2.0"}
	}

	return none{}
}

// none is no service: a review run on a person's machine prints its
// report and publishes nothing.
type none struct{}

func (none) Name() string             { return "" }
func (none) Target() string           { return "" }
func (none) Publish(app.Review) error { return nil }

// checkout names a file from the checkout's root.
func checkout(prefix, file string) string {
	if file == "" {
		return ""
	}

	return path.Join(prefix, file)
}
