package pdf

import "fmt"

const (
	maxParsedBytes = 32 << 20
	maxParsedNodes = 100_000
)

// materialization bounds allocations over the lifetime of a document, including
// failed parses and objects later evicted or overwritten. It is deliberately
// conservative: accounting is not refunded when memory becomes collectible.
type materialization struct {
	bytes int64
	nodes int
	err   error
}

func (b *materialization) reserve(bytes int64, nodes int) error {
	if b == nil {
		return nil
	}
	if b.err != nil {
		return b.err
	}
	if bytes < 0 || bytes > b.bytes || nodes < 0 || nodes > b.nodes {
		b.err = fmt.Errorf("%w: cumulative parsed objects", ErrTooLarge)
		return b.err
	}
	b.bytes -= bytes
	b.nodes -= nodes
	return nil
}

// appendByte controls capacity growth and charges before allocating. Charging
// the whole new buffer also covers the old buffer during the growth copy.
func (l *lexer) appendByte(out []byte, c byte) ([]byte, error) {
	if len(out) == cap(out) {
		n := max(8, 2*cap(out))
		if err := l.budget.reserve(int64(n), 0); err != nil {
			return nil, err
		}
		next := make([]byte, len(out), n)
		copy(next, out)
		out = next
	}
	return append(out, c), nil
}
