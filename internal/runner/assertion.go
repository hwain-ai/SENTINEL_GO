package runner

import (
	"go/ast"
	"go/token"
	"strconv"
)

// RISK(security): only direct calls on resolved testing parameters are marked.
// Method values, aliases and external assertion helpers without sites fail closed.
func instrumentAssertions(set *token.FileSet, file *ast.File, source []byte, packagePath, name string, sites map[string]string) {
	parameters := testingParameters(file)
	sourceSHA256 := hashReplayFields([]string{string(source)})
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, receiver := assertionCall(call, parameters)
		if selector == nil {
			return true
		}
		site := hashReplayFields([]string{"assertion-v1", packagePath, name, sourceSHA256,
			strconv.Itoa(set.PositionFor(call.Pos(), false).Offset), selector.Sel.Name})
		sites[site] = packagePath
		// Preserve the original argument list, including a sole multi-value call.
		call.Fun = &ast.SelectorExpr{X: &ast.CallExpr{Fun: ast.NewIdent(helperPrefix + "Bind"),
			Args: []ast.Expr{receiver, quotedAST(packagePath), quotedAST(site)}}, Sel: selector.Sel}
		return true
	})
}

func testingParameters(file *ast.File) map[*ast.Object]bool {
	parameters := map[*ast.Object]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		field, ok := node.(*ast.Field)
		if !ok || !replayTestingType(field.Type) {
			return true
		}
		for _, name := range field.Names {
			if name.Obj != nil {
				parameters[name.Obj] = true
			}
		}
		return true
	})
	return parameters
}

func replayTestingType(expression ast.Expr) bool {
	if testingPointer(expression, "T") || testingPointer(expression, "F") {
		return true
	}
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "TB" {
		return false
	}
	name, ok := selector.X.(*ast.Ident)
	return ok && name.Name == "testing"
}

func assertionCall(call *ast.CallExpr, parameters map[*ast.Object]bool) (*ast.SelectorExpr, *ast.Ident) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !assertionMethod(selector.Sel.Name) {
		return nil, nil
	}
	receiver, ok := selector.X.(*ast.Ident)
	if !ok || !parameters[receiver.Obj] {
		return nil, nil
	}
	return selector, receiver
}

func assertionMethod(name string) bool {
	switch name {
	case "Fail", "FailNow", "Error", "Errorf", "Fatal", "Fatalf":
		return true
	default:
		return false
	}
}

func quotedAST(value string) *ast.BasicLit {
	return &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(value)}
}

// Forward after arguments have been evaluated, including for defer and go calls.
// Use interface{} in generated code to preserve pre-Go 1.18 project language versions.
const assertionHelperSource = `
func sentinelGoTypedRunnerAssertion(test testing.TB, pkg, site string) {
	sentinelGoTypedRunnerWriteEvent(sentinelGoTypedRunnerEvent{
		Nonce: os.Getenv("SENTINEL_GO_RUNNER_NONCE"), TestID: pkg + "." + test.Name(), Kind: "assertionSite", SiteID: site,
	})
}
type sentinelGoTypedRunnerBoundAssertion struct { test testing.TB; pkg, site string }
func sentinelGoTypedRunnerBind(test testing.TB, pkg, site string) sentinelGoTypedRunnerBoundAssertion {
	return sentinelGoTypedRunnerBoundAssertion{test: test, pkg: pkg, site: site}
}
func (bound sentinelGoTypedRunnerBoundAssertion) Fail() {
	bound.test.Helper(); sentinelGoTypedRunnerAssertion(bound.test, bound.pkg, bound.site); bound.test.Fail()
}
func (bound sentinelGoTypedRunnerBoundAssertion) FailNow() {
	bound.test.Helper(); sentinelGoTypedRunnerAssertion(bound.test, bound.pkg, bound.site); bound.test.FailNow()
}
func (bound sentinelGoTypedRunnerBoundAssertion) Error(args ...interface{}) {
	bound.test.Helper(); sentinelGoTypedRunnerAssertion(bound.test, bound.pkg, bound.site); bound.test.Error(args...)
}
func (bound sentinelGoTypedRunnerBoundAssertion) Errorf(format string, args ...interface{}) {
	bound.test.Helper(); sentinelGoTypedRunnerAssertion(bound.test, bound.pkg, bound.site); bound.test.Errorf(format, args...)
}
func (bound sentinelGoTypedRunnerBoundAssertion) Fatal(args ...interface{}) {
	bound.test.Helper(); sentinelGoTypedRunnerAssertion(bound.test, bound.pkg, bound.site); bound.test.Fatal(args...)
}
func (bound sentinelGoTypedRunnerBoundAssertion) Fatalf(format string, args ...interface{}) {
	bound.test.Helper(); sentinelGoTypedRunnerAssertion(bound.test, bound.pkg, bound.site); bound.test.Fatalf(format, args...)
}
`
