package pdf

import (
	"context"
	"fmt"
	"io"
	"strconv"
)

const (
	maxString = 16 << 20 // bytes in one string
	maxToken  = 4 << 10  // bytes in one name, number or keyword
	maxItems  = 100_000  // items in one array or dictionary
	maxDepth  = 64       // nested arrays and dictionaries
	window    = 32 << 10 // bytes read at a time
)

type tokKind int

const (
	tEOF tokKind = iota
	tNumber
	tString
	tName
	tKeyword
	tArrayOpen
	tArrayClose
	tDictOpen
	tDictClose
)

type token struct {
	kind tokKind
	num  Object // int64 or float64
	b    []byte // a string's bytes, or a name's or keyword's text
	pos  int64
}

// lexer reads tokens and objects from r, starting at pos.
type lexer struct {
	r      io.ReaderAt
	size   int64
	buf    []byte
	bufOff int64
	pos    int64
	back   []token
	ctx    context.Context
	budget *materialization
	err    error
}

func newLexer(r io.ReaderAt, size, pos int64) *lexer {
	return &lexer{r: r, size: size, pos: pos, ctx: context.Background()}
}

func (l *lexer) status() error {
	if l.err != nil {
		return l.err
	}
	if l.budget != nil && l.budget.err != nil {
		return l.budget.err
	}
	return l.ctx.Err()
}

func (l *lexer) peek() (byte, bool) {
	if l.err != nil {
		return 0, false
	}
	if l.pos%4096 == 0 {
		if l.err = l.ctx.Err(); l.err != nil {
			return 0, false
		}
	}
	if l.pos >= l.size || l.pos < 0 {
		return 0, false
	}
	if l.pos < l.bufOff || l.pos >= l.bufOff+int64(len(l.buf)) {
		n := min(window, l.size-l.pos)
		if int64(cap(l.buf)) < n {
			l.buf = make([]byte, n)
		}
		l.buf = l.buf[:n]
		m, _ := l.r.ReadAt(l.buf, l.pos)
		l.buf, l.bufOff = l.buf[:m], l.pos
		if m == 0 {
			return 0, false
		}
	}
	return l.buf[l.pos-l.bufOff], true
}

func (l *lexer) read() (byte, bool) {
	b, ok := l.peek()
	if ok {
		l.pos++
	}
	return b, ok
}

func isSpace(b byte) bool {
	return b == 0 || b == '\t' || b == '\n' || b == '\f' || b == '\r' || b == ' '
}

