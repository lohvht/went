package token

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Most of this package is listed and adapted from
// https://golang.org/src/text/template/parse/lex.go and partially taken from
// https://golang.org/src/go/scanner/scanner.go

// Tokenise creates a new scanner for the input string
func Tokenise(name, input string) *Lexer {
	l := &Lexer{
		Name:    name,
		Input:   input,
		tokens:  make(chan Token),
		line:    1,
		col:     0,
		prevCol: 0,
	}
	go l.run()
	return l
}

// Next returns the next Token from the input
// called by the parser, not in the lexing goroutine
func (l *Lexer) Next() Token { return <-l.tokens }

// Drain drains the output so that the lexing goroutine will exit
// Called by the parser, not in lexing goroutine
func (l *Lexer) Drain() {
	for range l.tokens {
	}
}

// Lexer scans the entire input string and tokenises it, storing the tokens in
// a channel of Tokens
type Lexer struct {
	Name   string     // name of the input; used only for error reporting
	Input  string     // string being scanned
	tokens chan Token // channel of the scanned items

	// current state to track & emit info
	line    uint32 // 1 + number of newlines seen
	col     uint32 // 1 + current column number
	prevCol uint32 // previous column number seen (ensure backup() is correct)

	// Internal lexer state
	start        int       // start position of the current token
	pos          int       // current position
	runeWidth    int       // runeWidth of the last rune read from input
	prevTokTyp   Type      // previous Token type used for automatic semicolon insertion
	bracketStack runeStack // a stack of runes used to keep track of all '(', '[' and '{'
}

const eof = -1

type runeStack []rune

func (rs *runeStack) empty() bool {
	return len(*rs) == 0
}

// push a rune to the top of the stack
func (rs *runeStack) push(r rune) {
	*rs = append(*rs, r)
}

// pop removes a rune from the top of the stack, you should always check if
// the stack is empty prior to popping
func (rs *runeStack) pop() (r rune) {
	r, *rs = (*rs)[len(*rs)-1], (*rs)[:len(*rs)-1]
	return
}

// peek looks at the top of the stack you should always check if the stack is
// empty prior to peeking
func (rs *runeStack) peek() rune {
	return (*rs)[len(*rs)-1]
}

// next returns the next rune in the input
// next increases newline count
func (l *Lexer) next() rune {
	if int(l.pos) >= len(l.Input) {
		l.runeWidth = 0
		return eof
	}
	r, w := utf8.DecodeRuneInString(l.Input[l.pos:])
	l.runeWidth = w
	l.pos += l.runeWidth
	// handle columns and lines seen
	if r == '\n' {
		l.line++
		l.col = 1
	} else {
		l.prevCol = l.col
		l.col++
	}
	return r
}

// peek returns but does not consume next rune in the input
func (l *Lexer) peek() rune {
	r := l.next()
	l.backup()
	return r
}

// backup steps back one rune, can only be called once per call of next
func (l *Lexer) backup() {
	l.pos -= l.runeWidth
	l.col = l.prevCol
	if l.runeWidth == 1 && l.Input[l.pos] == '\n' {
		l.line--
	}
}

// emit passes a Token back to the client
// this will also update the last seen emitted Token type
func (l *Lexer) emit(typ Type) {
	l.tokens <- Token{
		typ,
		l.Input[l.start:l.pos],
		newPos(l.line, l.col),
	}
	l.start = l.pos
	l.prevTokTyp = typ
}

// ignore skips over the pending input before this point
func (l *Lexer) ignore() { l.start = l.pos }

// accept consumes the next rune if its from the valid set
func (l *Lexer) accept(valid string) bool {
	if strings.ContainsRune(valid, l.next()) {
		return true
	}
	l.backup()
	return false
}

// acceptRun consumes a run of runes from the valid set
func (l *Lexer) acceptRun(valid string) {
	for strings.ContainsRune(valid, l.next()) {
	}
	l.backup()
}

// errorf emits an error Token and terminates the scan by passing back a nil
// pointer that will be the next state, terminating l.nextToken.
func (l *Lexer) errorf(format string, args ...interface{}) stateFunc {
	l.tokens <- Token{
		ERROR,
		fmt.Sprintf(format, args...),
		newPos(l.line, l.col),
	}
	return nil
}

// run starts the state machine for the Lexer
func (l *Lexer) run() {
	for state := lexCode; state != nil; {
		state = state(l)
	}
	close(l.tokens)
}

