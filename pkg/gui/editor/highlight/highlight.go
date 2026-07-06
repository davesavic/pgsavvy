package highlight

import (
	"log/slog"
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

// jsonLexer is the Chroma JSON lexer, resolved once at init time.
var jsonLexer chroma.Lexer

func init() {
	pgLexer = lexers.Get("postgresql")
	if pgLexer == nil {
		pgLexer = lexers.Fallback
	}
	pgLexer = chroma.Coalesce(pgLexer)

	jsonLexer = lexers.Get("json")
	if jsonLexer == nil {
		slog.Warn("highlight: json lexer not found, falling back to chroma.Fallback")
		jsonLexer = lexers.Fallback
	}
	jsonLexer = chroma.Coalesce(jsonLexer)
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

// HighlightJSON returns text with ANSI SGR escape sequences applied based
// on the current theme's syntax-highlight colours, using the Chroma JSON
// lexer. The output always ends with a reset sequence (\x1b[0m) so
// downstream rendering does not leak colour state.
//
// Inputs larger than 1 MiB are returned unhighlighted (with a trailing
// reset), and an empty input returns "".
func HighlightJSON(text string) string {
	if text == "" {
		return ""
	}
	if theme.IsMonochrome() {
		return text
	}
	if len(text) > 1<<20 {
		return text + "\x1b[0m"
	}

	iter, err := jsonLexer.Tokenise(nil, text)
	if err != nil {
		return text + "\x1b[0m"
	}

	var b strings.Builder
	b.Grow(len(text) * 2)

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
		return cur.Keyword
	case String:
		return cur.String
	case Comment:
		return cur.Comment
	case Number:
		return cur.Numeric
	case Operator:
		return cur.Operator
	case Identifier:
		return cur.Identifier
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
	fg := theme.ColorParamSGR(s.Fg, theme.Fg)
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