func isDelim(b byte) bool {
	switch b {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func (l *lexer) unread(t token) { l.back = append(l.back, t) }

func (l *lexer) next() (token, error) {
	if err := l.status(); err != nil {
		return token{}, err
	}
	if n := len(l.back); n > 0 {
		t := l.back[n-1]
		l.back = l.back[:n-1]
		return t, nil
	}
	for {
		b, ok := l.peek()
		if !ok {
			return token{kind: tEOF, pos: l.pos}, l.status()
		}
		if isSpace(b) {
			l.pos++
			continue
		}
		if b == '%' {
			for {
				if c, ok := l.read(); !ok || c == '\n' || c == '\r' {
					break
				}
			}
			continue
		}
		break
	}
	start := l.pos
	b, _ := l.read()
	switch b {
	case '(':
		s, err := l.literal()
		return token{kind: tString, b: s, pos: start}, err
	case '<':
		if c, _ := l.peek(); c == '<' {
			l.pos++
			return token{kind: tDictOpen, pos: start}, nil
		}
		s, err := l.hex()
		return token{kind: tString, b: s, pos: start}, err
	case '>':
		if c, _ := l.peek(); c == '>' {
			l.pos++
			return token{kind: tDictClose, pos: start}, nil
		}
		return token{}, fmt.Errorf("%w: stray > at %d", errSyntax, start)
	case '[':
		return token{kind: tArrayOpen, pos: start}, nil
	case ']':
		return token{kind: tArrayClose, pos: start}, nil
	case '{', '}':
		return token{kind: tKeyword, b: []byte{b}, pos: start}, nil
	case ')':
		return token{}, fmt.Errorf("%w: stray ) at %d", errSyntax, start)
	case '/':
		name, err := l.name()
		return token{kind: tName, b: name, pos: start}, err
	}
	word, err := l.appendByte(nil, b)
	if err != nil {
		return token{}, err
	}
	for {
		c, ok := l.peek()
		if !ok || isSpace(c) || isDelim(c) {
			break
		}
		if len(word) == maxToken {
			return token{}, fmt.Errorf("%w: a token longer than %d bytes", ErrTooLarge, maxToken)
		}
		word, err = l.appendByte(word, c)
		if err != nil {
			return token{}, err
		}
		l.pos++
	}
	if n, ok := number(word); ok {
		return token{kind: tNumber, num: n, pos: start}, nil
	}
	return token{kind: tKeyword, b: word, pos: start}, nil
}

// number parses a PDF number; integers too large for int64 become floats.
func number(w []byte) (Object, bool) {
	digits, dot := 0, false
	for i, c := range w {
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c == '.' && !dot:
			dot = true
		case (c == '+' || c == '-') && i == 0:
		default:
			return nil, false
		}
	}
	if digits == 0 {
		return nil, false
	}
	if !dot {
		if i, err := strconv.ParseInt(string(w), 10, 64); err == nil {
			return i, true
		}
	}
	f, err := strconv.ParseFloat(string(w), 64)
	if err != nil && f == 0 {
		return nil, false
	}
	return f, true
}

// literal reads a (string) after its opening parenthesis.
func (l *lexer) literal() ([]byte, error) {
	var out []byte
	depth := 1
	for {
		if len(out) > maxString {
			return nil, fmt.Errorf("%w: a string longer than %d bytes", ErrTooLarge, maxString)
		}
		c, ok := l.read()
		if !ok {
			if err := l.status(); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("%w: unterminated string", errSyntax)
		}
		switch c {
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return out, nil
			}
		case '\r': // an end of line in a string is a line feed
			if n, _ := l.peek(); n == '\n' {
				l.pos++
			}
			c = '\n'
		case '\\':
			e, ok := l.read()
			if !ok {
				return nil, fmt.Errorf("%w: unterminated string", errSyntax)
			}
			switch e {
			case 'n':
				c = '\n'
			case 'r':
				c = '\r'
			case 't':
				c = '\t'
			case 'b':
				c = '\b'
			case 'f':
				c = '\f'
			case '\r':
				if n, _ := l.peek(); n == '\n' {
					l.pos++
				}
				continue
			case '\n':
				continue
			case '0', '1', '2', '3', '4', '5', '6', '7':
				v := int(e - '0')
				for range 2 {
					d, ok := l.peek()
					if !ok || d < '0' || d > '7' {
						break
					}
					v = v*8 + int(d-'0')
					l.pos++
				}
				c = byte(v) //nolint:gosec // high bits of an octal escape are dropped, as readers do
			default:
				c = e
			}
		}
		var err error
		out, err = l.appendByte(out, c)
		if err != nil {
			return nil, err
		}
	}
}

// hex reads a <hex string> after its opening bracket.
func (l *lexer) hex() ([]byte, error) {
	var out []byte
	var hi byte
	half := false
	for {
		c, ok := l.read()
		if !ok {
			if err := l.status(); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("%w: unterminated hex string", errSyntax)
		}
		var v byte
		switch {
		case c == '>':
			if half {
				var err error
				out, err = l.appendByte(out, hi<<4)
				if err != nil {
					return nil, err
				}
			}
			return out, nil
		case isSpace(c):
			continue
		case c >= '0' && c <= '9':
			v = c - '0'
		case c >= 'a' && c <= 'f':
			v = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v = c - 'A' + 10
		default:
			return nil, fmt.Errorf("%w: %q in a hex string", errSyntax, c)
		}
		if half {
			if len(out) == maxString {
				return nil, fmt.Errorf("%w: a string longer than %d bytes", ErrTooLarge, maxString)
			}
			var err error
			out, err = l.appendByte(out, hi<<4|v)
			if err != nil {
				return nil, err
			}
		} else {
			hi = v
		}
		half = !half
	}
}

// name reads a /Name after its slash, decoding #xx escapes.
func (l *lexer) name() ([]byte, error) {
	var out []byte
	for {
		c, ok := l.peek()
		if !ok || isSpace(c) || isDelim(c) {
			return out, nil
		}
		l.pos++
		if c == '#' {
			h, ok1 := l.read()
			lo, ok2 := l.read()
			v, err := strconv.ParseUint(string([]byte{h, lo}), 16, 8)
			if !ok1 || !ok2 || err != nil {
				return nil, fmt.Errorf("%w: a bad # escape in a name", errSyntax)
			}
			c = byte(v)
		}
		if len(out) == maxToken {
			return nil, fmt.Errorf("%w: a name longer than %d bytes", ErrTooLarge, maxToken)
		}
		var err error
		out, err = l.appendByte(out, c)
		if err != nil {
			return nil, err
		}
	}
}

