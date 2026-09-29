// Package gitvcs materializes other revisions of a git repository.
package gitvcs

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var (
	ErrRevision = errors.New("a revision may not start with a dash")

	knownFlag = map[string]bool{"--quiet": true, "--short": true, "--verify": true}
)

// Git reads trees out of a repository with the git command.
type Git struct {
	root string
}

// New creates a git adapter for a repository.
//
// Parameters:
//   - root: the repository root.
//
// Returns:
//   - result: the adapter.
func New(root string) *Git {
	return &Git{root: root}
}

// Materialize extracts the tree at ref into a temporary directory with git
// archive, leaving the working tree and index untouched.
//
// Parameters:
//   - ref: any git revision.
//
// Returns:
//   - dir: the temporary directory.
//   - cleanup: removes the directory.
//   - err: git failed or the ref does not exist.
func (this *Git) Materialize(ref string) (dir string, cleanup func(), err error) {
	if strings.HasPrefix(ref, "-") {
		return "", nil, fmt.Errorf("%w: %q", ErrRevision, ref)
	}

	dir, err = os.MkdirTemp("", "treaty-base-")
	if err != nil {
		return "", nil, err
	}

	cleanup = func() { _ = os.RemoveAll(dir) }
	archive := exec.Command("git", "-C", this.root, "archive", "--format=tar", ref)
	extract := exec.Command("tar", "-x", "-C", dir)
	var stderr bytes.Buffer
	archive.Stderr = &stderr
	extract.Stdin, err = archive.StdoutPipe()
	if err != nil {
		cleanup()
		return "", nil, err
	}

	if err := extract.Start(); err != nil {
		cleanup()
		return "", nil, err
	}

	if err := archive.Run(); err != nil {
		_ = extract.Wait()
		cleanup()
		return "", nil, fmt.Errorf("git archive %s: %w: %s", ref, err, bytes.TrimSpace(stderr.Bytes()))
	}

	if err := extract.Wait(); err != nil {
		cleanup()
		return "", nil, err
	}

	return dir, cleanup, nil
}

// DefaultBranch names the branch pull requests merge into: the remote's
// default branch, such as origin/main, or a local main or master.
//
// Returns:
//   - branch: the branch name.
//   - err: no candidate branch exists.
func (this *Git) DefaultBranch() (branch string, err error) {
	if name, err := this.git("symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && name != "" {
		return name, nil
	}

	for _, candidate := range []string{"origin/main", "origin/master", "main", "master"} {
		if _, err := this.Resolve(candidate); err == nil {
			return candidate, nil
		}
	}

	return "", errors.New("no default branch: set a target branch")
}

// MergeBase finds the commit where two revisions diverged.
//
// Parameters:
//   - a: one revision.
//   - b: the other revision.
//
// Returns:
//   - commit: the merge base's id.
//   - err: git failed or the revisions share no history.
func (this *Git) MergeBase(a, b string) (commit string, err error) {
	return this.git("merge-base", a, b)
}

// Resolve turns a revision into a commit id.
//
// Parameters:
//   - ref: any git revision.
//
// Returns:
//   - commit: the full commit id.
//   - err: the revision does not name a commit.
func (this *Git) Resolve(ref string) (commit string, err error) {
	return this.git("rev-parse", "--verify", "--quiet", ref+"^{commit}")
}

func (this *Git) git(args ...string) (string, error) {
	for _, arg := range args[1:] {
		if strings.HasPrefix(arg, "-") && !knownFlag[arg] {
			return "", fmt.Errorf("%w: %q", ErrRevision, arg)
		}
	}

	command := exec.Command("git", append([]string{"-C", this.root}, args...)...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(stderr.Bytes()))
	}

	return strings.TrimSpace(stdout.String()), nil
}
