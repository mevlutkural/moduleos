package handler

import "testing"

func TestStreamLimiterEnforcesGlobalAndPerApplicationLimits(t *testing.T) {
	limiter := newStreamLimiter(2, 1)
	one, ok := limiter.acquire("one")
	if !ok {
		t.Fatal("first lease rejected")
	}
	if _, ok := limiter.acquire("one"); ok {
		t.Fatal("per-application limit was not enforced")
	}
	two, ok := limiter.acquire("two")
	if !ok {
		t.Fatal("second application rejected")
	}
	if _, ok := limiter.acquire("three"); ok {
		t.Fatal("global limit was not enforced")
	}
	one.Release()
	one.Release()
	three, ok := limiter.acquire("three")
	if !ok {
		t.Fatal("released capacity was not reusable")
	}
	two.Release()
	three.Release()
	active, apps := limiter.snapshot()
	if active != 0 || apps != 0 {
		t.Fatalf("snapshot = %d/%d", active, apps)
	}
}
