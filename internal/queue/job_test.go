package queue

import (
	"testing"
	"time"
)

func TestRetryPolicyDelay(t *testing.T) {
	p := DefaultRetryPolicy()
	if p.MaxAttempts != 5 || p.BaseDelay != 2*time.Second || p.MaxDelay != 60*time.Second {
		t.Fatalf("policy default tidak sesuai keputusan A9: %+v", p)
	}
	cases := map[int]time.Duration{
		0:  2 * time.Second, // attempt belum tercatat diperlakukan seperti attempt 1
		1:  2 * time.Second,
		2:  4 * time.Second,
		3:  8 * time.Second,
		5:  32 * time.Second,
		6:  60 * time.Second,
		40: 60 * time.Second, // tidak overflow
	}
	for attempt, want := range cases {
		if got := p.Delay(attempt); got != want {
			t.Errorf("Delay(%d) = %v, seharusnya %v", attempt, got, want)
		}
	}
	if got := (RetryPolicy{}).Delay(3); got != 0 {
		t.Errorf("policy nol harus tanpa jeda, dapat %v", got)
	}
}

func TestStatusRules(t *testing.T) {
	holds := map[Status]bool{
		StatusQueued: false, StatusRunning: true, StatusSucceeded: false, StatusFailed: false,
		StatusUncertain: true, StatusPartial: true, StatusPartiallyReverted: true,
	}
	for s, want := range holds {
		if s.HoldsDatabase() != want {
			t.Errorf("%s.HoldsDatabase() = %v", s, !want)
		}
		if !s.Valid() {
			t.Errorf("%s harus valid", s)
		}
	}
	if Status("done").Valid() {
		t.Error("status tidak dikenal tidak boleh valid")
	}
	for _, s := range []Status{StatusSucceeded, StatusFailed} {
		if !s.Terminal() {
			t.Errorf("%s harus terminal", s)
		}
	}
	for _, s := range []Status{StatusQueued, StatusRunning, StatusUncertain, StatusPartial, StatusPartiallyReverted} {
		if s.Terminal() {
			t.Errorf("%s tidak boleh terminal", s)
		}
	}
}
