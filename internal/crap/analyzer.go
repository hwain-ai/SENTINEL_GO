package crap

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"unicode/utf8"
)

type callableIdentity struct {
	Version                  string       `json:"version"`
	ModulePath               string       `json:"modulePath"`
	Namespace                string       `json:"namespace"`
	Kind                     CallableKind `json:"kind"`
	QualifiedName            string       `json:"qualifiedName"`
	NormalizedSignature      string       `json:"normalizedSignature"`
	EnclosingNamedCallableID string       `json:"enclosingNamedCallableId"`
	SemanticSite             string       `json:"semanticSite"`
}

type literalSiteIdentity struct {
	Version string `json:"version"`
	Role    string `json:"role"`
	Anchor  string `json:"anchor"`
	Slot    int    `json:"slot"`
}

type analyzer struct {
	fset        *token.FileSet
	file        *ast.File
	modulePath  string
	parents     map[ast.Node]ast.Node
	rows        []Callable
	callableIDs map[string]struct{}
	namedSites  map[ast.Node]string
}

// AnalyzeSource discovers every function, method, and function literal as an
// independent callable without changing the supplied source bytes.
func AnalyzeSource(source []byte, modulePath string) ([]Callable, error) {
	if err := validateModulePath(modulePath); err != nil {
		return nil, err
	}
	if !utf8.Valid(source) {
		return nil, fmt.Errorf("source must be valid UTF-8")
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, modulePath, source, parser.AllErrors)
	if err != nil {
		return nil, fmt.Errorf("parse Go source: %w", err)
	}
	worker := &analyzer{
		fset:        fset,
		file:        file,
		modulePath:  modulePath,
		parents:     buildParentMap(file),
		callableIDs: make(map[string]struct{}),
		namedSites:  make(map[ast.Node]string),
	}
	if err := worker.prepareNamedSemanticSites(); err != nil {
		return nil, err
	}
	if err := worker.collect(); err != nil {
		return nil, err
	}
	return worker.rows, nil
}

func (a *analyzer) prepareNamedSemanticSites() error {
	groups, err := a.namedCallableGroups()
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return a.assignRepeatedNamedSites(keys, groups)
}

func (a *analyzer) namedCallableGroups() (map[string][]*ast.FuncDecl, error) {
	groups := make(map[string][]*ast.FuncDecl)
	for _, declaration := range a.file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if function.Body == nil {
			return nil, fmt.Errorf("bodyless callable %q is unsupported", function.Name.Name)
		}
		key, err := a.namedCallableKey(function)
		if err != nil {
			return nil, err
		}
		groups[key] = append(groups[key], function)
	}
	return groups, nil
}

func (a *analyzer) namedCallableKey(function *ast.FuncDecl) (string, error) {
	kind, name, signature, err := a.declaredCallableDescriptor(function)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(callableIdentity{
		Version:                  "sentinel-go-callable-v2",
		ModulePath:               a.modulePath,
		Namespace:                a.file.Name.Name,
		Kind:                     kind,
		QualifiedName:            name,
		NormalizedSignature:      signature,
		EnclosingNamedCallableID: "",
		SemanticSite:             "",
	})
	if err != nil {
		return "", fmt.Errorf("encode named callable key: %w", err)
	}
	return string(encoded), nil
}

func (a *analyzer) assignRepeatedNamedSites(keys []string, groups map[string][]*ast.FuncDecl) error {
	for _, key := range keys {
		group := groups[key]
		if len(group) < 2 {
			continue
		}
		if err := a.assignNamedDiscriminators(group); err != nil {
			return err
		}
	}
	return nil
}

