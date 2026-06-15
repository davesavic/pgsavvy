package highlight

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"

	"github.com/davesavic/pgsavvy/pkg/theme"
)

// TokenKind classifies a SQL token for downstream styling or analysis.
type TokenKind int

const (
	Keyword     TokenKind = iota // SELECT, FROM, WHERE, ...
	String                       // 'hello', $$body$$, ...
	Comment                      // -- line, /* block */
	Number                       // 42, 3.14, ...
	Operator                     // =, <>, +, ...
	Identifier                   // column/table names, aliases
	Punctuation                  // (, ), ;, ., ...
	Other                        // anything that does not fit above
)

// Token is a single lexical unit with its classification and rune
// position within the original input.
type Token struct {
	Type       TokenKind
	Value      string
	RuneOffset int
	RuneLen    int
}

// pgLexer is the Chroma PostgreSQL lexer, resolved once at init time.
var pgLexer chroma.Lexer

func init() {
	pgLexer = lexers.Get("postgresql")
	if pgLexer == nil {
		pgLexer = lexers.Fallback
	}
	pgLexer = chroma.Coalesce(pgLexer)
}

// Highlight returns text with ANSI SGR escape sequences applied based
// on the current theme's syntax-highlight colours. The output always
// ends with a reset sequence (\x1b[0m) so downstream rendering does
// not leak colour state.
//
// An empty input returns "".
func Highlight(text string) string {
	if text == "" {
		return ""
	}
	if theme.IsMonochrome() {
		return text
	}

	iter, err := pgLexer.Tokenise(nil, text)
	if err != nil {
		return text + "\x1b[0m"
	}

	var b strings.Builder
	b.Grow(len(text) * 2) // rough estimate for escape overhead

	for _, tok := range iter.Tokens() {
		sgr := sgrForChromaToken(tok.Type)
		if sgr != "" {
			b.WriteString(sgr)
			b.WriteString(tok.Value)
			b.WriteString("\x1b[0m")
		} else {
			b.WriteString(tok.Value)
		}
	}

	// Guarantee trailing reset.
	b.WriteString("\x1b[0m")
	return b.String()
}

// Tokenize returns a classified token stream with rune offsets
// computed over the input string. A nil or empty input returns nil.
func Tokenize(text string) []Token {
	if text == "" {
		return nil
	}

	iter, err := pgLexer.Tokenise(nil, text)
	if err != nil {
		return nil
	}

	var (
		tokens     []Token
		runeOffset int
	)

	for _, tok := range iter.Tokens() {
		runeLen := utf8.RuneCountInString(tok.Value)
		tokens = append(tokens, Token{
			Type:       mapChromaToken(tok.Type),
			Value:      tok.Value,
			RuneOffset: runeOffset,
			RuneLen:    runeLen,
		})
		runeOffset += runeLen
	}
	return tokens
}

// mapChromaToken converts a Chroma token type to our TokenKind enum.
func mapChromaToken(t chroma.TokenType) TokenKind {
	switch t {
	case chroma.Comment, chroma.CommentSingle,
		chroma.CommentMultiline, chroma.CommentSpecial,
		chroma.CommentPreproc, chroma.CommentPreprocFile:
		return Comment

	case chroma.Keyword, chroma.KeywordConstant,
		chroma.KeywordDeclaration, chroma.KeywordNamespace,
		chroma.KeywordPseudo, chroma.KeywordReserved,
		chroma.KeywordType:
		return Keyword

	case chroma.LiteralString, chroma.LiteralStringSingle,
		chroma.LiteralStringDouble, chroma.LiteralStringAffix,
		chroma.LiteralStringBacktick, chroma.LiteralStringChar,
		chroma.LiteralStringDelimiter, chroma.LiteralStringDoc,
		chroma.LiteralStringEscape, chroma.LiteralStringHeredoc,
		chroma.LiteralStringInterpol, chroma.LiteralStringOther,
		chroma.LiteralStringRegex, chroma.LiteralStringSymbol:
		return String

	case chroma.LiteralNumber, chroma.LiteralNumberBin,
		chroma.LiteralNumberFloat, chroma.LiteralNumberHex,
		chroma.LiteralNumberInteger, chroma.LiteralNumberIntegerLong,
		chroma.LiteralNumberOct:
		return Number

	case chroma.Operator, chroma.OperatorWord:
		return Operator

	case chroma.Name, chroma.NameAttribute,
		chroma.NameBuiltin, chroma.NameBuiltinPseudo,
		chroma.NameClass, chroma.NameConstant,
		chroma.NameDecorator, chroma.NameEntity,
		chroma.NameException, chroma.NameFunction,
		chroma.NameFunctionMagic, chroma.NameLabel,
		chroma.NameNamespace, chroma.NameOther,
		chroma.NameProperty, chroma.NameTag,
		chroma.NameVariable, chroma.NameVariableAnonymous,
		chroma.NameVariableClass, chroma.NameVariableGlobal,
		chroma.NameVariableInstance, chroma.NameVariableMagic:
		return Identifier

	case chroma.Punctuation:
		return Punctuation

	default:
		return Other
	}
}

