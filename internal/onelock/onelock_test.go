package onelock

import (
	"errors"
	"testing"
)

func TestOneAtATime(t *testing.T) {
	dir := t.TempDir()
	a, err := Take("orosdrive-test", dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Take("orosdrive-test", dir); !errors.Is(err, ErrHeld) {
		t.Fatalf("second take: %v, want ErrHeld", err)
	}
	a.Release()
	b, err := Take("orosdrive-test", dir)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	b.Release()
}