func (a *analyzer) assignNamedDiscriminators(group []*ast.FuncDecl) error {
	if group[0].Recv != nil || group[0].Name.Name != "init" {
		return fmt.Errorf("ambiguous callable identity %q", group[0].Name.Name)
	}
	seen := make(map[string]struct{})
	for _, function := range group {
		normalized, err := a.renderNode(function)
		if err != nil {
			return err
		}
		digest := sha256.Sum256([]byte(normalized))
		anchor := hex.EncodeToString(digest[:])
		if _, duplicate := seen[anchor]; duplicate {
			return fmt.Errorf("ambiguous callable identity %q", function.Name.Name)
		}
		seen[anchor] = struct{}{}
		site, err := encodeSemanticSite("sentinel-go-declaration-site-v1", "normalizedDeclaration", anchor, -1)
		if err != nil {
			return err
		}
		a.namedSites[function] = site
	}
	return nil
}

func (a *analyzer) collect() error {
	for _, declaration := range a.file.Decls {
		if err := a.collectDeclaration(declaration); err != nil {
			return err
		}
	}
	return nil
}

func (a *analyzer) collectDeclaration(declaration ast.Decl) error {
	function, ok := declaration.(*ast.FuncDecl)
	if !ok {
		return a.collectLiterals(declaration, "", "")
	}
	if function.Body == nil {
		return fmt.Errorf("bodyless callable %q is unsupported", function.Name.Name)
	}
	kind, name, signature, err := a.declaredCallableDescriptor(function)
	if err != nil {
		return err
	}
	ownerID, err := a.appendCallable(function, signature, function.Body, kind, name, "")
	if err != nil {
		return err
	}
	return a.collectLiterals(function.Body, name, ownerID)
}

func (a *analyzer) declaredCallableDescriptor(function *ast.FuncDecl) (CallableKind, string, string, error) {
	if function.Recv == nil || len(function.Recv.List) == 0 {
		signature, err := a.renderFunctionSignature(function.Type, nil)
		return Function, function.Name.Name, signature, err
	}
	receiver, receiverTypeNames, err := a.renderCanonicalReceiver(function.Recv.List[0].Type)
	if err != nil {
		return "", "", "", err
	}
	signature, err := a.renderFunctionSignature(function.Type, receiverTypeNames)
	if err != nil {
		return "", "", "", err
	}
	if strings.HasPrefix(receiver, "*") {
		receiver = "(" + receiver + ")"
	}
	return Method, receiver + "." + function.Name.Name, signature, nil
}

func (a *analyzer) collectLiterals(root ast.Node, ownerName, ownerID string) error {
	var collectionError error
	ast.Inspect(root, func(node ast.Node) bool {
		if collectionError != nil {
			return false
		}
		literal, ok := node.(*ast.FuncLit)
		if !ok {
			return true
		}
		name := a.literalName(literal, ownerName)
		signature, err := a.renderFunctionSignature(literal.Type, nil)
		if err != nil {
			collectionError = err
			return false
		}
		if _, collectionError = a.appendCallable(literal, signature, literal.Body, FunctionLiteral, name, ownerID); collectionError != nil {
			return false
		}
		collectionError = a.collectLiterals(literal.Body, name, ownerID)
		return false
	})
	return collectionError
}

func (a *analyzer) literalName(literal *ast.FuncLit, ownerName string) string {
	hint := literalBindingHint(literal, a.parents)
	if hint == "" {
		hint = "anonymous"
	}
	prefix := ownerName
	if prefix != "" {
		prefix += "."
	}
	return prefix + "<literal:" + hint + ">"
}

func (a *analyzer) appendCallable(node ast.Node, signature string, body *ast.BlockStmt, kind CallableKind, name, ownerID string) (string, error) {
	semanticSite, callableID, err := a.makeCallableIdentity(node, signature, kind, name, ownerID)
	if err != nil {
		return "", err
	}
	if err := a.reserveCallableID(callableID, name); err != nil {
		return "", err
	}
	rangeValue, err := a.sourceRange(node)
	if err != nil {
		return "", err
	}
	excluded, err := a.directLiteralRanges(body)
	if err != nil {
		return "", err
	}
	a.rows = append(a.rows, Callable{
		ModulePath:          a.modulePath,
		Kind:                kind,
		QualifiedName:       name,
		NormalizedSignature: signature,
		SemanticSite:        semanticSite,
		CallableID:          callableID,
		SourceRange:         rangeValue,
		ExcludedRanges:      excluded,
		Complexity:          cyclomaticComplexity(body),
	})
	return callableID, nil
}

