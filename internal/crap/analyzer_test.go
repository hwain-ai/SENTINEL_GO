package crap

import (
	"bytes"
	"testing"
)

func TestAnalyzeFindsFunctionsMethodsAndFunctionLiterals(t *testing.T) {
	source := []byte(`package sample

type Widget struct{}

func Plain(x int) int {
	if x > 0 && x < 10 {
		return x
	}
	return 0
}

func (widget *Widget) Run() {}

func Outer() {
	inner := func(value int) int {
		if value > 0 {
			return value
		}
		return 0
	}
	_ = inner
}
`)
	original := bytes.Clone(source)

	rows, err := AnalyzeSource(source, "internal/sample/callables.go")
	if err != nil {
		t.Fatalf("AnalyzeSource returned error: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("len(rows) = %d, want 4: %#v", len(rows), rows)
	}

	want := []struct {
		kind       CallableKind
		name       string
		complexity int64
	}{
		{Function, "Plain", 3},
		{Method, "(*Widget).Run", 1},
		{Function, "Outer", 1},
		{FunctionLiteral, "Outer.<literal:inner>", 2},
	}
	seenIDs := map[string]bool{}
	for index, expected := range want {
		row := rows[index]
		if row.Kind != expected.kind || row.QualifiedName != expected.name || row.Complexity != expected.complexity {
			t.Fatalf("rows[%d] = %#v, want kind=%q name=%q complexity=%d", index, row, expected.kind, expected.name, expected.complexity)
		}
		if row.ModulePath != "internal/sample/callables.go" || row.CallableID == "" {
			t.Fatalf("rows[%d] has incomplete identity: %#v", index, row)
		}
		if seenIDs[row.CallableID] {
			t.Fatalf("duplicate callable ID %q", row.CallableID)
		}
		seenIDs[row.CallableID] = true
		if row.SourceRange.StartByte < 0 || row.SourceRange.EndByte > int64(len(source)) || row.SourceRange.StartByte >= row.SourceRange.EndByte {
			t.Fatalf("rows[%d] has invalid byte range: %#v", index, row.SourceRange)
		}
	}

	if !bytes.Equal(source, original) {
		t.Fatal("analysis modified source bytes")
	}
}

func TestAnalyzeCountsRobertMartinGoDecisionSyntax(t *testing.T) {
	source := []byte(`package sample

func Decisions(value int, input <-chan int) int {
	if value > 0 || value < -10 { value++ }
	for value < 3 { value++ }
	for range []int{1} { value++ }
	switch value {
	case 1: value++
	default: value--
	}
select {
case <-input: value++
default: value--
}
return value
}
`)
	rows, err := AnalyzeSource(source, "decisions.go")
	if err != nil {
		t.Fatalf("AnalyzeSource returned error: %v", err)
	}
	if len(rows) != 1 || rows[0].Complexity != 9 {
		t.Fatalf("rows = %#v, want one callable with complexity 9", rows)
	}
}

func TestAnalyzeCanonicalizesVariadicParameterSignature(t *testing.T) {
	rows, err := AnalyzeSource([]byte("package sample\nfunc Join(values ...string) {}\n"), "join.go")
	if err != nil {
		t.Fatalf("AnalyzeSource rejected a variadic function: %v", err)
	}
	if len(rows) != 1 || rows[0].NormalizedSignature != "func(...string)" {
		t.Fatalf("rows = %#v, want canonical func(...string)", rows)
	}
}

func TestAnalyzeRejectsNonCanonicalInput(t *testing.T) {
	cases := []struct {
		name       string
		source     []byte
		modulePath string
	}{
		{"dot segment", []byte("package p\nfunc F() {}\n"), "./file.go"},
		{"parent segment", []byte("package p\nfunc F() {}\n"), "../file.go"},
		{"backslash", []byte("package p\nfunc F() {}\n"), `dir\file.go`},
		{"invalid UTF-8", []byte{0xff}, "file.go"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := AnalyzeSource(testCase.source, testCase.modulePath); err == nil {
				t.Fatal("AnalyzeSource succeeded, want error")
			}
		})
	}
}

func TestAnalyzeAcceptsLeadingBOMAndIncludesItsRawBytesInOffsets(t *testing.T) {
	plainSource := []byte("package p\nfunc F() {}\n")
	plainRows, err := AnalyzeSource(plainSource, "file.go")
	if err != nil {
		t.Fatalf("AnalyzeSource without BOM returned error: %v", err)
	}
	withBOM := append([]byte{0xef, 0xbb, 0xbf}, plainSource...)
	bomRows, err := AnalyzeSource(withBOM, "file.go")
	if err != nil {
		t.Fatalf("AnalyzeSource with BOM returned error: %v", err)
	}
	if len(plainRows) != 1 || len(bomRows) != 1 {
		t.Fatalf("unexpected callable rows: plain=%#v BOM=%#v", plainRows, bomRows)
	}
	if bomRows[0].SourceRange.StartByte != plainRows[0].SourceRange.StartByte+3 {
		t.Fatalf("BOM start byte = %d, want raw offset %d", bomRows[0].SourceRange.StartByte, plainRows[0].SourceRange.StartByte+3)
	}
	if bomRows[0].SourceRange.EndByte != plainRows[0].SourceRange.EndByte+3 {
		t.Fatalf("BOM end byte = %d, want raw offset %d", bomRows[0].SourceRange.EndByte, plainRows[0].SourceRange.EndByte+3)
	}
}

func TestAnalyzeUsesRawUTF8OffsetsWithBOMAndCRLF(t *testing.T) {
	source := []byte("\xef\xbb\xbfpackage p\r\n// 한글 😀\r\nfunc F() {}\r\n")
	rows, err := AnalyzeSource(source, "unicode.go")
	if err != nil {
		t.Fatal(err)
	}
	wantStart := bytes.Index(source, []byte("func F"))
	wantEnd := bytes.Index(source, []byte("}\r\n")) + 1
	if len(rows) != 1 || rows[0].SourceRange.StartByte != int64(wantStart) || rows[0].SourceRange.EndByte != int64(wantEnd) {
		t.Fatalf("range = %#v, want raw byte range [%d,%d)", rows, wantStart, wantEnd)
	}
}
