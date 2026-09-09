// Package budget is assay's spend accountant. Cost and token figures come back
// from opencode per invocation; the accountant tallies them so the pipeline can
// enforce a ceiling on MEASURED spend rather than an estimate, and report the
// actuals either way. A cap chosen without measured spend is a guess.
package budget

import "sync"

// Tokens is the token accounting opencode reports per invocation.
type Tokens struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
}

// Add returns the element-wise sum of a and b.
func (t Tokens) Add(b Tokens) Tokens {
	return Tokens{
		Input:      t.Input + b.Input,
		Output:     t.Output + b.Output,
		CacheRead:  t.CacheRead + b.CacheRead,
		CacheWrite: t.CacheWrite + b.CacheWrite,
	}
}

// Snapshot is a point-in-time view of the accountant.
type Snapshot struct {
	CostUSD  float64 `json:"cost_usd"`
	Tokens   Tokens  `json:"tokens"`
	Calls    int     `json:"calls"`
	KnownUSD bool    `json:"cost_known"`
}

// Accountant accumulates measured cost and tokens across every LLM invocation
// in a run, including failed schema-retry attempts. Safe for concurrent use:
// reviewers, meta-selectors and adversary batches all fan out on goroutines.
type Accountant struct {
	mu          sync.Mutex
	costUSD     float64
	knownUSD    bool
	unknownCost bool
	tokens      Tokens
	calls       int
}

// New returns an empty accountant.
func New() *Accountant { return &Accountant{} }

// Record adds one invocation's measured usage. cost may be nil, meaning the
// provider did not report a cost for that call — distinct from a reported
// $0.00. Once any call is unpriced the total is incomplete, and Snapshot
// reports KnownUSD=false so the total is never presented as a full measure.
func (a *Accountant) Record(cost *float64, tokens Tokens) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if cost != nil {
		a.costUSD += *cost
		a.knownUSD = true
	} else {
		a.unknownCost = true
	}
	a.tokens = a.tokens.Add(tokens)
}

// CostUSD returns the measured spend so far (0 when unknown).
func (a *Accountant) CostUSD() float64 {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.costUSD
}

// Tokens returns the measured token usage so far.
func (a *Accountant) Tokens() Tokens {
	if a == nil {
		return Tokens{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tokens
}

// Calls returns the number of recorded invocations.
func (a *Accountant) Calls() int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// Snapshot returns all counters atomically.
func (a *Accountant) Snapshot() Snapshot {
	if a == nil {
		return Snapshot{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return Snapshot{CostUSD: a.costUSD, Tokens: a.tokens, Calls: a.calls, KnownUSD: a.knownUSD && !a.unknownCost}
}
