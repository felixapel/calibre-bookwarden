package pdf

import "bytes"

// maxOperands bounds the operands kept before an operator.
const maxOperands = 64

// ContentHasText reports whether a content stream shows text: a non-empty
// string operand of Tj, ', " or TJ. Inline image data is skipped, and syntax
// errors are stepped over, as viewers do.
func ContentHasText(b []byte) bool {
	l := newLexer(bytes.NewReader(b), int64(len(b)), 0)
	var operands []Object
	for {
		start := l.pos
		o, err := l.object(0)
		if err != nil {
			if l.pos >= l.size {
				return false
			}
			if l.pos == start {
				l.pos++
			}
			l.back, operands = nil, operands[:0]
			continue
		}
		op, ok := o.(keyword)
		if !ok {
			if len(operands) < maxOperands {
				operands = append(operands, o)
			}
			continue
		}
		var last Object
		if len(operands) > 0 {
			last = operands[len(operands)-1]
		}
		switch op {
		case "Tj", "'", `"`:
			if s, ok := last.(String); ok && len(s) > 0 {
				return true
			}
		case "TJ":
			if a, ok := last.(Array); ok {
				for _, v := range a {
					if s, ok := v.(String); ok && len(s) > 0 {
						return true
					}
				}
			}
		case "ID":
			l.skipInlineImage()
		}
		operands = operands[:0]
	}
}

// skipInlineImage skips an inline image's data, up to the EI that follows
// white space and precedes white space, a delimiter or the end.
func (l *lexer) skipInlineImage() {
	if c, ok := l.peek(); ok && isSpace(c) {
		l.pos++
	}
	for {
		c, ok := l.read()
		if !ok {
			return
		}
		if !isSpace(c) {
			continue
		}
		if e, _ := l.peek(); e != 'E' {
			continue
		}
		l.pos++
		if i, _ := l.peek(); i != 'I' {
			continue
		}
		l.pos++
		if n, ok := l.peek(); !ok || isSpace(n) || isDelim(n) {
			return
		}
	}
}
