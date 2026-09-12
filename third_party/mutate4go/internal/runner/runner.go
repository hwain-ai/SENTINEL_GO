package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/unclebob/mutate4go/internal/cli"
	"github.com/unclebob/mutate4go/internal/coverage"
	"github.com/unclebob/mutate4go/internal/manifest"
	"github.com/unclebob/mutate4go/internal/mutations"
)

const CoverageProfile = "target/coverage/coverage.out"

type Result struct {
	Site     mutations.Site
	Status   string
	Duration time.Duration
}

func verbosef(options cli.Options, format string, args ...any) {
	if !options.Verbose {
		return
	}
	fmt.Fprintf(os.Stderr, "verbose: "+format+"\n", args...)
}

func resultString(err error) string {
	if err == nil {
		return "ok"
	}
	return fmt.Sprintf("error=%q", err.Error())
}

func optionsSummary(options cli.Options) string {
	return fmt.Sprintf(
		"source=%q scan=%t update_manifest=%t reuse_coverage=%t lines=%s since_last_run=%t mutate_all=%t mutation_warning=%d timeout_factor=%d test_command=%q max_workers=%d verbose=%t help=%t error=%q",
		options.SourcePath,
		options.Scan,
		options.UpdateManifest,
		options.ReuseCoverage,
		linesSummary(options.Lines),
		options.SinceLastRun,
		options.MutateAll,
		options.MutationWarning,
		options.TimeoutFactor,
		options.TestCommand,
		options.MaxWorkers,
		options.Verbose,
		options.Help,
		options.Error,
	)
}

func linesSummary(lines map[int]bool) string {
	if lines == nil {
		return "all"
	}
	values := make([]int, 0, len(lines))
	for line := range lines {
		values = append(values, line)
	}
	sort.Ints(values)
	parts := make([]string, 0, len(values))
	for _, line := range values {
		parts = append(parts, fmt.Sprint(line))
	}
	return strings.Join(parts, ",")
}

func resultsSummary(results []Result) string {
	counts := map[string]int{}
	for _, result := range results {
		counts[result.Status]++
	}
	return fmt.Sprintf("total=%d killed=%d survived=%d timeout=%d", len(results), counts["killed"], counts["survived"], counts["timeout"])
}

func Run(options cli.Options) (err error) {
	verbosef(options, "run start args=%q options=%s", options.Args, optionsSummary(options))
	defer func() {
		verbosef(options, "run finish result=%s", resultString(err))
	}()
	if options.Help {
		verbosef(options, "print help start")
		fmt.Print(cli.UsageSummary)
		verbosef(options, "print help finish result=ok")
		return nil
	}
	if options.Error != "" {
		return fmt.Errorf("%s\n\n%s", options.Error, cli.UsageSummary)
	}
	verbosef(options, "restore backup start source=%q", options.SourcePath)
	restored, err := manifest.RestoreBackup(options.SourcePath)
	if err != nil {
		return err
	}
	verbosef(options, "restore backup finish restored=%t result=ok", restored)
	if restored {
		fmt.Println("Restored source from backup (previous run was interrupted).")
	}
	if options.Scan {
		verbosef(options, "scan start source=%q warning=%d", options.SourcePath, options.MutationWarning)
		err = Scan(options.SourcePath, options.MutationWarning, options.Verbose)
		verbosef(options, "scan finish result=%s", resultString(err))
		return err
	}
	if options.UpdateManifest {
		verbosef(options, "update manifest start source=%q", options.SourcePath)
		err = UpdateManifest(options.SourcePath, options.Verbose)
		verbosef(options, "update manifest finish result=%s", resultString(err))
		return err
	}
	return Mutate(options)
}

