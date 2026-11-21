package sqlsafety

import (
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newCheck(session, database string) *Check {
	return &Check{SessionID: session, Database: database, Risk: RiskLow}
}

func TestTokenSingleUseAndBound(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)}
	tokens := NewTokens(5*time.Minute, clock.now)

	c := newCheck("s1", "app")
	tokens.Issue(c, "hash-1")
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(c.CheckID) {
		t.Fatalf("CheckID %q is not 128-bit hex", c.CheckID)
	}
	if want := clock.t.Add(5 * time.Minute); !c.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want %v", c.ExpiresAt, want)
	}

	for name, args := range map[string][4]string{
		"session":  {c.CheckID, "s2", "app", "hash-1"},
		"database": {c.CheckID, "s1", "other", "hash-1"},
		"hash":     {c.CheckID, "s1", "app", "hash-2"},
		"id":       {"0123456789abcdef0123456789abcdef", "s1", "app", "hash-1"},
	} {
		if _, err := tokens.Consume(args[0], args[1], args[2], args[3]); !errors.Is(err, ErrCheckInvalid) {
			t.Fatalf("mismatched %s: err = %v, want ErrCheckInvalid", name, err)
		}
	}

	got, err := tokens.Consume(c.CheckID, "s1", "app", "hash-1")
	if err != nil || got != c {
		t.Fatalf("first Consume = %v, %v; want the issued check", got, err)
	}
	if _, err := tokens.Consume(c.CheckID, "s1", "app", "hash-1"); !errors.Is(err, ErrCheckInvalid) {
		t.Fatalf("second Consume err = %v, want ErrCheckInvalid", err)
	}

	expiring := newCheck("s1", "app")
	tokens.Issue(expiring, "hash-1")
	clock.t = clock.t.Add(5*time.Minute + time.Second)
	if _, err := tokens.Consume(expiring.CheckID, "s1", "app", "hash-1"); !errors.Is(err, ErrCheckInvalid) {
		t.Fatalf("expired Consume err = %v, want ErrCheckInvalid", err)
	}
}

func TestTokenCapacityDropsOldest(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)}
	tokens := NewTokens(5*time.Minute, clock.now)
	checks := make([]*Check, maxPendingChecks+1)
	for i := range checks {
		checks[i] = newCheck("s", "db")
		tokens.Issue(checks[i], fmt.Sprint(i))
	}
	if _, err := tokens.Consume(checks[0].CheckID, "s", "db", "0"); !errors.Is(err, ErrCheckInvalid) {
		t.Fatalf("oldest token survived overflow: err = %v", err)
	}
	for _, i := range []int{1, maxPendingChecks} {
		if _, err := tokens.Consume(checks[i].CheckID, "s", "db", fmt.Sprint(i)); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}
}

func TestTokenReissueKeepsBinding(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)}
	tokens := NewTokens(5*time.Minute, clock.now)
	c := newCheck("s1", "app")
	tokens.Issue(c, "hash-1")
	got, err := tokens.Consume(c.CheckID, "s1", "app", "hash-1")
	if err != nil {
		t.Fatal(err)
	}
	updated := *got
	updated.Risk = RiskHigh
	clock.t = clock.t.Add(time.Minute)
	if err := tokens.Reissue(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.CheckID == c.CheckID || !updated.ExpiresAt.Equal(clock.t.Add(5*time.Minute)) {
		t.Fatalf("reissued check = %+v", updated)
	}
	if _, err := tokens.Consume(updated.CheckID, "s1", "app", "hash-2"); !errors.Is(err, ErrCheckInvalid) {
		t.Fatalf("reissued token accepted another query: %v", err)
	}
	if again, err := tokens.Consume(updated.CheckID, "s1", "app", "hash-1"); err != nil || again.Risk != RiskHigh {
		t.Fatalf("reissued Consume = %+v, %v", again, err)
	}
	if err := tokens.Reissue(newCheck("s1", "app")); err == nil {
		t.Fatal("Reissue accepted a check that was never issued")
	}
}