func (a *analyzer) makeCallableIdentity(node ast.Node, signature string, kind CallableKind, name, ownerID string) (string, string, error) {
	semanticSite, err := a.semanticSiteForNode(node)
	if err != nil {
		return "", "", err
	}
	identity := callableIdentity{
		Version:                  "sentinel-go-callable-v2",
		ModulePath:               a.modulePath,
		Namespace:                a.file.Name.Name,
		Kind:                     kind,
		QualifiedName:            name,
		NormalizedSignature:      signature,
		EnclosingNamedCallableID: ownerID,
		SemanticSite:             semanticSite,
	}
	encodedIdentity, err := json.Marshal(identity)
	if err != nil {
		return "", "", fmt.Errorf("encode callable identity: %w", err)
	}
	digest := sha256.Sum256(encodedIdentity)
	return semanticSite, hex.EncodeToString(digest[:]), nil
}

func (a *analyzer) semanticSiteForNode(node ast.Node) (string, error) {
	if site, exists := a.namedSites[node]; exists {
		return site, nil
	}
	literal, ok := node.(*ast.FuncLit)
	if !ok {
		return "", nil
	}
	return a.literalSemanticSite(literal)
}

func (a *analyzer) reserveCallableID(callableID, name string) error {
	if _, duplicate := a.callableIDs[callableID]; duplicate {
		return fmt.Errorf("ambiguous callable identity %q", name)
	}
	a.callableIDs[callableID] = struct{}{}
	return nil
}

func (a *analyzer) literalSemanticSite(literal *ast.FuncLit) (string, error) {
	if target := literalBindingTarget(literal, a.parents); target != nil {
		binding, err := a.renderNode(target)
		if err != nil {
			return "", err
		}
		return encodeLiteralSite("binding", binding, -1)
	}
	child, parent := literalContext(literal, a.parents)
	return a.literalSiteForParent(child, parent)
}

func literalContext(literal *ast.FuncLit, parents map[ast.Node]ast.Node) (ast.Node, ast.Node) {
	child := ast.Node(literal)
	for {
		parent := parents[child]
		parentheses, ok := parent.(*ast.ParenExpr)
		if !ok {
			return child, parent
		}
		child = parentheses
	}
}

func (a *analyzer) literalSiteForParent(child, parent ast.Node) (string, error) {
	switch typed := parent.(type) {
	case *ast.CallExpr:
		return a.callLiteralSite(child, typed)
	case *ast.KeyValueExpr:
		return a.propertyLiteralSite(child, typed)
	case *ast.ReturnStmt:
		return returnLiteralSite(child, typed)
	default:
		return encodeLiteralSite("unanchored", "", -1)
	}
}

func (a *analyzer) callLiteralSite(child ast.Node, call *ast.CallExpr) (string, error) {
	if call.Fun == child {
		return a.invokedLiteralSite(call)
	}
	anchor, err := a.renderNode(call.Fun)
	if err != nil {
		return "", err
	}
	for index, argument := range call.Args {
		if argument == child {
			return encodeLiteralSite("argument", anchor, index)
		}
	}
	return encodeLiteralSite("unanchored", "", -1)
}

func (a *analyzer) invokedLiteralSite(call *ast.CallExpr) (string, error) {
	switch a.parents[call].(type) {
	case *ast.GoStmt:
		return encodeLiteralSite("goCallable", "", -1)
	case *ast.DeferStmt:
		return encodeLiteralSite("deferCallable", "", -1)
	case *ast.ExprStmt:
		return encodeLiteralSite("invokedCallable", "", -1)
	default:
		return encodeLiteralSite("callableExpression", "", -1)
	}
}