func Scan(sourcePath string, warning int, verbose ...bool) error {
	options := cli.Options{Verbose: len(verbose) > 0 && verbose[0]}
	verbosef(options, "discover mutations start source=%q", sourcePath)
	sites, functions, err := mutations.Discover(sourcePath)
	if err != nil {
		return err
	}
	verbosef(options, "discover mutations finish sites=%d functions=%d result=ok", len(sites), len(functions))
	verbosef(options, "read manifest start source=%q", sourcePath)
	previous, hasManifest, current, err := currentManifest(sourcePath, functions)
	if err != nil {
		return err
	}
	changed := manifest.ChangedFunctionIDs(previous, current)
	changedCount := countChangedSites(sites, changed)
	verbosef(options, "read manifest finish has_manifest=%t changed_functions=%d changed_sites=%d result=ok", hasManifest, len(changed), changedCount)
	fmt.Printf("Mutation scan: %s\n", sourcePath)
	fmt.Printf("Total mutation sites: %d\n", len(sites))
	fmt.Printf("Changed mutation sites: %d\n", changedCount)
	fmt.Printf("Manifest exists: %t\n", hasManifest)
	if len(sites) > warning {
		fmt.Printf("Warning: %d mutation sites exceeds threshold %d.\n", len(sites), warning)
	}
	return nil
}

func UpdateManifest(sourcePath string, verbose ...bool) error {
	options := cli.Options{Verbose: len(verbose) > 0 && verbose[0]}
	verbosef(options, "read source start path=%q", sourcePath)
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	verbosef(options, "read source finish bytes=%d result=ok", len(content))
	clean := manifest.Strip(string(content))
	verbosef(options, "discover mutations start source=%q", sourcePath)
	_, functions, err := mutations.Discover(sourcePath)
	if err != nil {
		return err
	}
	verbosef(options, "discover mutations finish functions=%d result=ok", len(functions))
	verbosef(options, "embed manifest start source=%q functions=%d", sourcePath, len(functions))
	next := manifest.Build(functions, clean, time.Now())
	embedded, err := manifest.Embed(clean, next)
	if err != nil {
		return err
	}
	verbosef(options, "embed manifest finish bytes=%d result=ok", len(embedded))
	verbosef(options, "write manifest start source=%q bytes=%d", sourcePath, len(embedded))
	if err := os.WriteFile(sourcePath, []byte(embedded), 0o644); err != nil {
		verbosef(options, "write manifest finish result=%s", resultString(err))
		return err
	}
	verbosef(options, "write manifest finish result=ok")
	fmt.Println("Updated manifest: " + sourcePath)
	return nil
}