// atIdentifierTerminator reports whether the input is at valid
// termination character to appear after an identifier
func (l *Lexer) atIdentifierTerminator() bool {
	r := l.peek()
	if isSpace(r) || isEndOfLine(r) {
		return true
	}
	switch r {
	case
		eof, '=', // EOF character and assignment/declaration ('='), or equality check ('==')
		'.', ',', // DOT ('.') to denote .property, or commas
		'|', '&', // OR ('||'), or AND ('&&')
		'(', ')', '[', ']', '{', '}', // Parenthesis, square, curly and normal
		'+', '-', '/', '*', '%': // Math operator signs, or start of a comment ('//', '/*')
		return true
	}
	return false
}

// State functions

// stateFn represents the state of the scanner as a function that returns the next state
type stateFunc func(*Lexer) stateFunc

var vectoredLexState map[rune]stateFunc

func init() {
	vectoredLexState = map[rune]stateFunc{
		eof: lexEOF, // where lexCode loop terminates
		// Spaces
		' ':  lexSpace,
		'\t': lexSpace,
		'\r': lexSpace,
		'\n': lexNewline,

		// Punctuations
		':': func(l *Lexer) stateFunc { l.emit(COLON); return lexCode },
		';': func(l *Lexer) stateFunc { l.emit(SEMICOLON); return lexCode },
		',': func(l *Lexer) stateFunc { l.emit(COMMA); return lexCode },
		'|': func(l *Lexer) stateFunc {
			r := l.Input[l.start]
			if l.next() == '|' {
				l.emit(LOGICALOR)
			} else {
				l.errorf("expected Token %#U", r)
			}
			return lexCode
		},
		'&': func(l *Lexer) stateFunc {
			r := l.Input[l.start]
			if l.next() == '&' {
				l.emit(LOGICALAND)
			} else {
				l.errorf("expected Token %#U", r)
			}
			return lexCode
		},
		'.': lexDot,

		// quotes
		'\'': lexQuotedString,
		'`':  lexRawString,

		// brackets
		'(': func(l *Lexer) stateFunc { l.emit(LROUND); l.bracketStack.push('('); return lexCode },
		'[': func(l *Lexer) stateFunc { l.emit(LSQUARE); l.bracketStack.push('['); return lexCode },
		'{': func(l *Lexer) stateFunc { l.emit(LCURLY); l.bracketStack.push('{'); return lexCode },
		')': lexRightBracket,
		']': lexRightBracket,
		'}': lexRightBracket,

		// Operators
		'+': lexOperator,
		'-': lexOperator,
		'*': lexOperator,
		'%': lexOperator,
		'=': lexOperator,
		'!': lexOperator,
		'<': lexOperator,
		'>': lexOperator,
		'/': func(l *Lexer) stateFunc { // handle for '/', can be comment or divide sign
			// Special lookahead for '*' or '/', for comment check
			if int(l.pos) < len(l.Input) {
				switch r := l.Input[l.pos]; {
				case r == '/':
					return lexSinglelineComment
				case r == '*':
					return lexMultilineComment
				}
			}
			return lexOperator
		},
	}
	// runes for numbers to the lexState map
	for r := '0'; r <= '9'; r++ {
		vectoredLexState[r] = lexNumber
	}
}

// lexCode scans the main body of the code, recursively returning itself
func lexCode(l *Lexer) stateFunc {
	r := l.next()
	if stfn, ok := vectoredLexState[r]; ok {
		return stfn
	}
	switch {
	case isAlphaNumeric(r):
		l.backup()
		return lexIdentifier
	default:
		return l.errorf("unrecognised character in code: %#U", r)
	}
}

// lexEOF emits the EOF Token and handles the termination of the main lexCode loop
func lexEOF(l *Lexer) stateFunc {
	if !l.bracketStack.empty() {
		r := l.bracketStack.pop()
		return l.errorf("unclosed left bracket: %#U", r)
	}
	l.emit(EOF)
	return nil
}

// lexSpace scans a run of space characters, One space has already been seen
// Ignore spaces seen
func lexSpace(l *Lexer) stateFunc {
	for isSpace(l.peek()) {
		l.next()
	}
	l.ignore()
	return lexCode
}

