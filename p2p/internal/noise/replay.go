package noise

// replayWindow remembers the last windowBits counters in a ring bitmap
// (RFC 6479); within the window packets may arrive in any order.
const windowBits = 2048

type replayWindow struct {
	next uint64 // highest accepted counter + 1
	bits [windowBits / 64]uint64
}

func (w *replayWindow) check(n uint64) bool {
	if n >= w.next {
		return true
	}
	if w.next-n > windowBits {
		return false
	}
	i := n % windowBits
	return w.bits[i/64]&(1<<(i%64)) == 0
}

// update marks n accepted; call only after check(n).
func (w *replayWindow) update(n uint64) {
	if n >= w.next {
		w.advance(n)
	}
	i := n % windowBits
	w.bits[i/64] |= 1 << (i % 64)
}

// advance moves the window's top to n, clearing the ring positions that
// held counters now out of the window.
func (w *replayWindow) advance(n uint64) {
	if n-w.next+1 >= windowBits {
		w.bits = [windowBits / 64]uint64{}
	} else {
		for c := w.next; c <= n; c++ {
			i := c % windowBits
			w.bits[i/64] &^= 1 << (i % 64)
		}
	}
	w.next = n + 1
}
