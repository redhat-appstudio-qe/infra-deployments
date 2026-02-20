// Command render-diff computes and displays the kustomize render delta for
// components affected by the current branch's changes.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	charmlog "github.com/charmbracelet/log"
	"golang.org/x/term"

	"github.com/redhat-appstudio/infra-deployments/infra-tools/internal/appset"
	"github.com/redhat-appstudio/infra-deployments/infra-tools/internal/detector"
	"github.com/redhat-appstudio/infra-deployments/infra-tools/internal/git"
	"github.com/redhat-appstudio/infra-deployments/infra-tools/internal/renderdiff"
)

// version is set via -ldflags at build time.
var version = "dev"

func main() {
	var (
		repoRoot    = flag.String("repo-root", ".", "Path to the repository root")
		baseRef     = flag.String("base-ref", "", "Base git ref to compare against (default: merge-base with main)")
		overlaysDir = flag.String("overlays-dir", "argo-cd-apps/overlays", "Path to overlays directory relative to repo root")
		color       = flag.Bool("color", false, "Force color output")
		noColor     = flag.Bool("no-color", false, "Disable color output")
		openDiff    = flag.Bool("open", false, "Open diffs in $DIFFTOOL or git difftool")
		outputDir   = flag.String("output-dir", "", "Write per-component .diff files to this directory")
		outputMode  = flag.String("output-mode", "local", "Output mode: local, ci-summary, ci-comment, ci-artifact-dir")
		showVersion = flag.Bool("version", false, "Print version and exit")
		logFile     = flag.String("log-file", "", "Write debug-level logs to this file")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("render-diff %s\n", version)
		os.Exit(0)
	}

	// Set up logging
	logCleanup, err := setupLogging(*logFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to set up logging: %v\n", err)
		os.Exit(1)
	}
	if logCleanup != nil {
		defer logCleanup()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	absRepoRoot, err := filepath.Abs(*repoRoot)
	if err != nil {
		fatal("resolving repo root", "err", err)
	}

	// Resolve base ref: default to merge-base with main
	effectiveBaseRef := *baseRef
	if effectiveBaseRef == "" {
		effectiveBaseRef, err = git.MergeBase(ctx, absRepoRoot, "main")
		if err != nil {
			slog.Warn("could not compute merge-base with main, falling back to 'main'", "err", err)
			effectiveBaseRef = "main"
		}
	}

	headSHA, err := git.ResolveRef(ctx, absRepoRoot, "HEAD")
	if err != nil {
		fatal("resolving HEAD", "err", err)
	}
	baseSHA, err := git.ResolveRef(ctx, absRepoRoot, effectiveBaseRef)
	if err != nil {
		fatal("resolving base ref", "err", err)
	}

	slog.Info("Comparing refs", "head", headSHA, "base", baseSHA)

	// Step 1: Get changed files
	changedFiles, err := git.ChangedFiles(ctx, absRepoRoot, effectiveBaseRef)
	if err != nil {
		fatal("getting changed files", "err", err)
	}
	if len(changedFiles) == 0 {
		fmt.Println("No changed files detected — nothing to diff.")
		return
	}
	slog.Info("Changed files detected", "count", len(changedFiles))

	// Step 2: Create worktree at base ref
	worktreePath, cleanup, err := git.CreateWorktree(ctx, absRepoRoot, effectiveBaseRef)
	if err != nil {
		fatal("creating worktree", "err", err)
	}
	defer cleanup()

	headRef := detector.NewRepoRef(absRepoRoot)
	baseRefRepo := detector.NewRepoRef(worktreePath)

	// Step 3: Detect affected components
	slog.Info("Detecting affected components...")
	d, err := detector.NewDetector(headRef, baseRefRepo, *overlaysDir)
	if err != nil {
		fatal("initializing detector", "err", err)
	}
	affected, err := d.AffectedComponents(changedFiles)
	if err != nil {
		fatal("detecting affected components", "err", err)
	}

	// Count total jobs
	totalJobs := 0
	for _, paths := range affected {
		totalJobs += len(paths)
	}
	if totalJobs == 0 {
		fmt.Println("No affected components detected — nothing to diff.")
		return
	}
	slog.Info("Affected component paths detected", "count", totalJobs)

	// Step 4: Run render-diff engine
	engine := renderdiff.NewEngine(headRef, baseRefRepo, 0) // 0 = auto (NumCPU)

	switch *outputMode {
	case "local":
		runLocal(engine, affected, *color, *noColor, *openDiff, *outputDir)
	case "ci-summary":
		runCISummary(engine, affected)
	case "ci-comment":
		runCIComment(engine, affected, headSHA, baseSHA)
	case "ci-artifact-dir":
		if *outputDir == "" {
			fatal("--output-dir is required for ci-artifact-dir mode")
		}
		runCIArtifactDir(engine, affected, *outputDir)
	default:
		fatal("unknown output mode", "mode", *outputMode)
	}
}

// runLocal handles the default local output mode with progressive output.
func runLocal(engine *renderdiff.Engine, affected map[detector.Environment][]appset.ComponentPath, forceColor, forceNoColor, openDiff bool, outputDir string) {
	useColor := shouldUseColor(forceColor, forceNoColor)

	if outputDir != "" {
		// Write to directory mode
		result, err := engine.Run(affected)
		if err != nil {
			fatal("render-diff failed", "err", err)
		}
		if err := writeDiffFiles(result, outputDir); err != nil {
			fatal("writing diff files", "err", err)
		}
		printSummary(result)
		return
	}

	if openDiff {
		// Open in external diff tool
		result, err := engine.Run(affected)
		if err != nil {
			fatal("render-diff failed", "err", err)
		}
		if err := openInDiffTool(result); err != nil {
			fatal("opening diff tool", "err", err)
		}
		return
	}

	// Progressive output to stdout
	ch := make(chan renderdiff.ComponentDiff, 10)
	go func() {
		for cd := range ch {
			printComponentDiff(cd, useColor)
		}
	}()

	result, err := engine.RunProgressive(affected, ch)
	if err != nil {
		fatal("render-diff failed", "err", err)
	}
	printSummary(result)
}

// runCISummary generates markdown for GITHUB_STEP_SUMMARY.
func runCISummary(engine *renderdiff.Engine, affected map[detector.Environment][]appset.ComponentPath) {
	result, err := engine.Run(affected)
	if err != nil {
		fatal("render-diff failed", "err", err)
	}

	if len(result.Diffs) == 0 {
		fmt.Println("No render differences detected.")
		return
	}

	sortDiffs(result.Diffs)

	fmt.Println("# Kustomize Render Diff")
	fmt.Println()
	fmt.Printf("**%d components** with differences (+%d -%d lines)\n\n", len(result.Diffs), result.TotalAdded, result.TotalRemoved)

	const truncateThreshold = 50 * 1024 // 50KB
	for _, d := range result.Diffs {
		summary := fmt.Sprintf("%s (%s) — +%d -%d", d.Path, d.Env, d.Added, d.Removed)
		fmt.Printf("<details>\n<summary>%s</summary>\n\n", summary)
		if len(d.Diff) > truncateThreshold {
			fmt.Printf("```diff\n%s\n```\n\n", d.Diff[:truncateThreshold])
			fmt.Println("⚠️ Diff truncated. Download the full artifact for the complete diff.")
		} else {
			fmt.Printf("```diff\n%s\n```\n\n", d.Diff)
		}
		fmt.Println("</details>")
		fmt.Println()
	}
}

// runCIComment generates the PR comment markdown.
func runCIComment(engine *renderdiff.Engine, affected map[detector.Environment][]appset.ComponentPath, headSHA, baseSHA string) {
	result, err := engine.Run(affected)
	if err != nil {
		fatal("render-diff failed", "err", err)
	}

	// HTML comment marker for find-and-replace
	fmt.Println("<!-- render-diff-comment -->")
	fmt.Println("### Kustomize Render Diff")
	fmt.Println()
	fmt.Printf("Comparing `%s` → `%s`\n\n", baseSHA, headSHA)

	if len(result.Diffs) == 0 {
		fmt.Println("No render differences detected.")
		return
	}

	sortDiffs(result.Diffs)

	fmt.Println("| Component | Environment | Changes |")
	fmt.Println("|-----------|-------------|---------|")
	for _, d := range result.Diffs {
		fmt.Printf("| `%s` | %s | +%d -%d |\n", d.Path, d.Env, d.Added, d.Removed)
	}
	fmt.Println()
	fmt.Printf("**Total:** %d components, +%d -%d lines\n\n", len(result.Diffs), result.TotalAdded, result.TotalRemoved)
	fmt.Println("📋 Full diff available in the [workflow summary](../actions) and as a downloadable artifact.")
}

// runCIArtifactDir writes raw .diff files to a directory.
func runCIArtifactDir(engine *renderdiff.Engine, affected map[detector.Environment][]appset.ComponentPath, dir string) {
	result, err := engine.Run(affected)
	if err != nil {
		fatal("render-diff failed", "err", err)
	}
	if err := writeDiffFiles(result, dir); err != nil {
		fatal("writing artifact diff files", "err", err)
	}
	fmt.Printf("Wrote %d diff files to %s\n", len(result.Diffs), dir)
}

// writeDiffFiles writes per-component .diff files to a directory.
func writeDiffFiles(result *renderdiff.DiffResult, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	for _, d := range result.Diffs {
		name := diffFileName(d.Path, string(d.Env))
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(d.Diff), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
	}
	return nil
}

// diffFileName converts a component path and environment to a safe filename.
// e.g., "components/foo/staging" + "staging" → "components__foo__staging__staging.diff"
func diffFileName(componentPath, env string) string {
	safe := strings.ReplaceAll(componentPath, "/", "__")
	return fmt.Sprintf("%s__%s.diff", safe, env)
}

// printComponentDiff prints a single component's diff to stdout.
func printComponentDiff(cd renderdiff.ComponentDiff, useColor bool) {
	header := fmt.Sprintf("=== %s (%s) === +%d -%d", cd.Path, cd.Env, cd.Added, cd.Removed)
	if useColor {
		fmt.Printf("\033[1;36m%s\033[0m\n", header)
		colorDiff(cd.Diff)
	} else {
		fmt.Println(header)
		fmt.Print(cd.Diff)
	}
	fmt.Println()
}

// colorDiff prints a unified diff with ANSI colors.
func colorDiff(diff string) {
	for _, line := range strings.Split(diff, "\n") {
		if len(line) == 0 {
			fmt.Println()
			continue
		}
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			fmt.Printf("\033[1m%s\033[0m\n", line)
		case strings.HasPrefix(line, "@@"):
			fmt.Printf("\033[36m%s\033[0m\n", line)
		case line[0] == '+':
			fmt.Printf("\033[32m%s\033[0m\n", line)
		case line[0] == '-':
			fmt.Printf("\033[31m%s\033[0m\n", line)
		default:
			fmt.Println(line)
		}
	}
}

