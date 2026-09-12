package orchestrator

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hwain-hwang/sentinel-go/internal/crap"
)

// CrapResultRow is the privacy-safe public result for one callable.
type CrapResultRow struct {
	CallableID    string `json:"callableId"`
	Complexity    int64  `json:"cyclomaticComplexity"`
	CoveredUnits  int64  `json:"coveredUnits,omitempty"`
	TotalUnits    int64  `json:"totalUnits,omitempty"`
	Numerator     string `json:"numerator,omitempty"`
	Denominator   string `json:"denominator,omitempty"`
	Decimal       string `json:"decimal,omitempty"`
	Pass          bool   `json:"pass"`
	UnknownReason string `json:"unknownReason,omitempty"`
}

type CrapReport struct {
	Pass bool            `json:"pass"`
	Rows []CrapResultRow `json:"rows"`
}

type analyzedCrapRow struct {
	order crap.CrapRow
	row   CrapResultRow
}

// AnalyzeCrap joins the complete production source inventory to one validated
// Go coverprofile and applies the exact CRAP formula to every callable.
func AnalyzeCrap(projectRoot, coverProfilePath string) (CrapReport, error) {
	modulePath, err := modulePathFromFile(filepath.Join(projectRoot, "go.mod"))
	if err != nil {
		return CrapReport{}, err
	}
	profileFile, err := os.Open(coverProfilePath)
	if err != nil {
		return CrapReport{}, fmt.Errorf("open coverprofile: %w", err)
	}
	defer profileFile.Close()
	profile, err := crap.ParseCoverProfile(profileFile, modulePath)
	if err != nil {
		return CrapReport{}, fmt.Errorf("parse coverprofile: %w", err)
	}
	sources, err := productionGoSources(projectRoot)
	if err != nil {
		return CrapReport{}, err
	}
	return analyzeCrapSources(projectRoot, sources, profile)
}

func analyzeCrapSources(projectRoot string, sources []string, profile *crap.CoverProfile) (CrapReport, error) {
	all := make([]analyzedCrapRow, 0)
	for _, source := range sources {
		rows, err := analyzeCrapSource(projectRoot, source, profile)
		if err != nil {
			return CrapReport{}, err
		}
		all = append(all, rows...)
	}
	return orderCrapReport(all)
}

func analyzeCrapSource(projectRoot, source string, profile *crap.CoverProfile) ([]analyzedCrapRow, error) {
	payload, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(source)))
	if err != nil {
		return nil, fmt.Errorf("read Go source: %w", err)
	}
	callables, err := crap.AnalyzeSource(payload, source)
	if err != nil {
		return nil, err
	}
	rows := make([]analyzedCrapRow, 0, len(callables))
	for _, callable := range callables {
		rows = append(rows, scoreCallable(profile, callable))
	}
	return rows, nil
}

func scoreCallable(profile *crap.CoverProfile, callable crap.Callable) analyzedCrapRow {
	covered, total, unknown := profile.Counts(callable.ModulePath, callable.SourceRange, callable.ExcludedRanges)
	row := CrapResultRow{CallableID: callable.CallableID, Complexity: callable.Complexity, CoveredUnits: covered, TotalUnits: total}
	order := crap.CrapRow{ModuleRelativePath: callable.ModulePath, SourceStartByte: callable.SourceRange.StartByte, CallableID: callable.CallableID}
	if unknown != "" {
		row.UnknownReason = unknown
		order.UnknownReason = unknown
		return analyzedCrapRow{order: order, row: row}
	}
	exact, err := crap.Calculate(callable.Complexity, covered, total)
	if err != nil {
		row.UnknownReason = "invalidCoverageCounts"
		order.UnknownReason = row.UnknownReason
		return analyzedCrapRow{order: order, row: row}
	}
	row.Numerator = exact.Numerator
	row.Denominator = exact.Denominator
	row.Decimal = exact.Decimal
	row.Pass = exact.Pass
	order.Numerator = exact.Numerator
	order.Denominator = exact.Denominator
	return analyzedCrapRow{order: order, row: row}
}

func orderCrapReport(rows []analyzedCrapRow) (CrapReport, error) {
	orderRows := make([]crap.CrapRow, len(rows))
	byID := make(map[string]CrapResultRow, len(rows))
	for index, row := range rows {
		orderRows[index] = row.order
		byID[row.row.CallableID] = row.row
	}
	ordered, err := crap.SortRows(orderRows)
	if err != nil {
		return CrapReport{}, err
	}
	report := CrapReport{Pass: len(ordered) > 0, Rows: make([]CrapResultRow, 0, len(ordered))}
	for _, row := range ordered {
		public := byID[row.CallableID]
		report.Rows = append(report.Rows, public)
		if !public.Pass || public.UnknownReason != "" {
			report.Pass = false
		}
	}
	return report, nil
}

func productionGoSources(projectRoot string) ([]string, error) {
	inventory := &productionInventory{root: projectRoot}
	err := filepath.WalkDir(projectRoot, inventory.visit)
	if err != nil {
		return nil, err
	}
	sort.Strings(inventory.sources)
	return inventory.sources, nil
}

type productionInventory struct {
	root    string
	sources []string
}

func (inventory *productionInventory) visit(path string, entry fs.DirEntry, walkError error) error {
	if walkError != nil {
		return walkError
	}
	relative, err := filepath.Rel(inventory.root, path)
	if err != nil || relative == "." {
		return err
	}
	if entry.Type()&os.ModeSymlink != 0 {
		return fmt.Errorf("source inventory contains symbolic link")
	}
	if entry.IsDir() {
		return sourceDirectoryDecision(path)
	}
	if isProductionGoFile(relative, entry.Type()) {
		inventory.sources = append(inventory.sources, filepath.ToSlash(relative))
	}
	return nil
}

func sourceDirectoryDecision(path string) error {
	if excludedSourceDirectory(filepath.Base(path)) {
		return filepath.SkipDir
	}
	return nil
}

func isProductionGoFile(relative string, mode fs.FileMode) bool {
	return mode.IsRegular() && strings.HasSuffix(relative, ".go") && !strings.HasSuffix(relative, "_test.go")
}

func excludedSourceDirectory(name string) bool {
	switch name {
	case ".git", ".sentinel", ".toolchain", "target", "vendor", "third_party", "testdata":
		return true
	default:
		return false
	}
}

func modulePathFromFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open go.mod: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "module" {
			return fields[1], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("go.mod module directive missing")
}