func (a *analyzer) propertyLiteralSite(child ast.Node, property *ast.KeyValueExpr) (string, error) {
	if property.Value != child {
		return encodeLiteralSite("unanchored", "", -1)
	}
	anchor, err := a.renderNode(property.Key)
	if err != nil {
		return "", err
	}
	return encodeLiteralSite("property", anchor, -1)
}

func returnLiteralSite(child ast.Node, statement *ast.ReturnStmt) (string, error) {
	for index, result := range statement.Results {
		if result == child {
			return encodeLiteralSite("returnValue", "", index)
		}
	}
	return encodeLiteralSite("unanchored", "", -1)
}

func encodeLiteralSite(role, anchor string, slot int) (string, error) {
	return encodeSemanticSite("sentinel-go-literal-site-v1", role, anchor, slot)
}

func encodeSemanticSite(version, role, anchor string, slot int) (string, error) {
	encoded, err := json.Marshal(literalSiteIdentity{
		Version: version,
		Role:    role,
		Anchor:  anchor,
		Slot:    slot,
	})
	if err != nil {
		return "", fmt.Errorf("encode literal semantic site: %w", err)
	}
	return string(encoded), nil
}

func (a *analyzer) directLiteralRanges(body *ast.BlockStmt) ([]SourceRange, error) {
	ranges := make([]SourceRange, 0)
	var rangeError error
	ast.Inspect(body, func(node ast.Node) bool {
		if rangeError != nil {
			return false
		}
		literal, ok := node.(*ast.FuncLit)
		if !ok {
			return true
		}
		var rangeValue SourceRange
		rangeValue, rangeError = a.sourceRange(literal)
		if rangeError == nil {
			ranges = append(ranges, rangeValue)
		}
		return false
	})
	return ranges, rangeError
}

func (a *analyzer) sourceRange(node ast.Node) (SourceRange, error) {
	start := a.fset.PositionFor(node.Pos(), false)
	end := a.fset.PositionFor(node.End(), false)
	file := a.fset.File(node.Pos())
	if file == nil || !node.Pos().IsValid() || !node.End().IsValid() {
		return SourceRange{}, fmt.Errorf("callable has invalid source position")
	}
	startByte := file.Offset(node.Pos())
	endByte := file.Offset(node.End())
	if startByte < 0 || endByte <= startByte {
		return SourceRange{}, fmt.Errorf("callable has invalid byte range")
	}
	return SourceRange{
		StartByte:   int64(startByte),
		EndByte:     int64(endByte),
		StartLine:   start.Line,
		StartColumn: start.Column,
		EndLine:     end.Line,
		EndColumn:   end.Column,
	}, nil
}

func (a *analyzer) renderNode(node ast.Node) (string, error) {
	var output bytes.Buffer
	if err := format.Node(&output, a.fset, node); err != nil {
		return "", fmt.Errorf("render normalized signature: %w", err)
	}
	return output.String(), nil
}

func (a *analyzer) renderCanonicalReceiver(receiverType ast.Expr) (string, map[string]string, error) {
	canonical, err := a.cloneRenderedExpression(receiverType, "receiver type")
	if err != nil {
		return "", nil, err
	}
	names, err := canonicalizeReceiverArguments(receiverTypeArguments(receiverType), receiverTypeArguments(canonical))
	if err != nil {
		return "", nil, err
	}
	canonicalText, err := a.renderNode(canonical)
	return canonicalText, names, err
}

func (a *analyzer) cloneRenderedExpression(expression ast.Expr, label string) (ast.Expr, error) {
	rendered, err := a.renderNode(expression)
	if err != nil {
		return nil, err
	}
	canonical, err := parser.ParseExpr(rendered)
	if err != nil {
		return nil, fmt.Errorf("parse normalized %s: %w", label, err)
	}
	return canonical, nil
}

