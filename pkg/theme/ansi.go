package theme

// AnsiReset is the ANSI SGR sequence that clears all attributes. Pair it with
// the escape returned by ColorSGR to bound a tinted span.
const AnsiReset = "\x1b[0m"
