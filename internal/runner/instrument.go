package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const helperPrefix = "sentinelGoTypedRunner"

var outputComment = regexp.MustCompile(`(?m)^\s*//\s*(Unordered )?Output:`)

type instrumentedFile struct {
	path        string
	original    []byte
	transformed []byte
	mode        fs.FileMode
}

type packagePlan struct {
	directory   string
	packageName string
	helperPath  string
	runnable    bool
}

type instrumentation struct {
	files         []instrumentedFile
	packages      []packagePlan
	inventory     []InventoryEntry
	captureReplay bool
	sites         map[string]string
}

func instrumentProject(root string, captureReplay bool) (*instrumentation, error) {
	modulePath, err := runnerModulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}
	plan := &instrumentation{captureReplay: captureReplay, sites: map[string]string{}}
	packages := make(map[string]packagePlan)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkError error) error {
		return plan.inspectEntry(root, modulePath, packages, path, entry, walkError)
	})
	if err != nil {
		return nil, err
	}
	if len(plan.inventory) == 0 {
		return nil, fmt.Errorf("typed runner found no supported tests")
	}
	plan.selectRunnablePackages(packages)
	slices.SortFunc(plan.inventory, compareInventoryEntry)
	slices.SortFunc(plan.packages, comparePackagePlan)
	return plan, nil
}

func (plan *instrumentation) selectRunnablePackages(packages map[string]packagePlan) {
	for _, entry := range packages {
		if entry.runnable {
			plan.packages = append(plan.packages, entry)
		}
	}
	files := plan.files[:0]
	for _, file := range plan.files {
		if packages[filepath.Dir(file.path)].runnable {
			files = append(files, file)
		}
	}
	plan.files = files
}

func compareInventoryEntry(left, right InventoryEntry) int {
	return strings.Compare(left.ID, right.ID)
}

func comparePackagePlan(left, right packagePlan) int {
	return strings.Compare(left.helperPath, right.helperPath)
}

func (plan *instrumentation) inspectEntry(root, modulePath string, packages map[string]packagePlan, path string, entry fs.DirEntry, walkError error) error {
	if walkError != nil {
		return walkError
	}
	if entry.Type()&os.ModeSymlink != 0 {
		return fmt.Errorf("typed runner rejects symbolic links")
	}
	if entry.IsDir() {
		return runnerDirectoryDecision(root, path)
	}
	if !entry.Type().IsRegular() || !strings.HasSuffix(path, "_test.go") {
		return nil
	}
	return plan.inspectTestSource(root, modulePath, packages, path)
}

func (plan *instrumentation) inspectTestSource(root, modulePath string, packages map[string]packagePlan, path string) error {
	file, packageEntry, inventory, err := plan.instrumentTestFile(root, modulePath, path)
	if err != nil {
		return err
	}
	if len(inventory) == 0 && !plan.captureReplay {
		return nil
	}
	if !compatiblePackagePlan(packages[packageEntry.directory], packageEntry) {
		return fmt.Errorf("typed runner does not support mixed test packages")
	}
	packageEntry.runnable = packages[packageEntry.directory].runnable || len(inventory) > 0
	packages[packageEntry.directory] = packageEntry
	plan.files = append(plan.files, file)
	plan.inventory = append(plan.inventory, inventory...)
	return nil
}

func compatiblePackagePlan(prior, current packagePlan) bool {
	return prior.packageName == "" || prior.packageName == current.packageName
}

func runnerDirectoryDecision(root, path string) error {
	if path == root {
		return nil
	}
	switch filepath.Base(path) {
	case ".git", ".sentinel", ".toolchain", "target", "vendor", "third_party", "testdata":
		return filepath.SkipDir
	default:
		return nil
	}
}