func canonicalizeReceiverArguments(originalArguments, canonicalArguments []ast.Expr) (map[string]string, error) {
	if len(originalArguments) != len(canonicalArguments) {
		return nil, fmt.Errorf("receiver type argument mismatch")
	}
	names := make(map[string]string, len(originalArguments))
	for index, original := range originalArguments {
		originalName, err := receiverTypeParameter(original)
		if err != nil {
			return nil, err
		}
		canonicalName, err := receiverTypeParameter(canonicalArguments[index])
		if err != nil {
			return nil, err
		}
		if _, duplicate := names[originalName.Name]; duplicate {
			return nil, fmt.Errorf("duplicate receiver type parameter %q", originalName.Name)
		}
		replacement := fmt.Sprintf("$R%d", index)
		names[originalName.Name] = replacement
		canonicalName.Name = replacement
	}
	return names, nil
}

func receiverTypeParameter(expression ast.Expr) (*ast.Ident, error) {
	identifier, ok := expression.(*ast.Ident)
	if !ok {
		return nil, fmt.Errorf("receiver type argument must be an identifier")
	}
	return identifier, nil
}

func receiverTypeArguments(expression ast.Expr) []ast.Expr {
	for {
		switch typed := expression.(type) {
		case *ast.ParenExpr:
			expression = typed.X
		case *ast.StarExpr:
			expression = typed.X
		case *ast.IndexExpr:
			return []ast.Expr{typed.Index}
		case *ast.IndexListExpr:
			return typed.Indices
		default:
			return nil
		}
	}
}

func (a *analyzer) renderFunctionSignature(functionType *ast.FuncType, inheritedNames map[string]string) (string, error) {
	typeParameterNames := make(map[string]string, len(inheritedNames))
	for name, replacement := range inheritedNames {
		typeParameterNames[name] = replacement
	}
	typeParameters, err := a.canonicalTypeParameters(functionType.TypeParams, typeParameterNames)
	if err != nil {
		return "", err
	}
	parameters, err := a.canonicalValueFields(functionType.Params, typeParameterNames)
	if err != nil {
		return "", err
	}
	results, err := a.canonicalValueFields(functionType.Results, typeParameterNames)
	if err != nil {
		return "", err
	}
	canonical := *functionType
	canonical.TypeParams = typeParameters
	canonical.Params = parameters
	canonical.Results = results
	return a.renderNode(&canonical)
}

func (a *analyzer) canonicalTypeParameters(fields *ast.FieldList, names map[string]string) (*ast.FieldList, error) {
	if fields == nil {
		return nil, nil
	}
	if err := registerTypeParameterNames(fields, names); err != nil {
		return nil, err
	}
	canonical := &ast.FieldList{}
	for _, field := range fields.List {
		constraint, err := a.cloneSignatureExpression(field.Type, names)
		if err != nil {
			return nil, err
		}
		for _, name := range field.Names {
			canonical.List = append(canonical.List, &ast.Field{
				Names: []*ast.Ident{ast.NewIdent(names[name.Name])},
				Type:  constraint,
			})
		}
	}
	return canonical, nil
}

func registerTypeParameterNames(fields *ast.FieldList, names map[string]string) error {
	index := 0
	for _, field := range fields.List {
		for _, name := range field.Names {
			if _, duplicate := names[name.Name]; duplicate {
				return fmt.Errorf("duplicate type parameter %q", name.Name)
			}
			names[name.Name] = fmt.Sprintf("$T%d", index)
			index++
		}
	}
	return nil
}

func (a *analyzer) canonicalValueFields(fields *ast.FieldList, names map[string]string) (*ast.FieldList, error) {
	if fields == nil {
		return nil, nil
	}
	canonical := &ast.FieldList{}
	for _, field := range fields.List {
		fieldType, err := a.cloneSignatureExpression(field.Type, names)
		if err != nil {
			return nil, err
		}
		repetitions := len(field.Names)
		if repetitions == 0 {
			repetitions = 1
		}
		for range repetitions {
			canonical.List = append(canonical.List, &ast.Field{Type: fieldType})
		}
	}
	return canonical, nil
}