// printSummary prints aggregate statistics.
func printSummary(result *renderdiff.DiffResult) {
	if len(result.Diffs) == 0 {
		fmt.Println("\nNo render differences detected.")
		return
	}

	fmt.Println("\n--- Summary ---")
	sortDiffs(result.Diffs)
	for _, d := range result.Diffs {
		fmt.Printf("  %s (%s): +%d -%d\n", d.Path, d.Env, d.Added, d.Removed)
	}
	fmt.Printf("\nTotal: %d components, +%d -%d lines\n", len(result.Diffs), result.TotalAdded, result.TotalRemoved)
}

// shouldUseColor determines whether to use ANSI colors.
func shouldUseColor(forceColor, forceNoColor bool) bool {
	if forceNoColor {
		return false
	}
	if forceColor {
		return true
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// openInDiffTool opens diffs in the user's preferred diff tool.
func openInDiffTool(result *renderdiff.DiffResult) error {
	if len(result.Diffs) == 0 {
		fmt.Println("No render differences to display.")
		return nil
	}

	for _, d := range result.Diffs {
		baseFile, err := os.CreateTemp("", "render-diff-base-*.yaml")
		if err != nil {
			return err
		}
		defer func() { _ = os.Remove(baseFile.Name()) }()

		headFile, err := os.CreateTemp("", "render-diff-head-*.yaml")
		if err != nil {
			return err
		}
		defer func() { _ = os.Remove(headFile.Name()) }()

		if _, err := baseFile.Write(d.BaseYAML); err != nil {
			return err
		}
		_ = baseFile.Close()

		if _, err := headFile.Write(d.HeadYAML); err != nil {
			return err
		}
		_ = headFile.Close()

		toolName := os.Getenv("DIFFTOOL")
		var cmd *exec.Cmd
		if toolName != "" {
			cmd = exec.Command(toolName, baseFile.Name(), headFile.Name())
		} else {
			cmd = exec.Command("git", "difftool", "--no-index", baseFile.Name(), headFile.Name())
		}
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		fmt.Printf("Opening diff for %s (%s)...\n", d.Path, d.Env)
		if err := cmd.Run(); err != nil {
			// git difftool returns non-zero if files differ, which is expected
			slog.Debug("diff tool exited", "err", err)
		}
	}
	return nil
}

// sortDiffs sorts diffs by environment then path for consistent output.
func sortDiffs(diffs []renderdiff.ComponentDiff) {
	sort.Slice(diffs, func(i, j int) bool {
		if diffs[i].Env != diffs[j].Env {
			return diffs[i].Env < diffs[j].Env
		}
		return diffs[i].Path < diffs[j].Path
	})
}

// fatal logs an error and exits.
func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

// setupLogging configures slog with charmbracelet/log handlers.
func setupLogging(logFile string) (func(), error) {
	stdoutHandler := charmlog.NewWithOptions(os.Stderr, charmlog.Options{
		Level: charmlog.InfoLevel,
	})

	if logFile == "" {
		slog.SetDefault(slog.New(stdoutHandler))
		return nil, nil
	}

	f, err := os.Create(logFile)
	if err != nil {
		return nil, fmt.Errorf("opening log file %s: %w", logFile, err)
	}

	fileHandler := charmlog.NewWithOptions(f, charmlog.Options{
		Level:           charmlog.DebugLevel,
		ReportTimestamp: true,
	})

	multi := &multiHandler{handlers: []slog.Handler{stdoutHandler, fileHandler}}
	slog.SetDefault(slog.New(multi))

	return func() { _ = f.Close() }, nil
}

// multiHandler fans out log records to multiple handlers.
type multiHandler struct {
	handlers []slog.Handler
}

func (m *multiHandler) Enabled(_ context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(context.Background(), level) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: handlers}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		handlers[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: handlers}
}