// lexNewline scans for a run of newline chars ('\n')
// This method also does the automatic semicolon insertions (ASI rule 1) with
// the following rules for newlines:
// 1. the Token is an identifier, or string/boolean/number literal
// 2. the Token is a `break`, `return` or `continue`
// 3. Token closes a round, square, or curly bracket (')', ']', '}')
func lexNewline(l *Lexer) stateFunc {
	l.backup()
Loop:
	for {
		switch r := l.next(); {
		case r == '\n':
			// Absorb and go to next iteration
		default:
			l.backup()
			break Loop
		}
	}
	switch l.prevTokTyp {
	case NAME, STR, FALSE,
		TRUE, INT, FLOAT, BREAK, CONT, RETURN,
		RROUND, RSQUARE, RCURLY:
		l.emit(SEMICOLON)
	default:
		l.ignore() // do not count the spaces as the next() already adds
	}
	return lexCode
}

// lexQuotedString scans a quoted string, can be escaped using the '\' character
func lexQuotedString(l *Lexer) stateFunc {
	l.ignore() // ignore the opening quote
Loop:
	for {
		switch l.next() {
		case '\\': // single '\' character as escape character
			if r := l.next(); r == '\n' || r == eof {
				return l.errorf("unterminated quoted string")
			}
		case '\'':
			l.backup() // move back before the closing quote
			break Loop
		}
	}
	l.emit(STR)
	l.next()
	l.ignore() // now consume and ignore the closing quote
	return lexCode
}

// lexRawString scans a raw string delimited by '`' character
func lexRawString(l *Lexer) stateFunc {
	l.ignore() // ignore the opening quote
	startLine := l.line
	startCol := l.col
Loop:
	for {
		switch l.next() {
		case eof:
			// restore line and col number to the location of the opening quote
			// will error out, okay to overwrite l.line
			l.line = startLine
			l.col = startCol
			return l.errorf("Unterminated raw string")
		case '`':
			l.backup() // move back before the closing quote
			break Loop
		}
	}
	l.emit(STR)
	l.next()
	l.ignore() // now consume and ignore the closing quote
	return lexCode
}

// lexDot scans a dot and determines if its part of the number or a dot
// to access property
func lexDot(l *Lexer) stateFunc {
	// Special lookahead for ".property" so we don't break l.backup()
	if int(l.pos) < len(l.Input) {
		if r := l.Input[l.pos]; r < '0' || r > '9' { // if its not a number
			l.emit(DOT)
			return lexCode // emit the dot '.' and go back to lexCode
		}
	}
	return lexNumber
}

// lexOperator scans for a potential operator
// The first character ('+', '-', '/', '%', '*', '=', '!', '>', '<') has already
// been consumed
func lexOperator(l *Lexer) stateFunc {
	r := l.Input[l.start] // store the 1st character somewhere
	if l.next() != '=' {
		l.backup() // go back to capture 'r' only
		switch r {
		case '+':
			l.emit(PLUS)
		case '-':
			l.emit(MINUS)
		case '/':
			l.emit(DIV)
		case '%':
			l.emit(MOD)
		case '*':
			l.emit(MULT)
		case '=':
			l.emit(ASSIGN)
		case '!':
			l.emit(LOGICALNOT)
		case '>':
			l.emit(GR)
		case '<':
			l.emit(SM)
		}
	} else {
		// capture both r and the equal sign '='
		switch r {
		case '+':
			l.emit(PLUSASSIGN)
		case '-':
			l.emit(MINUSASSIGN)
		case '/':
			l.emit(DIVASSIGN)
		case '%':
			l.emit(MODASSIGN)
		case '*':
			l.emit(MULTASSIGN)
		case '=':
			l.emit(EQ)
		case '!':
			l.emit(NEQ)
		case '>':
			l.emit(GREQ)
		case '<':
			l.emit(SMEQ)
		}
	}
	return lexCode
}

// scanSignificand scans for all numbers (of the given base) up to a non-number
func (l *Lexer) scanSignificand(base int) {
	for digitValue(l.peek()) < base {
		l.next()
	}
}

