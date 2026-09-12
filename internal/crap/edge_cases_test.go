package crap

import (
	"errors"
	"go/ast"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyzeNamesAdditionalCallableForms(t *testing.T) {
	source := []byte(`package sample

var PackageLevel = func() {}

type First struct{}
type Second struct{}

func (First) Same() {}
func (*Second) Same() {}

func Outer() {
	wrapped := (func() {})
	consume(func() {})
	_ = wrapped
}
`)
	rows, err := AnalyzeSource(source, "forms.go")
	if err != nil {
		t.Fatalf("AnalyzeSource returned error: %v", err)
	}
	want := []string{
		"<literal:PackageLevel>",
		"First.Same",
		"(*Second).Same",
		"Outer",
		"Outer.<literal:wrapped>",
		"Outer.<literal:anonymous>",
	}
	if len(rows) != len(want) {
		t.Fatalf("len(rows) = %d, want %d: %#v", len(rows), len(want), rows)
	}
	for index, name := range want {
		if rows[index].QualifiedName != name {
			t.Fatalf("rows[%d].QualifiedName = %q, want %q", index, rows[index].QualifiedName, name)
		}
	}
}

func TestAnalyzeIdentityIgnoresBodyAndByteOffsetChanges(t *testing.T) {
	first, err := AnalyzeSource([]byte("package p\nfunc Stable(x int) int { return x }\n"), "stable.go")
	if err != nil {
		t.Fatal(err)
	}
	second, err := AnalyzeSource([]byte("package p\n\n\nfunc Stable(x int) int { if x > 0 { return x }; return 0 }\n"), "stable.go")
	if err != nil {
		t.Fatal(err)
	}
	if first[0].CallableID != second[0].CallableID {
		t.Fatalf("stable callable IDs differ: %q and %q", first[0].CallableID, second[0].CallableID)
	}
}

func TestAnalyzeIdentityUsesNamespaceAndCanonicalGoSignature(t *testing.T) {
	packageP, err := AnalyzeSource([]byte("package p\nfunc Stable(value int) int { return value }\n"), "stable.go")
	if err != nil {
		t.Fatal(err)
	}
	packageQ, err := AnalyzeSource([]byte("package q\nfunc Stable(value int) int { return value }\n"), "stable.go")
	if err != nil {
		t.Fatal(err)
	}
	renamedParameter, err := AnalyzeSource([]byte("package p\nfunc Stable(renamed int) int { return renamed }\n"), "stable.go")
	if err != nil {
		t.Fatal(err)
	}
	if packageP[0].CallableID == packageQ[0].CallableID {
		t.Fatal("callable ID did not change with the package namespace")
	}
	if packageP[0].CallableID != renamedParameter[0].CallableID {
		t.Fatal("callable ID changed when only a parameter name changed")
	}

	firstLiteral, err := AnalyzeSource([]byte("package p\nfunc Outer() { worker := func(value int) {}; _ = worker }\n"), "literal.go")
	if err != nil {
		t.Fatal(err)
	}
	secondLiteral, err := AnalyzeSource([]byte("package p\nfunc Outer() { worker := func(renamed int) {}; _ = worker }\n"), "literal.go")
	if err != nil {
		t.Fatal(err)
	}
	if firstLiteral[1].CallableID != secondLiteral[1].CallableID {
		t.Fatal("literal ID changed when only a parameter name changed")
	}

	genericT, err := AnalyzeSource([]byte("package p\nfunc Generic[T ~int](value T) T { return value }\n"), "generic.go")
	if err != nil {
		t.Fatal(err)
	}
	genericU, err := AnalyzeSource([]byte("package p\nfunc Generic[U ~int](renamed U) U { return renamed }\n"), "generic.go")
	if err != nil {
		t.Fatal(err)
	}
	if genericT[0].CallableID != genericU[0].CallableID {
		t.Fatal("callable ID changed when only a type parameter name changed")
	}

	nestedValue, err := AnalyzeSource([]byte("package p\nfunc Callback(callback func(value int) int) {}\n"), "callback.go")
	if err != nil {
		t.Fatal(err)
	}
	nestedRenamed, err := AnalyzeSource([]byte("package p\nfunc Callback(renamed func(other int) int) {}\n"), "callback.go")
	if err != nil {
		t.Fatal(err)
	}
	if nestedValue[0].CallableID != nestedRenamed[0].CallableID {
		t.Fatal("callable ID changed with a nested function parameter name")
	}

	genericReceiverT, err := AnalyzeSource([]byte("package p\ntype Box[T any] struct{}\nfunc (Box[T]) Stable(value T) {}\n"), "receiver.go")
	if err != nil {
		t.Fatal(err)
	}
	genericReceiverU, err := AnalyzeSource([]byte("package p\ntype Box[U any] struct{}\nfunc (Box[U]) Stable(value U) {}\n"), "receiver.go")
	if err != nil {
		t.Fatal(err)
	}
	if genericReceiverT[0].CallableID != genericReceiverU[0].CallableID {
		t.Fatal("method ID changed when only a receiver type parameter name changed")
	}
	genericPairAB, err := AnalyzeSource([]byte("package p\ntype Pair[A, B any] struct{}\nfunc (Pair[A, B]) Stable(left A, right B) {}\n"), "receiver.go")
	if err != nil {
		t.Fatal(err)
	}
	genericPairXY, err := AnalyzeSource([]byte("package p\ntype Pair[X, Y any] struct{}\nfunc (Pair[X, Y]) Stable(left X, right Y) {}\n"), "receiver.go")
	if err != nil {
		t.Fatal(err)
	}
	if genericPairAB[0].CallableID != genericPairXY[0].CallableID {
		t.Fatal("method ID changed when two receiver type parameters were renamed")
	}

	placeholderType, err := AnalyzeSource([]byte("package p\ntype __sentinel_type_parameter_0 int\nfunc Stable[T any](value T, other __sentinel_type_parameter_0) {}\n"), "collision.go")
	if err != nil {
		t.Fatal(err)
	}
	repeatedTypeParameter, err := AnalyzeSource([]byte("package p\nfunc Stable[T any](value T, other T) {}\n"), "collision.go")
	if err != nil {
		t.Fatal(err)
	}
	if placeholderType[0].CallableID == repeatedTypeParameter[0].CallableID {
		t.Fatal("canonical type parameter placeholder collided with a legal Go type name")
	}
}

func TestCanonicalReceiverArgumentsFailClosed(t *testing.T) {
	identifier := func(name string) ast.Expr { return ast.NewIdent(name) }
	cases := []struct {
		original  []ast.Expr
		canonical []ast.Expr
	}{
		{[]ast.Expr{identifier("T")}, nil},
		{[]ast.Expr{&ast.StarExpr{X: identifier("T")}}, []ast.Expr{identifier("T")}},
		{[]ast.Expr{identifier("T")}, []ast.Expr{&ast.StarExpr{X: identifier("T")}}},
		{[]ast.Expr{identifier("T"), identifier("T")}, []ast.Expr{identifier("T"), identifier("T")}},
	}
	for _, testCase := range cases {
		if _, err := canonicalizeReceiverArguments(testCase.original, testCase.canonical); err == nil {
			t.Fatal("canonicalizeReceiverArguments accepted an ambiguous receiver descriptor")
		}
	}
}

func TestSignatureIdentifierClassificationPreservesNonTypeNames(t *testing.T) {
	selectorName := ast.NewIdent("T")
	if signatureIdentifierIsType(selectorName, &ast.SelectorExpr{X: ast.NewIdent("pkg"), Sel: selectorName}) {
		t.Fatal("selector field name was classified as a type reference")
	}
	fieldName := ast.NewIdent("T")
	if signatureIdentifierIsType(fieldName, &ast.Field{Names: []*ast.Ident{fieldName}, Type: ast.NewIdent("int")}) {
		t.Fatal("struct or interface field name was classified as a type reference")
	}
	fieldType := ast.NewIdent("T")
	if !signatureIdentifierIsType(fieldType, &ast.Field{Names: []*ast.Ident{ast.NewIdent("value")}, Type: fieldType}) {
		t.Fatal("field type was not classified as a type reference")
	}
}

func TestAnalyzeLiteralIdentityIncludesEnclosingNamedCallableID(t *testing.T) {
	source := []byte("package p\nfunc init() { worker := func() {}; _ = worker; println(1) }\nfunc init() { worker := func() {}; _ = worker; println(2) }\n")
	rows, err := AnalyzeSource(source, "init.go")
	if err != nil {
		t.Fatalf("AnalyzeSource rejected distinct init-owned literals: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("len(rows) = %d, want 4", len(rows))
	}
	literalIDs := make(map[string]struct{})
	for _, row := range rows {
		if row.Kind == FunctionLiteral {
			literalIDs[row.CallableID] = struct{}{}
		}
	}
	if len(literalIDs) != 2 {
		t.Fatalf("literal callable IDs = %#v, want two distinct IDs", literalIDs)
	}
}

func TestAnalyzeSelectorAndIndexBindingsAreDistinctSemanticSites(t *testing.T) {
	source := []byte("package p\ntype Holder struct{ F func() }\nfunc Outer(a, b *Holder) { a.F = func() {}; b.F = func() {} }\n")
	rows, err := AnalyzeSource(source, "selector.go")
	if err != nil {
		t.Fatalf("AnalyzeSource rejected distinct selector bindings: %v", err)
	}
	if len(rows) != 3 || rows[1].CallableID == rows[2].CallableID || rows[1].SemanticSite == rows[2].SemanticSite {
		t.Fatalf("selector-bound literal identities are not distinct: %#v", rows)
	}
	indexed, err := AnalyzeSource([]byte("package p\nfunc Outer(handlers []func()) { handlers[0] = func() {}; handlers[1] = func() {} }\n"), "index.go")
	if err != nil {
		t.Fatalf("AnalyzeSource rejected distinct index bindings: %v", err)
	}
	if len(indexed) != 3 || indexed[1].CallableID == indexed[2].CallableID || indexed[1].SemanticSite == indexed[2].SemanticSite {
		t.Fatalf("index-bound literal identities are not distinct: %#v", indexed)
	}
}

func TestAnalyzeFailsClosedForBodylessCallable(t *testing.T) {
	sources := [][]byte{
		[]byte("package p\nfunc Hidden()\n"),
		[]byte("package p\ntype Widget int\nfunc (Widget) Hidden()\n"),
	}
	for _, source := range sources {
		if _, err := AnalyzeSource(source, "bodyless.go"); err == nil {
			t.Fatal("AnalyzeSource silently omitted a bodyless callable")
		}
	}
}

func TestAnalyzeAnonymousIdentityIgnoresDistinctEarlierSibling(t *testing.T) {
	original, err := AnalyzeSource([]byte("package p\nfunc consume(func()) {}\nfunc consumeInt(func(int)) {}\nfunc Outer() { consume(func() {}) }\n"), "stable.go")
	if err != nil {
		t.Fatal(err)
	}
	modified, err := AnalyzeSource([]byte("package p\nfunc consume(func()) {}\nfunc consumeInt(func(int)) {}\nfunc Outer() { consumeInt(func(int) {}); consume(func() {}) }\n"), "stable.go")
	if err != nil {
		t.Fatal(err)
	}
	if original[3].CallableID != modified[4].CallableID {
		t.Fatalf("same trailing literal changed identity after distinct earlier insertion: %q != %q", original[3].CallableID, modified[4].CallableID)
	}
}

func TestAnalyzeRejectsAmbiguousAnonymousIdentity(t *testing.T) {
	source := []byte("package p\nfunc consume(func()) {}\nfunc Outer() { consume(func() {}); consume(func() {}) }\n")
	if _, err := AnalyzeSource(source, "ambiguous.go"); err == nil {
		t.Fatal("AnalyzeSource accepted duplicate anonymous callable descriptors")
	}
}

func TestAnalyzeAnonymousCallRolesSurviveReordering(t *testing.T) {
	firstSource := []byte("package p\nfunc consumeA(func()) {}\nfunc consumeB(func()) {}\nfunc Outer() { consumeA(func() {}); consumeB(func() {}) }\n")
	secondSource := []byte("package p\nfunc consumeA(func()) {}\nfunc consumeB(func()) {}\nfunc Outer() { consumeB(func() {}); consumeA(func() {}) }\n")
	first, err := AnalyzeSource(firstSource, "stable.go")
	if err != nil {
		t.Fatal(err)
	}
	second, err := AnalyzeSource(secondSource, "stable.go")
	if err != nil {
		t.Fatal(err)
	}
	firstIDs := literalIDsBySemanticRole(t, first)
	secondIDs := literalIDsBySemanticRole(t, second)
	for role, firstID := range firstIDs {
		if secondIDs[role] != firstID {
			t.Fatalf("role %q ID changed after reorder: %q != %q", role, firstID, secondIDs[role])
		}
	}
}

func literalIDsBySemanticRole(t *testing.T, rows []Callable) map[string]string {
	t.Helper()
	ids := make(map[string]string)
	for _, row := range rows {
		if row.Kind != FunctionLiteral {
			continue
		}
		if row.SemanticSite == "" {
			t.Fatalf("literal %q has no semantic site", row.QualifiedName)
		}
		ids[row.SemanticSite] = row.CallableID
	}
	if len(ids) != 2 {
		t.Fatalf("literal semantic roles = %#v, want two", ids)
	}
	return ids
}

func TestAnalyzeAnonymousPropertyAndReturnRolesAreDistinct(t *testing.T) {
	source := []byte(`package p
type Pair struct { Left func(); Right func() }
func Properties() { _ = Pair{Left: func() {}, Right: func() {}} }
func Returns() (func(), func()) { return func() {}, func() {} }
`)
	rows, err := AnalyzeSource(source, "roles.go")
	if err != nil {
		t.Fatalf("AnalyzeSource rejected distinct semantic roles: %v", err)
	}
	roles := make(map[string]bool)
	for _, row := range rows {
		if row.Kind == FunctionLiteral {
			roles[row.SemanticSite] = true
		}
	}
	if len(roles) != 4 {
		t.Fatalf("semantic roles = %#v, want four distinct descriptors", roles)
	}
}

func TestAnalyzeAnonymousInvocationKindsAreDistinct(t *testing.T) {
	source := []byte("package p\nfunc Outer() { go func() {}(); defer func() {}() }\n")
	rows, err := AnalyzeSource(source, "roles.go")
	if err != nil {
		t.Fatalf("AnalyzeSource rejected distinct invocation roles: %v", err)
	}
	roles := literalIDsBySemanticRole(t, rows)
	if len(roles) != 2 {
		t.Fatalf("semantic roles = %#v, want go and defer descriptors", roles)
	}
}

func TestAnalyzeRepeatedNamedSignatureUsesSemanticDiscriminator(t *testing.T) {
	firstSource := []byte("package p\nfunc record(string) {}\nfunc init() { record(\"a\") }\nfunc init() { record(\"b\") }\n")
	secondSource := []byte("package p\nfunc record(string) {}\n\nfunc init(){record(\"b\")}\n\nfunc init(){record(\"a\")}\n")
	first, err := AnalyzeSource(firstSource, "init.go")
	if err != nil {
		t.Fatalf("first AnalyzeSource rejected distinct init declarations: %v", err)
	}
	second, err := AnalyzeSource(secondSource, "init.go")
	if err != nil {
		t.Fatalf("second AnalyzeSource rejected reordered init declarations: %v", err)
	}
	firstIDs := callableIDSet(first, "init")
	secondIDs := callableIDSet(second, "init")
	if len(firstIDs) != 2 || len(secondIDs) != 2 {
		t.Fatalf("init ID sets = %#v and %#v, want two each", firstIDs, secondIDs)
	}
	for id := range firstIDs {
		if !secondIDs[id] {
			t.Fatalf("init ID %q changed after whitespace and reorder", id)
		}
	}
}

func TestAnalyzeRejectsIdenticalRepeatedNamedSignature(t *testing.T) {
	source := []byte("package p\nfunc init() {}\nfunc init() {}\n")
	if _, err := AnalyzeSource(source, "init.go"); err == nil {
		t.Fatal("AnalyzeSource accepted indistinguishable init declarations")
	}
}

func TestAnalyzeRejectsNonInitRepeatedNamedSignature(t *testing.T) {
	source := []byte("package p\nfunc Same() { println(1) }\nfunc Same() { println(2) }\n")
	if _, err := AnalyzeSource(source, "duplicate.go"); err == nil {
		t.Fatal("AnalyzeSource accepted repeated non-init declarations")
	}
}

func callableIDSet(rows []Callable, name string) map[string]bool {
	ids := make(map[string]bool)
	for _, row := range rows {
		if row.QualifiedName == name {
			ids[row.CallableID] = true
		}
	}
	return ids
}

func TestAnalyzeReportsDirectChildLiteralAsExcludedRange(t *testing.T) {
	rows, err := AnalyzeSource([]byte("package p\nfunc Outer() { inner := func() {}; _ = inner }\n"), "nested.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || len(rows[0].ExcludedRanges) != 1 || len(rows[1].ExcludedRanges) != 0 {
		t.Fatalf("unexpected callable exclusions: %#v", rows)
	}
	child := rows[1].SourceRange
	if rows[0].ExcludedRanges[0] != child {
		t.Fatalf("parent exclusion = %#v, want child range %#v", rows[0].ExcludedRanges[0], child)
	}
}

func TestAnalyzeRejectsAdditionalInvalidInputs(t *testing.T) {
	cases := []struct {
		path   string
		source []byte
	}{
		{"", []byte("package p\n")},
		{"/absolute.go", []byte("package p\n")},
		{"dir//file.go", []byte("package p\n")},
		{"nul\x00.go", []byte("package p\n")},
		{"broken.go", []byte("package")},
		{"duplicate.go", []byte("package p\nfunc Same() {}\nfunc Same() {}\n")},
	}
	for _, testCase := range cases {
		if _, err := AnalyzeSource(testCase.source, testCase.path); err == nil {
			t.Fatalf("AnalyzeSource(%q) succeeded, want error", testCase.path)
		}
	}
}

func TestProductionCallablesStayWithinComplexityBudget(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, filename := range files {
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}
		source, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		modulePath := filepath.ToSlash(filepath.Join("internal/crap", filename))
		rows, err := AnalyzeSource(source, modulePath)
		if err != nil {
			t.Fatalf("AnalyzeSource(%q): %v", filename, err)
		}
		for _, row := range rows {
			if row.Complexity > 8 {
				t.Errorf("%s:%s complexity = %d, want at most 8", modulePath, row.QualifiedName, row.Complexity)
			}
		}
	}
}

func TestParseCoverProfileRejectsHeaderAndIntegerErrors(t *testing.T) {
	if _, err := ParseCoverProfile(strings.NewReader("mode: set\n"), "../project"); err == nil {
		t.Fatal("ParseCoverProfile accepted an invalid expected module path")
	}
	cases := []io.Reader{
		strings.NewReader(""),
		strings.NewReader("mode: unknown\n"),
		strings.NewReader("mode: set\nnot-a-segment\n"),
		strings.NewReader("mode: set\nfile.go:9223372036854775808.1,9223372036854775808.2 1 1\n"),
		errorReader{},
		io.MultiReader(strings.NewReader("mode: set\n"), errorReader{}),
	}
	for _, input := range cases {
		if _, err := ParseCoverProfile(input, "example.com/project"); err == nil {
			t.Fatal("ParseCoverProfile succeeded, want error")
		}
	}
	for _, mode := range []string{"set", "count", "atomic"} {
		if _, err := ParseCoverProfile(strings.NewReader("mode: "+mode+"\n"), "example.com/project"); err != nil {
			t.Fatalf("valid mode %q failed: %v", mode, err)
		}
	}
}

func TestCoverProfileCountsPositionedRangesAndExclusions(t *testing.T) {
	profile, err := ParseCoverProfile(strings.NewReader("mode: count\nexample.com/project/file.go:1.1,1.5 2 1\nexample.com/project/file.go:2.1,2.5 3 0\nexample.com/project/file.go:10.1,10.5 4 2\n"), "example.com/project")
	if err != nil {
		t.Fatal(err)
	}
	callable := SourceRange{StartByte: 0, EndByte: 20, StartLine: 1, StartColumn: 1, EndLine: 3, EndColumn: 1}
	covered, total, reason := profile.Counts("file.go", callable, nil)
	if covered != 2 || total != 5 || reason != "" {
		t.Fatalf("positioned Counts = (%d, %d, %q), want (2, 5, empty)", covered, total, reason)
	}
	excluded := []SourceRange{{StartByte: 10, EndByte: 20, StartLine: 2, StartColumn: 1, EndLine: 2, EndColumn: 5}}
	covered, total, reason = profile.Counts("file.go", callable, excluded)
	if covered != 2 || total != 2 || reason != "" {
		t.Fatalf("excluded Counts = (%d, %d, %q), want (2, 2, empty)", covered, total, reason)
	}
	outside := SourceRange{StartByte: 30, EndByte: 40, StartLine: 4, StartColumn: 1, EndLine: 5, EndColumn: 1}
	covered, total, reason = profile.Counts("file.go", outside, nil)
	if covered != 0 || total != 0 || reason != "zeroExecutableUnits" {
		t.Fatalf("outside Counts = (%d, %d, %q), want (0, 0, zeroExecutableUnits)", covered, total, reason)
	}
}

func TestCoverProfileCountsFailsClosedForInvalidInputs(t *testing.T) {
	profile := &CoverProfile{segments: map[string][]coverageSegment{
		"file.go": {
			{start: sourcePosition{1, 1}, end: sourcePosition{1, 2}, statements: math.MaxInt64, hitCount: 1},
			{start: sourcePosition{2, 1}, end: sourcePosition{2, 2}, statements: 1, hitCount: 1},
		},
	}}
	cases := []struct {
		profile  *CoverProfile
		path     string
		callable SourceRange
		excluded []SourceRange
		want     string
	}{
		{nil, "file.go", SourceRange{StartByte: 0, EndByte: 1}, nil, "invalidCoverageInput"},
		{profile, "./file.go", SourceRange{StartByte: 0, EndByte: 1}, nil, "invalidCoverageInput"},
		{profile, "missing.go", SourceRange{StartByte: 0, EndByte: 1}, nil, "coverageFileMissing"},
		{profile, "file.go", SourceRange{StartByte: 1, EndByte: 2}, nil, "invalidSourceRange"},
		{profile, "file.go", SourceRange{StartByte: 0, EndByte: 2}, []SourceRange{{StartByte: 0, EndByte: 1}}, "invalidSourceRange"},
		{profile, "file.go", SourceRange{StartByte: 0, EndByte: 2, StartLine: 1, StartColumn: 1, EndLine: 3, EndColumn: 1}, nil, "coverageCountOverflow"},
	}
	for _, testCase := range cases {
		_, _, reason := testCase.profile.Counts(testCase.path, testCase.callable, testCase.excluded)
		if reason != testCase.want {
			t.Fatalf("Counts reason = %q, want %q", reason, testCase.want)
		}
	}
}

func TestCanonicalDecimalRejectsInvalidFractions(t *testing.T) {
	cases := [][2]string{
		{"not-a-number", "1"},
		{"1", "not-a-number"},
		{"-1", "2"},
		{"1", "0"},
	}
	for _, fraction := range cases {
		if _, err := RenderCanonicalDecimal(fraction[0], fraction[1]); err == nil {
			t.Fatalf("RenderCanonicalDecimal(%q, %q) succeeded, want error", fraction[0], fraction[1])
		}
	}
	zero, err := RenderCanonicalDecimal("0", "3")
	if err != nil || zero != "0" {
		t.Fatalf("zero rendering = %q, %v; want 0, nil", zero, err)
	}
	roundedUp, err := RenderCanonicalDecimal("2", "3")
	if err != nil || roundedUp != "0.666666666667" {
		t.Fatalf("rounded-up rendering = %q, %v; want 0.666666666667, nil", roundedUp, err)
	}
	leftPadded, err := RenderCanonicalDecimal("1", "1000000000000")
	if err != nil || leftPadded != "0.000000000001" {
		t.Fatalf("left-padded rendering = %q, %v; want 0.000000000001, nil", leftPadded, err)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}