func Mutate(options cli.Options) error {
	verbosef(options, "mutate start source=%q", options.SourcePath)
	verbosef(options, "read source start path=%q", options.SourcePath)
	originalBytes, err := os.ReadFile(options.SourcePath)
	if err != nil {
		return err
	}
	verbosef(options, "read source finish bytes=%d result=ok", len(originalBytes))
	analysisContent := manifest.Strip(string(originalBytes))
	if analysisContent != string(originalBytes) {
		verbosef(options, "strip manifest start source=%q original_bytes=%d stripped_bytes=%d", options.SourcePath, len(originalBytes), len(analysisContent))
		if err := os.WriteFile(options.SourcePath, []byte(analysisContent), 0o644); err != nil {
			return err
		}
		verbosef(options, "strip manifest finish result=ok")
	}
	verbosef(options, "discover mutations start source=%q", options.SourcePath)
	sites, functions, err := mutations.Discover(options.SourcePath)
	if err != nil {
		return err
	}
	verbosef(options, "discover mutations finish sites=%d functions=%d result=ok", len(sites), len(functions))
	verbosef(options, "build manifest start source=%q", options.SourcePath)
	previous, hasManifest := manifest.Extract(string(originalBytes))
	current := manifest.Build(functions, analysisContent, time.Now())
	changed := manifest.ChangedFunctionIDs(previous, current)
	verbosef(options, "build manifest finish has_manifest=%t changed_functions=%d result=ok", hasManifest, len(changed))
	profile, err := ensureCoverage(options)
	if err != nil {
		return err
	}
	verbosef(options, "partition coverage start source=%q sites=%d", options.SourcePath, len(sites))
	covered, uncovered := partitionByCoverage(profile, options.SourcePath, sites)
	effectiveSinceLastRun := options.SinceLastRun || (hasManifest && !options.MutateAll && options.Lines == nil)
	selected := selectSites(covered, options.Lines, effectiveSinceLastRun, changed)
	verbosef(options, "partition coverage finish covered=%d uncovered=%d selected=%d effective_since_last_run=%t result=ok", len(covered), len(uncovered), len(selected), effectiveSinceLastRun)
	printHeader(options, sites, covered, uncovered, selected, hasManifest, changed)
	if len(uncovered) > 0 && options.Lines == nil && !effectiveSinceLastRun {
		printUncovered(uncovered)
	}
	baselineDuration, err := baseline(options.TestCommand, options.Verbose)
	if err != nil {
		return fmt.Errorf("baseline failed: %w", err)
	}
	timeout := time.Duration(options.TimeoutFactor) * baselineDuration
	if timeout < time.Second {
		timeout = time.Second
	}
	verbosef(options, "mutation timeout computed timeout=%s factor=%d baseline=%s", timeout, options.TimeoutFactor, baselineDuration)
	verbosef(options, "save backup start source=%q", options.SourcePath)
	if err := manifest.SaveBackup(options.SourcePath, analysisContent); err != nil {
		return err
	}
	verbosef(options, "save backup finish result=ok")
	defer func() {
		verbosef(options, "cleanup backup start source=%q", options.SourcePath)
		err := manifest.CleanupBackup(options.SourcePath)
		verbosef(options, "cleanup backup finish result=%s", resultString(err))
	}()
	results, err := runMutations(options.SourcePath, analysisContent, selected, timeout, options.TestCommand, options.MaxWorkers, options.Verbose)
	if err != nil {
		return err
	}
	verbosef(options, "restore source start path=%q bytes=%d", options.SourcePath, len(analysisContent))
	if err := os.WriteFile(options.SourcePath, []byte(analysisContent), 0o644); err != nil {
		return err
	}
	verbosef(options, "restore source finish result=ok")
	summarize(results, uncovered)
	verbosef(options, "embed manifest start source=%q functions=%d", options.SourcePath, len(current.Functions))
	embedded, err := manifest.Embed(analysisContent, current)
	if err != nil {
		return err
	}
	verbosef(options, "embed manifest finish bytes=%d result=ok", len(embedded))
	verbosef(options, "write manifest start source=%q bytes=%d", options.SourcePath, len(embedded))
	err = os.WriteFile(options.SourcePath, []byte(embedded), 0o644)
	verbosef(options, "write manifest finish result=%s", resultString(err))
	return err
}

func ensureCoverage(options cli.Options) (map[string][]coverage.Segment, error) {
	if options.ReuseCoverage {
		verbosef(options, "load coverage start path=%q reuse=true", CoverageProfile)
		profile, err := coverage.LoadProfile(CoverageProfile)
		if err != nil {
			return nil, err
		}
		if profile == nil {
			return nil, fmt.Errorf("Error: --reuse-coverage was requested, but %s does not exist.\nRun without --reuse-coverage once to generate coverage", CoverageProfile)
		}
		fmt.Println("Reusing existing coverage; covered/uncovered classification may be stale.")
		verbosef(options, "load coverage finish files=%d result=ok", len(profile))
		return profile, nil
	}
	verbosef(options, "prepare coverage directory start path=%q", filepath.Dir(CoverageProfile))
	if err := os.RemoveAll(filepath.Dir(CoverageProfile)); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(CoverageProfile), 0o755); err != nil {
		return nil, err
	}
	verbosef(options, "prepare coverage directory finish result=ok")
	verbosef(options, "coverage command start command=%q profile=%q", coverageCommand(options.TestCommand), CoverageProfile)
	cmd := exec.Command("sh", "-c", coverageCommand(options.TestCommand))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		verbosef(options, "coverage command finish result=%s", resultString(err))
		return nil, fmt.Errorf("coverage failed: %w", err)
	}
	verbosef(options, "coverage command finish result=ok")
	verbosef(options, "load coverage start path=%q reuse=false", CoverageProfile)
	profile, err := coverage.LoadProfile(CoverageProfile)
	verbosef(options, "load coverage finish files=%d result=%s", len(profile), resultString(err))
	return profile, err
}

