// Package gomutesting connects pinned external mutation operators to SENTINEL.
// The initial, explicit operator profile is experimental, not a strict backend.
package gomutesting

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/avito-tech/go-mutesting/mutator"
	"github.com/avito-tech/go-mutesting/mutator/expression"
	"github.com/avito-tech/go-mutesting/mutator/numbers"
)

const Version = "v0.0.0-20251226130216-48d0401f00fb"
const Profile = "comparison-and-numbers-v1"
const maximumSourceBytes = 4 * 1024 * 1024
const maximumPlanBytes = 32 * 1024 * 1024

// Candidate exposes identity and physical source location, never source bytes.
type Candidate struct {
	ID          string `json:"id"`
	SourceFile  string `json:"sourceFile"`
	Line        int    `json:"line"`
	Column      int    `json:"column"`
	Operator    string `json:"operator"`
	replacement []byte
}

type namedOperator struct {
	name   string
	mutate mutator.Mutator
}

// Only these upstream functions are selected; their mutation rules are not copied.
var operators = []namedOperator{
	{"expression/comparison", expression.MutatorComparison},
	{"numbers/decrementer", numbers.MutatorNumbersDecrementer},
	{"numbers/incrementer", numbers.MutatorNumbersIncrementer},
}

type planner struct {
	file   string
	source []byte
	tokens *token.FileSet
	tree   *ast.File
	sites  []Candidate
	bytes  int
	err    error
}

// Discover inventories every mutation in this profile, or rejects the whole plan.
func Discover(file string, source []byte) ([]Candidate, error) {
	if !validSourcePath(file) {
		return nil, fmt.Errorf("goMutestingSourceInvalid")
	}
	if len(source) > maximumSourceBytes {
		return nil, fmt.Errorf("goMutestingSourceTooLarge")
	}
	tokens := token.NewFileSet()
	tree, err := parser.ParseFile(tokens, file, source, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("goMutestingSourceInvalid")
	}
	p := &planner{file: file, source: source, tokens: tokens, tree: tree, sites: []Candidate{}}
	ast.Inspect(tree, p.visit)
	if p.err != nil {
		return nil, p.err
	}
	return p.sites, nil
}

func validSourcePath(file string) bool {
	return utf8.ValidString(file) && file != "" && path.Clean(file) == file && !path.IsAbs(file) &&
		!strings.Contains(file, "\\") && !strings.HasPrefix(file, "../") &&
		strings.HasSuffix(file, ".go") && !strings.HasSuffix(file, "_test.go")
}

func (p *planner) visit(node ast.Node) bool {
	if node == nil || p.err != nil {
		return false
	}
	for _, operator := range operators {
		for ordinal, change := range operator.mutate(nil, nil, node) {
			if err := p.add(node, operator.name, ordinal, change); err != nil {
				p.err = err
				return false
			}
		}
	}
	return true
}

func (p *planner) add(node ast.Node, operator string, ordinal int, change mutator.Mutation) error {
	change.Change()
	defer change.Reset()
	var buffer bytes.Buffer
	if err := format.Node(&buffer, p.tokens, p.tree); err != nil {
		return fmt.Errorf("goMutestingRenderFailed")
	}
	p.bytes += buffer.Len()
	if p.bytes > maximumPlanBytes {
		return fmt.Errorf("goMutestingPlanTooLarge")
	}
	position := p.tokens.PositionFor(node.Pos(), false)
	replacement := buffer.Bytes()
	id := candidateID(p.file, p.source, replacement, operator, position.Offset, ordinal)
	p.sites = append(p.sites, Candidate{id, p.file, position.Line, position.Column, operator, replacement})
	return nil
}

func candidateID(file string, original, replacement []byte, operator string, offset, ordinal int) string {
	hash := sha256.New()
	parts := []string{Profile, file, operator, strconv.Itoa(offset), strconv.Itoa(ordinal), digest(original), digest(replacement)}
	for _, part := range parts {
		fmt.Fprintf(hash, "%d:%s", len(part), part)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func digest(payload []byte) string {
	value := sha256.Sum256(payload)
	return hex.EncodeToString(value[:])
}
