package rounds

// FindLast returns the highest round r in [0, max) for which exists(r) is
// true, assuming rounds are contiguous from 0 (exists is true up to some round
// and false above it). It returns 0 when nothing exists.
func FindLast(exists func(uint64) bool, max uint64) uint64 {
	lo, hi := uint64(0), max // find the first round that does not exist
	for lo < hi {
		mid := lo + (hi-lo)/2
		if exists(mid) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return 0
	}
	return lo - 1
}

// ProbeForward walks up from `from` while consecutive rounds exist, checking
// at most limit rounds, and returns the highest existing round found. It
// repairs a persisted last-round pointer that fell behind the stored data.
func ProbeForward(exists func(uint64) bool, from, limit uint64) uint64 {
	for i := uint64(0); i < limit; i++ {
		if !exists(from + 1) {
			break
		}
		from++
	}
	return from
}
