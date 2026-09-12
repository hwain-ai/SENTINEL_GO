package crap

// RISK(breaking): Changing this bound changes the cross-language wire contract.
const maximumJSONSafeInteger int64 = 9007199254740991

// CallableKind identifies a callable form supported by the Go analyzer.
type CallableKind string

const (
	Function        CallableKind = "function"
	Method          CallableKind = "method"
	FunctionLiteral CallableKind = "functionLiteral"
)

// SourceRange is a half-open source range. Byte offsets are relative to the
// original UTF-8 source. Line and column values are one-based when present.
type SourceRange struct {
	StartByte   int64
	EndByte     int64
	StartLine   int
	StartColumn int
	EndLine     int
	EndColumn   int
}

// Callable describes one independently scored Go callable.
type Callable struct {
	ModulePath          string
	Kind                CallableKind
	QualifiedName       string
	NormalizedSignature string
	SemanticSite        string
	CallableID          string
	SourceRange         SourceRange
	ExcludedRanges      []SourceRange
	Complexity          int64
}

// ExactCrap contains an exact reduced CRAP score and its canonical rendering.
// A result with UnknownReason set deliberately has no fabricated numeric value.
type ExactCrap struct {
	Numerator     string
	Denominator   string
	Decimal       string
	Pass          bool
	UnknownReason string
}