// lexNumber scans for a number, assumes that the lexer has not consumed the start
// of the number (either number or a dot)
func lexNumber(l *Lexer) stateFunc {
	l.backup() // backup to see the '.' or numerical runes
	emitTyp := INT
	// Seen decimal point --> is a float (i.e. .1234E10 for example)
	if l.peek() == '.' {
		goto FRACTION
	}
	// Leading 0 ==> hexadecimal ("0x"/"0X") or octal 0
	if l.peek() == '0' {
		if l.accept("xX") {
			// hexadecimal int
			l.scanSignificand(16)
			if l.pos-l.start <= 2 {
				// Only scanned "0x" or "0X"
				return l.errorf("illegal hexadecimal number: %q", l.Input[l.start:l.pos])
			}
		} else {
			l.scanSignificand(8)
			if l.accept("89") {
				// error, illegal octal int/float
				l.scanSignificand(10)
				return l.errorf("illegal octal number: %q", l.Input[l.start:l.pos])
			}
			if r := l.peek(); r == '.' || r == 'e' || r == 'E' {
				// NOTE: ".eEi" including imaginary number, if we wanna support it in the future
				// Octal float
				goto FRACTION
			}
		}
		l.emit(emitTyp)
		return lexCode
	}
	// Decimal integer/float
	l.scanSignificand(10)
FRACTION: // handles all other floating point lexing
	if l.accept(".") {
		emitTyp = FLOAT
		l.scanSignificand(10)
	}
	if l.accept("eE") {
		emitTyp = FLOAT
		l.accept("+-")
		if digitValue(l.peek()) < 10 {
			l.scanSignificand(10)
		} else {
			return l.errorf("Illegal floating-point exponent: %q", l.Input[l.start:l.pos])
		}
	}
	l.emit(emitTyp)
	return lexCode
}

// lexIdentifier scans an alphanumeric word
func lexIdentifier(l *Lexer) stateFunc {
Loop:
	for {
		switch r := l.next(); {
		case isAlphaNumeric(r):
			// absorb until no more next alphanumeric characters
		default:
			l.backup()
			word := l.Input[l.start:l.pos]
			if !l.atIdentifierTerminator() {
				return l.errorf("Bad character: %#U", r)
			}
			switch {
			case keywordBegin+1 <= keywords[word] && keywords[word] < keywordEnd:
				l.emit(keywords[word])
			default:
				l.emit(NAME)
			}
			break Loop
		}
	}
	return lexCode
}

var bracketMap = map[rune]rune{
	')': '(',
	']': '[',
	'}': '{',
}

// lexRightBracket scans for a right bracket (curly, round, square)
// This function also runs ASI (Rule 2), a semicolon may be omitted before closing
// the right curly bracket, this allows complex statements to occupy a single line
func lexRightBracket(l *Lexer) stateFunc {
	l.backup()
	r := l.next() // backup to capture r
	if l.bracketStack.empty() {
		return l.errorf("unexpected right bracket %#U", r)
	} else if toCheck := l.bracketStack.pop(); toCheck != bracketMap[r] {
		return l.errorf("unexpected right bracket %#U", r)
	}
	switch r {
	case ')':
		l.emit(RROUND)
	case '}':
		if l.prevTokTyp != SEMICOLON {
			l.backup() // backup to not accidentally emit the right curly bracket
			l.emit(SEMICOLON)
			l.next() // advance forward to contain the right curly bracket again
		}
		l.emit(RCURLY)
	case ']':
		l.emit(RSQUARE)
	}
	return lexCode
}

// lexSinglelineComment scans a single line comment ('//') and discards it
func lexSinglelineComment(l *Lexer) stateFunc {
	for {
		if r := l.next(); isEndOfLine(r) || r == eof {
			break
		}
	}
	l.ignore()
	return lexCode
}

// lexMultilineComment scans for a multiline comment block ('/*', '*/') and discards it
// The left comment marker ('/*') has already been consumed
func lexMultilineComment(l *Lexer) stateFunc {
	if i := strings.Index(l.Input[l.pos:], "*/"); i < 0 {
		return l.errorf("Multiline comment is not closed")
	}
	var left, right rune
	right = l.next()
	for {
		left, right = right, l.next()
		if left == '*' && right == '/' {
			break
		}
	}
	l.ignore()
	return lexCode
}

// Utility Functions

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\r'
}

func isEndOfLine(r rune) bool {
	return r == '\n'
}

func isAlphaNumeric(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func digitValue(ch rune) int {
	switch {
	case '0' <= ch && ch <= '9':
		return int(ch - '0')
	case 'a' <= ch && ch <= 'f':
		return int(ch - 'a' + 10)
	case 'A' <= ch && ch <= 'F':
		return int(ch - 'A' + 10)
	}
	return 16 // larger than any legal digit val
}