// sgrForChromaToken returns the ANSI SGR open sequence for a Chroma
// token type based on the active theme, or "" when no styling applies.
func sgrForChromaToken(t chroma.TokenType) string {
	kind := mapChromaToken(t)
	s := styleForKind(kind)
	if s == nil {
		return ""
	}
	return styleToSGR(s)
}

// styleForKind reads the current theme and returns the *Style for the
// given TokenKind. Called per-token so theme hot-reloads are respected.
func styleForKind(k TokenKind) *theme.Style {
	cur := theme.Current()
	switch k {
	case Keyword:
		return cur.KeywordFg
	case String:
		return cur.StringFg
	case Comment:
		return cur.CommentFg
	case Number:
		return cur.NumericFg
	case Operator:
		return cur.OperatorFg
	case Identifier:
		return cur.IdentifierFg
	default:
		return nil
	}
}

// styleToSGR converts a theme.Style to an ANSI SGR open sequence.
// Returns "" when the style carries no visual information.
func styleToSGR(s *theme.Style) string {
	if s == nil {
		return ""
	}
	fg := colorToSGRFg(s.Fg)
	if fg == "" && !s.Bold && !s.Italic && !s.Underline {
		return ""
	}

	var b strings.Builder
	b.WriteString("\x1b[")
	needSep := false

	if s.Bold {
		b.WriteByte('1')
		needSep = true
	}
	if s.Italic {
		if needSep {
			b.WriteByte(';')
		}
		b.WriteByte('3')
		needSep = true
	}
	if s.Underline {
		if needSep {
			b.WriteByte(';')
		}
		b.WriteByte('4')
		needSep = true
	}
	if fg != "" {
		if needSep {
			b.WriteByte(';')
		}
		b.WriteString(fg)
	}
	b.WriteByte('m')
	return b.String()
}

// colorToSGRFg converts a colour string (named colour or #RRGGBB hex)
// to the numeric portion of an ANSI SGR foreground parameter. Returns
// "" for empty or unrecognised values.
func colorToSGRFg(c string) string {
	if c == "" {
		return ""
	}

	// Hex colour: #RGB or #RRGGBB -> true-colour 38;2;R;G;B
	if c[0] == '#' {
		r, g, b, ok := parseHex(c)
		if !ok {
			return ""
		}
		return fmt.Sprintf("38;2;%d;%d;%d", r, g, b)
	}

	// Named ANSI colours.
	switch strings.ToLower(c) {
	case "black":
		return "30"
	case "red":
		return "31"
	case "green":
		return "32"
	case "yellow":
		return "33"
	case "blue":
		return "34"
	case "magenta":
		return "35"
	case "cyan":
		return "36"
	case "white":
		return "37"
	case "gray", "grey":
		return "90" // bright black
	default:
		return ""
	}
}

// parseHex parses #RGB or #RRGGBB into (r, g, b, ok).
func parseHex(s string) (r, g, b uint8, ok bool) {
	s = strings.TrimPrefix(s, "#")
	switch len(s) {
	case 3:
		rv, err1 := strconv.ParseUint(string(s[0])+string(s[0]), 16, 8)
		gv, err2 := strconv.ParseUint(string(s[1])+string(s[1]), 16, 8)
		bv, err3 := strconv.ParseUint(string(s[2])+string(s[2]), 16, 8)
		if err1 != nil || err2 != nil || err3 != nil {
			return 0, 0, 0, false
		}
		return uint8(rv), uint8(gv), uint8(bv), true
	case 6:
		rv, err1 := strconv.ParseUint(s[0:2], 16, 8)
		gv, err2 := strconv.ParseUint(s[2:4], 16, 8)
		bv, err3 := strconv.ParseUint(s[4:6], 16, 8)
		if err1 != nil || err2 != nil || err3 != nil {
			return 0, 0, 0, false
		}
		return uint8(rv), uint8(gv), uint8(bv), true
	default:
		return 0, 0, 0, false
	}
}