func coverageCommand(testCommand string) string {
	return testCommand + " -coverprofile=" + CoverageProfile
}

func baseline(command string, verbose bool) (time.Duration, error) {
	verbosef(cli.Options{Verbose: verbose}, "baseline start command=%q", command)
	start := time.Now()
	cmd := exec.Command("sh", "-c", command)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	duration := time.Since(start)
	verbosef(cli.Options{Verbose: verbose}, "baseline finish duration=%s result=%s", duration, resultString(err))
	return duration, err
}

func runMutations(sourcePath, original string, sites []mutations.Site, timeout time.Duration, testCommand string, maxWorkers int, verbose bool) ([]Result, error) {
	verbosef(cli.Options{Verbose: verbose}, "run mutations start source=%q sites=%d timeout=%s command=%q max_workers=%d", sourcePath, len(sites), timeout, testCommand, maxWorkers)
	if maxWorkers <= 1 || len(sites) <= 1 {
		results, err := runMutationsSerial(sourcePath, original, sites, timeout, testCommand, verbose)
		verbosef(cli.Options{Verbose: verbose}, "run mutations finish mode=serial results=%s result=%s", resultsSummary(results), resultString(err))
		return results, err
	}
	results, err := runMutationsParallel(sourcePath, original, sites, timeout, testCommand, maxWorkers, verbose)
	verbosef(cli.Options{Verbose: verbose}, "run mutations finish mode=parallel results=%s result=%s", resultsSummary(results), resultString(err))
	return results, err
}

func runMutationsSerial(sourcePath, original string, sites []mutations.Site, timeout time.Duration, testCommand string, verbose bool) ([]Result, error) {
	var results []Result
	total := len(sites)
	for i, site := range sites {
		verbosef(cli.Options{Verbose: verbose}, "mutation start mode=serial index=%d total=%d line=%d description=%q function=%q command=%q timeout=%s", i+1, total, site.Line, site.Description, site.FunctionID, testCommand, timeout)
		mutated := mutations.Apply(original, site)
		if err := os.WriteFile(sourcePath, []byte(mutated), 0o644); err != nil {
			return nil, err
		}
		start := time.Now()
		status := runMutant(testCommand, timeout, "", verbose)
		result := Result{Site: site, Status: status, Duration: time.Since(start)}
		results = append(results, result)
		if err := os.WriteFile(sourcePath, []byte(original), 0o644); err != nil {
			return nil, err
		}
		verbosef(cli.Options{Verbose: verbose}, "mutation finish mode=serial index=%d total=%d status=%q duration=%s result=ok", i+1, total, status, result.Duration)
		fmt.Printf("[%d/%d] %s line %d %s: %s\n", i+1, total, status, site.Line, site.Description, site.FunctionID)
	}
	return results, nil
}