func (plan *instrumentation) instrumentTestFile(root, modulePath, path string) (instrumentedFile, packagePlan, []InventoryEntry, error) {
	payload, info, err := loadTestSource(path)
	if err != nil {
		return instrumentedFile{}, packagePlan{}, nil, err
	}
	set := token.NewFileSet()
	file, err := parseRunnerTestSource(set, path, payload)
	if err != nil {
		return instrumentedFile{}, packagePlan{}, nil, err
	}
	packagePath, directory, err := testPackagePath(root, modulePath, path)
	if err != nil {
		return instrumentedFile{}, packagePlan{}, nil, err
	}
	if plan.captureReplay {
		instrumentAssertions(set, file, payload, packagePath, filepath.Base(path), plan.sites)
	}
	inventory, err := instrumentDeclarations(set, file, payload, packagePath)
	if err != nil {
		return instrumentedFile{}, packagePlan{}, nil, err
	}
	rendered, err := renderInstrumentedFile(set, file)
	if err != nil {
		return instrumentedFile{}, packagePlan{}, nil, err
	}
	helperName := helperFileName(file.Name.Name)
	return instrumentedFile{path: path, original: payload, transformed: rendered, mode: info.Mode().Perm()}, packagePlan{
		directory: directory, packageName: file.Name.Name, helperPath: filepath.Join(directory, helperName),
	}, inventory, nil
}

func parseRunnerTestSource(set *token.FileSet, path string, payload []byte) (*ast.File, error) {
	file, err := parser.ParseFile(set, path, payload, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse test source: %w", err)
	}
	if hasReservedIdentifier(file) {
		return nil, fmt.Errorf("test package collides with runner helper")
	}
	return file, nil
}

func loadTestSource(path string) ([]byte, fs.FileInfo, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("test source is not regular")
	}
	return payload, info, nil
}

func renderInstrumentedFile(set *token.FileSet, file *ast.File) ([]byte, error) {
	var rendered bytes.Buffer
	if err := format.Node(&rendered, set, file); err != nil {
		return nil, fmt.Errorf("format instrumented test: %w", err)
	}
	return rendered.Bytes(), nil
}

func hasReservedIdentifier(file *ast.File) bool {
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if ok && strings.HasPrefix(identifier.Name, helperPrefix) {
			found = true
			return false
		}
		return !found
	})
	return found
}

func testPackagePath(root, modulePath, sourcePath string) (string, string, error) {
	directory := filepath.Dir(sourcePath)
	relative, err := filepath.Rel(root, directory)
	if err != nil || strings.HasPrefix(relative, "..") {
		return "", "", fmt.Errorf("test package path is outside project")
	}
	if relative == "." {
		return modulePath, directory, nil
	}
	return modulePath + "/" + filepath.ToSlash(relative), directory, nil
}

func instrumentDeclarations(set *token.FileSet, file *ast.File, source []byte, packagePath string) ([]InventoryEntry, error) {
	var inventory []InventoryEntry
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil {
			continue
		}
		kind, runnable, err := runnableKind(set, function, source)
		if err != nil {
			return nil, err
		}
		if !runnable {
			continue
		}
		id := packagePath + "." + function.Name.Name
		if err := prependInstrumentation(function, id, kind); err != nil {
			return nil, err
		}
		inventory = append(inventory, InventoryEntry{ID: id, Kind: kind})
	}
	return inventory, nil
}

func runnableKind(set *token.FileSet, function *ast.FuncDecl, source []byte) (TestKind, bool, error) {
	name := function.Name.Name
	if testName(name, "Benchmark") {
		return "", false, fmt.Errorf("benchmarks are unsupported by typed runner")
	}
	if name == "TestMain" {
		return "", false, fmt.Errorf("custom TestMain is unsupported by typed runner")
	}
	if testName(name, "Test") {
		return validateTestingSignature(function, "T", KindTest)
	}
	if testName(name, "Fuzz") {
		return validateTestingSignature(function, "F", KindFuzz)
	}
	if testName(name, "Example") && runnableExample(set, function, source) {
		return validateExampleSignature(function)
	}
	return "", false, nil
}

func testName(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	remainder := strings.TrimPrefix(name, prefix)
	first, _ := utf8.DecodeRuneInString(remainder)
	return remainder == "" || !unicode.IsLower(first)
}

