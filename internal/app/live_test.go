package app

import (
	"errors"
	"testing"
	"time"

	"github.com/smarty/treaty/internal/graph"
)

func TestShowIsRateLimited(t *testing.T) {
	g := graph.New()
	g.AddModule(&graph.Module{ID: "go:core", Language: "go", Path: "core"})
	live := &Live{analysis: &analysis{head: g}, subscribers: map[chan LiveState]bool{}, showEvery: 50 * time.Millisecond}
	updates, cancel := live.Subscribe()
	defer cancel()

	if err := live.Show("go:core", "first"); err != nil {
		t.Fatal(err)
	}

	if pushed := <-updates; pushed.Pointer == nil || pushed.Pointer.Target != "go:core" {
		t.Fatalf("pushed: %+v", pushed)
	}

	if err := live.Show("go:core", "second"); !errors.Is(err, ErrTooSoon) {
		t.Fatalf("second: %v", err)
	}

	time.Sleep(60 * time.Millisecond)
	if err := live.Show("go:core", "third"); err != nil {
		t.Fatalf("after the interval: %v", err)
	}

	if pointer := live.State().Pointer; pointer.Sequence != 2 || pointer.Reason != "third" {
		t.Fatalf("pointer: %+v", pointer)
	}
}

func TestPollEveryAdaptsToSize(t *testing.T) {
	for _, c := range []struct {
		files int
		took  time.Duration
		want  time.Duration
	}{
		{200, 5 * time.Millisecond, 500 * time.Millisecond},
		{LargeRepository - 1, 100 * time.Millisecond, 500 * time.Millisecond},
		{LargeRepository, 100 * time.Millisecond, 5 * time.Second},
		{3000, 400 * time.Millisecond, 5 * time.Second},
	} {
		if got := PollEvery(c.files, c.took); got != c.want {
			t.Errorf("PollEvery(%d, %s) = %s, want %s", c.files, c.took, got, c.want)
		}
	}
}