func runMutationsParallel(sourcePath, original string, sites []mutations.Site, timeout time.Duration, testCommand string, maxWorkers int, verbose bool) ([]Result, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	absSource, err := filepath.Abs(sourcePath)
	if err != nil {
		return nil, err
	}
	relSource, err := filepath.Rel(root, absSource)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(relSource, ".."+string(os.PathSeparator)) || relSource == ".." || filepath.IsAbs(relSource) {
		return nil, fmt.Errorf("source file must be inside working directory for parallel mutation: %s", sourcePath)
	}

	if maxWorkers > len(sites) {
		maxWorkers = len(sites)
	}
	runRoot := filepath.Join(root, "target", "mutation-workers", fmt.Sprintf("run-%d-%d", os.Getpid(), time.Now().UnixNano()))
	defer os.RemoveAll(runRoot)
	verbosef(cli.Options{Verbose: verbose}, "prepare workers start root=%q run_root=%q workers=%d", root, runRoot, maxWorkers)

	type job struct {
		Number int
		Site   mutations.Site
	}
	type worker struct {
		Root       string
		SourcePath string
	}
	workers := make([]worker, maxWorkers)
	for i := range workers {
		workerRoot := filepath.Join(runRoot, fmt.Sprintf("worker-%d", i+1))
		if err := copyProject(root, workerRoot); err != nil {
			return nil, err
		}
		workers[i] = worker{
			Root:       workerRoot,
			SourcePath: filepath.Join(workerRoot, relSource),
		}
	}
	verbosef(cli.Options{Verbose: verbose}, "prepare workers finish workers=%d result=ok", len(workers))

	jobs := make(chan job, len(sites))
	for i, site := range sites {
		jobs <- job{Number: i + 1, Site: site}
	}
	close(jobs)

	results := make(chan Result, len(sites))
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	for i, w := range workers {
		wg.Add(1)
		go func(workerNumber int, w worker) {
			defer wg.Done()
			for job := range jobs {
				verbosef(cli.Options{Verbose: verbose}, "mutation start mode=parallel worker=%d index=%d total=%d line=%d description=%q function=%q command=%q timeout=%s", workerNumber, job.Number, len(sites), job.Site.Line, job.Site.Description, job.Site.FunctionID, testCommand, timeout)
				mutated := mutations.Apply(original, job.Site)
				if err := os.WriteFile(w.SourcePath, []byte(mutated), 0o644); err != nil {
					sendFirstError(errs, err)
					return
				}
				start := time.Now()
				status := runMutant(testCommand, timeout, w.Root, verbose)
				if err := os.WriteFile(w.SourcePath, []byte(original), 0o644); err != nil {
					sendFirstError(errs, err)
					return
				}
				result := Result{Site: job.Site, Status: status, Duration: time.Since(start)}
				results <- result
				verbosef(cli.Options{Verbose: verbose}, "mutation finish mode=parallel worker=%d index=%d total=%d status=%q duration=%s result=ok", workerNumber, job.Number, len(sites), status, result.Duration)
				fmt.Printf("[%d/%d] worker-%d %s line %d %s: %s\n", job.Number, len(sites), workerNumber, status, job.Site.Line, job.Site.Description, job.Site.FunctionID)
			}
		}(i+1, w)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	collected := make([]Result, 0, len(sites))
	for result := range results {
		collected = append(collected, result)
	}
	select {
	case err := <-errs:
		return nil, err
	default:
	}
	if len(collected) != len(sites) {
		return nil, fmt.Errorf("mutation workers stopped after %d/%d results", len(collected), len(sites))
	}
	return sortResults(collected), nil
}

func sendFirstError(errs chan<- error, err error) {
	select {
	case errs <- err:
	default:
	}
}

func sortResults(results []Result) []Result {
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Site.Index < results[j].Site.Index
	})
	return results
}

func runMutant(command string, timeout time.Duration, dir string, verbose bool) string {
	verbosef(cli.Options{Verbose: verbose}, "test command start command=%q timeout=%s dir=%q", command, timeout, dir)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		verbosef(cli.Options{Verbose: verbose}, "test command finish status=timeout result=%s", resultString(err))
		return "timeout"
	}
	if err != nil {
		verbosef(cli.Options{Verbose: verbose}, "test command finish status=killed result=%s", resultString(err))
		return "killed"
	}
	verbosef(cli.Options{Verbose: verbose}, "test command finish status=survived result=ok")
	return "survived"
}