func validateTestingSignature(function *ast.FuncDecl, testingType string, kind TestKind) (TestKind, bool, error) {
	if !supportedTestingShape(function) {
		return "", false, fmt.Errorf("unsupported %s signature", function.Name.Name)
	}
	field := function.Type.Params.List[0]
	if len(field.Names) != 1 || !testingPointer(field.Type, testingType) {
		return "", false, fmt.Errorf("unsupported %s parameter", function.Name.Name)
	}
	return kind, true, nil
}

func supportedTestingShape(function *ast.FuncDecl) bool {
	return function.Type.TypeParams == nil && function.Type.Results == nil &&
		function.Type.Params != nil && len(function.Type.Params.List) == 1 && function.Body != nil
}

func testingPointer(expression ast.Expr, name string) bool {
	pointer, ok := expression.(*ast.StarExpr)
	if !ok {
		return false
	}
	selector, ok := pointer.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	packageName, packageOK := selector.X.(*ast.Ident)
	return packageOK && packageName.Name == "testing" && selector.Sel.Name == name
}

func runnableExample(set *token.FileSet, function *ast.FuncDecl, source []byte) bool {
	start := set.Position(function.Pos()).Offset
	end := set.Position(function.End()).Offset
	return start >= 0 && end <= len(source) && start < end && outputComment.Match(source[start:end])
}

func validateExampleSignature(function *ast.FuncDecl) (TestKind, bool, error) {
	paramsEmpty := function.Type.Params == nil || len(function.Type.Params.List) == 0
	resultsEmpty := function.Type.Results == nil || len(function.Type.Results.List) == 0
	if !paramsEmpty || !resultsEmpty || function.Type.TypeParams != nil || function.Body == nil {
		return "", false, fmt.Errorf("unsupported example signature")
	}
	return KindExample, true, nil
}

func prependInstrumentation(function *ast.FuncDecl, id string, kind TestKind) error {
	parameter := ""
	finish := helperPrefix + "FinishExample"
	if kind != KindExample {
		parameter = function.Type.Params.List[0].Names[0].Name
		finish = helperPrefix + "PrepareTesting"
	}
	snippet := "package p\nfunc _(){" + helperPrefix + "Emit(" + strconv.Quote(id) + ", \"start\"); "
	if kind == KindExample {
		snippet += "defer " + finish + "(" + strconv.Quote(id) + ")"
	} else {
		snippet += "defer " + helperPrefix + "CatchPanic(" + strconv.Quote(id) + "); " + finish + "(" + strconv.Quote(id) + ", " + parameter + ")"
	}
	snippet += "}"
	parsed, err := parser.ParseFile(token.NewFileSet(), "instrumentation.go", snippet, 0)
	if err != nil {
		return fmt.Errorf("build test instrumentation: %w", err)
	}
	statements := parsed.Decls[0].(*ast.FuncDecl).Body.List
	function.Body.List = append(statements, function.Body.List...)
	return nil
}

func helperFileName(packageName string) string {
	digest := sha256.Sum256([]byte(packageName))
	return "zz_sentinel_typed_runner_" + hex.EncodeToString(digest[:4]) + "_test.go"
}

func (plan *instrumentation) apply() (func() error, error) {
	if err := plan.ensureHelperPathsAvailable(); err != nil {
		return nil, err
	}
	if err := plan.writeTransformedFiles(); err != nil {
		return nil, err
	}
	if err := plan.writeHelperFiles(); err != nil {
		_ = plan.restore()
		return nil, err
	}
	return plan.restore, nil
}

func (plan *instrumentation) ensureHelperPathsAvailable() error {
	for _, packageEntry := range plan.packages {
		if _, err := os.Lstat(packageEntry.helperPath); err == nil || !os.IsNotExist(err) {
			return fmt.Errorf("runner helper path already exists")
		}
	}
	return nil
}

func (plan *instrumentation) writeTransformedFiles() error {
	for index, file := range plan.files {
		if err := os.WriteFile(file.path, file.transformed, file.mode); err != nil {
			_ = plan.restoreFiles(index)
			return fmt.Errorf("write instrumented test: %w", err)
		}
	}
	return nil
}

