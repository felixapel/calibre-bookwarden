package store

// ClopperPearson is the two-sided 95 % Clopper-Pearson interval for a share
// seen as k of n. It sits beside LowerBound, which is the one-sided 5 % bound
// the labels use, and solves the same way, by bisection on binomialTail, with
// 2.5 % in each tail. ok is false for n = 0.
func ClopperPearson(n, k int) (lower, upper float64, ok bool) {
	if n <= 0 || k < 0 || k > n {
		return 0, 0, false
	}
	const tail = 0.025
	lower, upper = 0, 1
	if k > 0 {
		// The share whose chance of k or more successes is 2.5 %: the tail
		// of k-1 or fewer falls as the share grows.
		lo, hi := 0.0, 1.0
		for range 100 {
			mid := (lo + hi) / 2
			if binomialTail(n, k-1, mid) > 1-tail {
				lo = mid
			} else {
				hi = mid
			}
		}
		lower = (lo + hi) / 2
	}
	if k < n {
		// The share whose chance of k or fewer successes is 2.5 %.
		lo, hi := 0.0, 1.0
		for range 100 {
			mid := (lo + hi) / 2
			if binomialTail(n, k, mid) > tail {
				lo = mid
			} else {
				hi = mid
			}
		}
		upper = (lo + hi) / 2
	}
	return lower, upper, true
}
