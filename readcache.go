package forgeapi

// DefaultReadCacheBytes is what one connection's read cache may hold when no
// [WithReadCache] is given. Measured: a GitHub run page of 100 runs projects to
// about 39 KB, so 32 MiB holds about 800 such pages.
const DefaultReadCacheBytes = 32 << 20

// ReadCacheState is what one connection's read cache has done since its client was
// built, read through [BudgetState.ReadCache].
type ReadCacheState struct {
	// Hits counts reads a held answer served after the instance confirmed it with
	// a not-modified status.
	Hits int
	// Misses counts every other cacheable read the instance answered in full, an
	// error status included.
	Misses int
	// Saved counts the not-modified answers a held answer was served for on a
	// product that bills none for them, each a request the instance did not bill.
	Saved int
	// Bytes is what the cache holds now.
	Bytes int64
}
