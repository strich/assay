package budget

import "testing"

func TestAccountantRecordsAndSums(t *testing.T) {
	a := New()
	if snap := a.Snapshot(); snap.KnownUSD || snap.Calls != 0 {
		t.Fatalf("empty snapshot = %+v", snap)
	}
	c1 := 0.25
	a.Record(&c1, Tokens{Input: 10, Output: 5})
	a.Record(nil, Tokens{Input: 3, CacheRead: 7})
	snap := a.Snapshot()
	// One unpriced call makes the total incomplete, so it is not "known".
	if snap.KnownUSD || snap.CostUSD != 0.25 || snap.Calls != 2 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.Tokens.Input != 13 || snap.Tokens.Output != 5 || snap.Tokens.CacheRead != 7 {
		t.Fatalf("tokens = %+v", snap.Tokens)
	}

	// All calls priced: the total is complete.
	b := New()
	b.Record(&c1, Tokens{Input: 1})
	if snap := b.Snapshot(); !snap.KnownUSD {
		t.Fatalf("all-priced snapshot = %+v, want KnownUSD", snap)
	}
}

func TestNilAccountantIsSafe(t *testing.T) {
	var a *Accountant
	a.Record(nil, Tokens{})
	if a.CostUSD() != 0 || a.Calls() != 0 {
		t.Fatal("nil accountant must be a no-op")
	}
}