func (a *analyzer) cloneSignatureExpression(expression ast.Expr, names map[string]string) (ast.Expr, error) {
	if ellipsis, variadic := expression.(*ast.Ellipsis); variadic {
		element, err := a.cloneSignatureExpression(ellipsis.Elt, names)
		if err != nil {
			return nil, err
		}
		return &ast.Ellipsis{Elt: element}, nil
	}
	cloned, err := a.cloneRenderedExpression(expression, "signature expression")
	if err != nil {
		return nil, err
	}
	parents := buildParentMap(cloned)
	ast.Inspect(cloned, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.FuncType:
			typed.Params = signatureFieldsWithoutNames(typed.Params)
			typed.Results = signatureFieldsWithoutNames(typed.Results)
		case *ast.Ident:
			if replacement, exists := names[typed.Name]; exists && signatureIdentifierIsType(typed, parents[typed]) {
				typed.Name = replacement
			}
		}
		return true
	})
	return cloned, nil
}

func signatureFieldsWithoutNames(fields *ast.FieldList) *ast.FieldList {
	if fields == nil {
		return nil
	}
	canonical := &ast.FieldList{}
	for _, field := range fields.List {
		repetitions := len(field.Names)
		if repetitions == 0 {
			repetitions = 1
		}
		for range repetitions {
			canonical.List = append(canonical.List, &ast.Field{Type: field.Type})
		}
	}
	return canonical
}

func signatureIdentifierIsType(identifier *ast.Ident, parent ast.Node) bool {
	if selector, ok := parent.(*ast.SelectorExpr); ok && selector.Sel == identifier {
		return false
	}
	if field, ok := parent.(*ast.Field); ok {
		for _, name := range field.Names {
			if name == identifier {
				return false
			}
		}
	}
	return true
}

func cyclomaticComplexity(body *ast.BlockStmt) int64 {
	complexity := int64(1)
	ast.Inspect(body, func(node ast.Node) bool {
		if _, nested := node.(*ast.FuncLit); nested {
			return false
		}
		switch typed := node.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.CaseClause, *ast.CommClause:
			complexity++
		case *ast.BinaryExpr:
			if typed.Op == token.LAND || typed.Op == token.LOR {
				complexity++
			}
		}
		return true
	})
	return complexity
}

func buildParentMap(root ast.Node) map[ast.Node]ast.Node {
	parents := make(map[ast.Node]ast.Node)
	stack := make([]ast.Node, 0)
	ast.Inspect(root, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) > 0 {
			parents[node] = stack[len(stack)-1]
		}
		stack = append(stack, node)
		return true
	})
	return parents
}

func literalBindingTarget(literal *ast.FuncLit, parents map[ast.Node]ast.Node) ast.Expr {
	child := ast.Node(literal)
	for parent := parents[child]; parent != nil; parent = parents[parent] {
		switch typed := parent.(type) {
		case *ast.ParenExpr:
			child = parent
			continue
		case *ast.AssignStmt:
			return assignmentTarget(child, typed)
		case *ast.ValueSpec:
			return valueSpecTarget(child, typed)
		default:
			return nil
		}
	}
	return nil
}

func assignmentTarget(child ast.Node, assignment *ast.AssignStmt) ast.Expr {
	for index, expression := range assignment.Rhs {
		if expression != child || index >= len(assignment.Lhs) {
			continue
		}
		return assignment.Lhs[index]
	}
	return nil
}

func valueSpecTarget(child ast.Node, specification *ast.ValueSpec) ast.Expr {
	for index, expression := range specification.Values {
		if expression == child && index < len(specification.Names) {
			return specification.Names[index]
		}
	}
	return nil
}

func literalBindingHint(literal *ast.FuncLit, parents map[ast.Node]ast.Node) string {
	target := literalBindingTarget(literal, parents)
	switch typed := target.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.SelectorExpr:
		return typed.Sel.Name
	default:
		return ""
	}
}