// object parses one object; depth counts the arrays and dictionaries around it.
func (l *lexer) object(depth int) (Object, error) {
	t, err := l.next()
	if err != nil {
		return nil, err
	}
	if err := l.budget.reserve(32, 1); err != nil {
		return nil, err
	}
	switch t.kind {
	case tEOF:
		return nil, fmt.Errorf("%w: unexpected end of data", errSyntax)
	case tNumber:
		if i, ok := t.num.(int64); ok && i >= 0 && i <= 1<<31 {
			if ref, ok := l.ref(int(i)); ok {
				return ref, nil
			}
		}
		return t.num, nil
	case tString:
		return String(t.b), nil
	case tName:
		if err := l.budget.reserve(int64(len(t.b)), 0); err != nil {
			return nil, err
		}
		return Name(t.b), nil
	case tKeyword:
		switch string(t.b) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		case "null":
			return nil, nil
		}
		if err := l.budget.reserve(int64(len(t.b)), 0); err != nil {
			return nil, err
		}
		return keyword(t.b), nil
	case tArrayOpen:
		if depth >= maxDepth {
			return nil, fmt.Errorf("%w: nested deeper than %d", errSyntax, maxDepth)
		}
		arr := Array{}
		for {
			t, err := l.next()
			if err != nil {
				return nil, err
			}
			if t.kind == tArrayClose {
				return arr, nil
			}
			if len(arr) == maxItems {
				return nil, fmt.Errorf("%w: an array of more than %d items", ErrTooLarge, maxItems)
			}
			l.unread(t)
			v, err := l.object(depth + 1)
			if err != nil {
				return nil, err
			}
			if len(arr) == cap(arr) {
				n := max(8, 2*cap(arr))
				if err := l.budget.reserve(int64(n)*16, 0); err != nil {
					return nil, err
				}
				next := make(Array, len(arr), n)
				copy(next, arr)
				arr = next
			}
			arr = append(arr, v)
		}
	case tDictOpen:
		if depth >= maxDepth {
			return nil, fmt.Errorf("%w: nested deeper than %d", errSyntax, maxDepth)
		}
		if err := l.budget.reserve(64, 0); err != nil {
			return nil, err
		}
		d := Dict{}
		for {
			t, err := l.next()
			if err != nil {
				return nil, err
			}
			if t.kind == tDictClose {
				return d, nil
			}
			if t.kind != tName {
				return nil, fmt.Errorf("%w: a dictionary key at %d isn't a name", errSyntax, t.pos)
			}
			if len(d) == maxItems {
				return nil, fmt.Errorf("%w: a dictionary of more than %d entries", ErrTooLarge, maxItems)
			}
			v, err := l.object(depth + 1)
			if err != nil {
				return nil, err
			}
			if err := l.budget.reserve(128+int64(len(t.b)), 0); err != nil {
				return nil, err
			}
			d[Name(t.b)] = v
		}
	}
	return nil, fmt.Errorf("%w: unexpected token at %d", errSyntax, t.pos)
}

// ref completes "num gen R" when the next two tokens are a generation and R.
func (l *lexer) ref(num int) (Ref, bool) {
	t2, err := l.next()
	if err != nil {
		return Ref{}, false
	}
	if g, ok := t2.num.(int64); ok && t2.kind == tNumber && g >= 0 && g <= 65535 {
		t3, err := l.next()
		if err == nil && t3.kind == tKeyword && string(t3.b) == "R" {
			return Ref{Num: num, Gen: int(g)}, true
		}
		if err == nil {
			l.unread(t3)
		}
	}
	l.unread(t2)
	return Ref{}, false
}

// indirect parses "num gen obj ... endobj" at the lexer's position. A stream's
// Length is left at -1 for the caller to settle.
func (l *lexer) indirect() (int, Object, error) {
	t1, err1 := l.next()
	t2, err2 := l.next()
	t3, err3 := l.next()
	num, ok1 := t1.num.(int64)
	_, ok2 := t2.num.(int64)
	if err1 != nil || err2 != nil || err3 != nil || !ok1 || !ok2 || t3.kind != tKeyword || string(t3.b) != "obj" || num < 0 || num > 1<<31 {
		return 0, nil, fmt.Errorf("%w: no object header at %d", errSyntax, t1.pos)
	}
	obj, err := l.object(0)
	if err != nil {
		return 0, nil, err
	}
	t, err := l.next()
	if err != nil || t.kind != tKeyword || string(t.b) != "stream" {
		return int(num), obj, nil // endobj, or a missing one
	}
	d, ok := obj.(Dict)
	if !ok {
		return 0, nil, fmt.Errorf("%w: a stream without a dictionary", errSyntax)
	}
	switch c, _ := l.peek(); c {
	case '\r':
		l.pos++
		if c, _ := l.peek(); c == '\n' {
			l.pos++
		}
	case '\n':
		l.pos++
	}
	return int(num), Stream{Dict: d, Offset: l.pos, Length: -1}, nil
}
