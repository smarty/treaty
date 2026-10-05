package app

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestProjectsShareADirectory(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "shop")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(parent, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}

	var opened, closed atomic.Int32
	projects := NewProjects(func(string) (*Live, func(), error) {
		opened.Add(1)
		return NewLive(nil, nil), func() { closed.Add(1) }, nil
	}, 50*time.Millisecond)

	first, joined, leaveFirst, err := projects.Attach(root)
	if err != nil || joined || first.Name != "shop" || first.Slug != "shop" {
		t.Fatalf("first: %+v %t %v", first, joined, err)
	}

	// A second session in the same directory, however it got there, joins.
	second, joined, leaveSecond, err := projects.Attach(link)
	if err != nil || !joined || second != first || opened.Load() != 1 {
		t.Fatalf("second: %+v %t %v, opened %d", second, joined, err, opened.Load())
	}

	if found, err := projects.Find("shop"); err != nil || found != first || projects.Latest() != "shop" {
		t.Fatalf("find: %+v %v", found, err)
	}

	if list := projects.List(); len(list) != 1 || list[0].Sessions != 2 {
		t.Fatalf("list: %+v", list)
	}

	// The project stays open until its last session has been gone a while.
	leaveFirst()
	leaveFirst()
	leaveSecond()
	if _, err := projects.Find("shop"); err != nil {
		t.Fatal("a project outlives its last session by the grace")
	}

	waitUntil(t, func() bool { return closed.Load() == 1 })
	if _, err := projects.Find("shop"); !errors.Is(err, ErrUnknownProject) {
		t.Fatalf("closed project: %v", err)
	}

	// With nothing open, the registry goes idle after the grace.
	select {
	case <-projects.Idle():
	case <-time.After(5 * time.Second):
		t.Fatal("the registry did not go idle")
	}
}

func TestProjectsRejoinWithinTheGrace(t *testing.T) {
	root := t.TempDir()
	var closed atomic.Int32
	projects := NewProjects(func(string) (*Live, func(), error) {
		return NewLive(nil, nil), func() { closed.Add(1) }, nil
	}, 100*time.Millisecond)

	_, _, leave, _ := projects.Attach(root)
	leave()
	_, joined, leave, err := projects.Attach(root)
	if err != nil || !joined {
		t.Fatalf("a session back within the grace rejoins: %t %v", joined, err)
	}

	time.Sleep(300 * time.Millisecond)
	if closed.Load() != 0 {
		t.Fatal("a rejoined project must stay open")
	}

	select {
	case <-projects.Idle():
		t.Fatal("the registry is not idle while a project is open")
	default:
	}

	leave()
}

func TestProjectsNameFoldersApart(t *testing.T) {
	parent := t.TempDir()
	billing, shipping := filepath.Join(parent, "billing", "api"), filepath.Join(parent, "shipping", "api")
	for _, dir := range []string{billing, shipping} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	projects := NewProjects(func(string) (*Live, func(), error) { return NewLive(nil, nil), func() {}, nil }, time.Minute)
	first, _, _, _ := projects.Attach(billing)
	second, _, _, _ := projects.Attach(shipping)
	if first.Name != "api" || first.Slug != "api" || second.Name != filepath.Join("shipping", "api") || second.Slug != "api-2" {
		t.Fatalf("names: %+v %+v", first, second)
	}

	if projects.Latest() != "api-2" {
		t.Fatalf("latest: %s", projects.Latest())
	}
}

func TestProjectsThatFailToOpen(t *testing.T) {
	failure := errors.New("no graph")
	projects := NewProjects(func(string) (*Live, func(), error) { return nil, nil, failure }, time.Minute)
	if _, _, _, err := projects.Attach(t.TempDir()); !errors.Is(err, failure) {
		t.Fatalf("attach: %v", err)
	}

	if list := projects.List(); len(list) != 0 {
		t.Fatalf("a project that failed to open is forgotten: %+v", list)
	}
}

func waitUntil(t *testing.T, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if done() {
			return
		}
	}

	t.Fatal("timed out")
}