func (plan *instrumentation) writeHelperFiles() error {
	for _, packageEntry := range plan.packages {
		source := plan.helperSource(packageEntry)
		if err := os.WriteFile(packageEntry.helperPath, []byte(source), 0o600); err != nil {
			return fmt.Errorf("write runner helper: %w", err)
		}
	}
	return nil
}

func (plan *instrumentation) helperSource(entry packagePlan) string {
	source := runnerHelperSource(entry.packageName)
	if plan.captureReplay {
		source += assertionHelperSource
	}
	return source
}

func (plan *instrumentation) restoreFiles(limit int) error {
	var first error
	for index := 0; index < limit; index++ {
		file := plan.files[index]
		if err := restoreTestFile(file); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (plan *instrumentation) restore() error {
	first := plan.restoreFiles(len(plan.files))
	for _, packageEntry := range plan.packages {
		if err := removeTestHelper(packageEntry.helperPath); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func runnerModulePath(path string) (string, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(payload), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("module directive missing")
}

func runnerHelperSource(packageName string) string {
	return fmt.Sprintf(`package %s

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"sync"
	"testing"
)

type sentinelGoTypedRunnerEvent struct {
	Nonce string `+"`json:\"nonce\"`"+`
	TestID string `+"`json:\"testId,omitempty\"`"+`
	Kind string `+"`json:\"kind\"`"+`
	SiteID string `+"`json:\"siteId,omitempty\"`"+`
}

var sentinelGoTypedRunnerMutex sync.Mutex
var sentinelGoTypedRunnerPanics = map[string]bool{}

func sentinelGoTypedRunnerEmit(testID, kind string) {
	event := sentinelGoTypedRunnerEvent{Nonce: os.Getenv("SENTINEL_GO_RUNNER_NONCE"), TestID: testID, Kind: kind}
	sentinelGoTypedRunnerWriteEvent(event)
}

func sentinelGoTypedRunnerWriteEvent(event sentinelGoTypedRunnerEvent) {
	payload, err := json.Marshal(event)
	if err != nil || len(payload) > %d { return }
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	sentinelGoTypedRunnerMutex.Lock()
	defer sentinelGoTypedRunnerMutex.Unlock()
	file, err := os.OpenFile(os.Getenv("SENTINEL_GO_RUNNER_EVENTS"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil { return }
	_, _ = file.Write(frame)
	_ = file.Close()
}

func sentinelGoTypedRunnerCatchPanic(testID string) {
	if recovered := recover(); recovered != nil {
		sentinelGoTypedRunnerMutex.Lock()
		sentinelGoTypedRunnerPanics[testID] = true
		sentinelGoTypedRunnerMutex.Unlock()
		sentinelGoTypedRunnerEmit(testID, "panic")
		panic(recovered)
	}
}

func sentinelGoTypedRunnerPrepareTesting(testID string, test interface{ Failed() bool; Cleanup(func()) }) {
	test.Cleanup(func() {
		sentinelGoTypedRunnerMutex.Lock()
		panicked := sentinelGoTypedRunnerPanics[testID]
		delete(sentinelGoTypedRunnerPanics, testID)
		sentinelGoTypedRunnerMutex.Unlock()
		if panicked { return }
		if test.Failed() { sentinelGoTypedRunnerEmit(testID, "assertionFailure"); return }
		sentinelGoTypedRunnerEmit(testID, "normalReturn")
	})
}

func sentinelGoTypedRunnerFinishExample(testID string) {
	if recovered := recover(); recovered != nil {
		sentinelGoTypedRunnerEmit(testID, "panic")
		panic(recovered)
	}
	sentinelGoTypedRunnerEmit(testID, "normalReturn")
}

func TestMain(m *testing.M) {
	sentinelGoTypedRunnerEmit("", "mainStart")
	defer func() {
		if recovered := recover(); recovered != nil {
			sentinelGoTypedRunnerEmit("", "panic")
			panic(recovered)
		}
	}()
	code := m.Run()
	sentinelGoTypedRunnerEmit("", "mainRunReturned")
	sentinelGoTypedRunnerEmit("", "mainNormalReturn")
	os.Exit(code)
}
`, packageName, privateEventPayloadLimit)
}