func copyProject(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if shouldSkipCopy(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		default:
			return nil
		}
	})
}

func shouldSkipCopy(rel string) bool {
	for _, dir := range []string{".git", ".gocache", ".gomodcache", ".tools", "target"} {
		if rel == dir || strings.HasPrefix(rel, dir+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func partitionByCoverage(profile map[string][]coverage.Segment, sourcePath string, sites []mutations.Site) ([]mutations.Site, []mutations.Site) {
	var covered []mutations.Site
	var uncovered []mutations.Site
	for _, site := range sites {
		if coverage.Covered(profile, sourcePath, site.Line) {
			covered = append(covered, site)
		} else {
			uncovered = append(uncovered, site)
		}
	}
	return covered, uncovered
}

func selectSites(sites []mutations.Site, lines map[int]bool, sinceLastRun bool, changed map[string]bool) []mutations.Site {
	var selected []mutations.Site
	for _, site := range sites {
		if lines != nil && !lines[site.Line] {
			continue
		}
		if sinceLastRun && !changed[site.FunctionID] {
			continue
		}
		selected = append(selected, site)
	}
	return selected
}

func currentManifest(sourcePath string, functions []mutations.Function) (*manifest.Manifest, bool, manifest.Manifest, error) {
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, false, manifest.Manifest{}, err
	}
	clean := manifest.Strip(string(content))
	previous, exists := manifest.Extract(string(content))
	return previous, exists, manifest.Build(functions, clean, time.Now()), nil
}

func countChangedSites(sites []mutations.Site, changed map[string]bool) int {
	n := 0
	for _, site := range sites {
		if changed[site.FunctionID] {
			n++
		}
	}
	return n
}

func printHeader(options cli.Options, all, covered, uncovered, selected []mutations.Site, hasManifest bool, changed map[string]bool) {
	fmt.Printf("Mutation run: %s\n", options.SourcePath)
	fmt.Printf("Total mutation sites: %d\n", len(all))
	fmt.Printf("Covered mutation sites: %d\n", len(covered))
	fmt.Printf("Uncovered mutation sites: %d\n", len(uncovered))
	fmt.Printf("Changed mutation sites: %d\n", countChangedSites(all, changed))
	fmt.Printf("Manifest exists: %t\n", hasManifest)
	fmt.Printf("Selected mutation sites: %d\n", len(selected))
	if len(all) > options.MutationWarning {
		fmt.Printf("Warning: %d mutation sites exceeds threshold %d.\n", len(all), options.MutationWarning)
	}
	if options.MaxWorkers > 0 {
		fmt.Printf("Mutation workers: %d\n", options.MaxWorkers)
	}
}

func printUncovered(sites []mutations.Site) {
	fmt.Println("Uncovered mutations:")
	for _, site := range sites {
		fmt.Printf("  line %d %s %s\n", site.Line, site.Description, site.FunctionID)
	}
}

func summarize(results []Result, uncovered []mutations.Site) {
	counts := map[string]int{}
	for _, result := range results {
		counts[result.Status]++
	}
	keys := []string{"killed", "survived", "timeout"}
	sort.Strings(keys)
	fmt.Println()
	fmt.Println("Mutation Report")
	fmt.Println("===============")
	fmt.Printf("Killed: %d\n", counts["killed"]+counts["timeout"])
	fmt.Printf("Survived: %d\n", counts["survived"])
	fmt.Printf("Uncovered: %d\n", len(uncovered))
	if counts["survived"] > 0 {
		fmt.Println()
		fmt.Println("Survivors:")
		for _, result := range results {
			if result.Status == "survived" {
				fmt.Printf("  line %d %s %s\n", result.Site.Line, result.Site.Description, result.Site.FunctionID)
			}
		}
	}
}

func StatusCode(err error) int {
	if err == nil {
		return 0
	}
	if strings.Contains(err.Error(), cli.UsageSummary) {
		return 1
	}
	return 1
}
