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

	"golang.org/x/term"

	"github.com/redhat-appstudio/infra-deployments/infra-tools/internal/appset"
	"github.com/redhat-appstudio/infra-deployments/infra-tools/internal/detector"
	"github.com/redhat-appstudio/infra-deployments/infra-tools/internal/git"
	ghclient "github.com/redhat-appstudio/infra-deployments/infra-tools/internal/github"
	"github.com/redhat-appstudio/infra-deployments/infra-tools/internal/logging"
	"github.com/redhat-appstudio/infra-deployments/infra-tools/internal/renderdiff"
)

// version is set via -ldflags at build time.
var version = "dev"

// OutputMode controls how render-diff formats and delivers its output.
type OutputMode string

const (
	OutputModeLocal      OutputMode = "local"
	OutputModeCISummary  OutputMode = "ci-summary"
	OutputModeCIComment  OutputMode = "ci-comment"
	OutputModeCIArtifact OutputMode = "ci-artifact-dir"
)

func main() {
	var (
		repoRoot    = flag.String("repo-root", "", "Path to the repository root (default: auto-detect via git)")
		baseRef     = flag.String("base-ref", "", "Base git ref to compare against (default: merge-base with main)")
		overlaysDir = flag.String("overlays-dir", "argo-cd-apps/overlays", "Path to overlays directory relative to repo root")
		color       = flag.String("color", "auto", "Color output: auto, always, never")
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
	logCleanup, err := logging.Setup(*logFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to set up logging: %v\n", err)
		os.Exit(1)
	}
	if logCleanup != nil {
		defer logCleanup()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Auto-detect repo root via git if not specified.
	if *repoRoot == "" {
		detected, err := git.TopLevel(ctx)
		if err != nil {
			logging.Fatal("auto-detecting repo root; use --repo-root to specify explicitly", "err", err)
		}
		repoRoot = &detected
	}

	absRepoRoot, err := filepath.Abs(*repoRoot)
	if err != nil {
		logging.Fatal("resolving repo root", "err", err)
	}

	// Resolve base ref: default to merge-base with main
	effectiveBaseRef := *baseRef
	if effectiveBaseRef == "" {
		effectiveBaseRef, err = git.MergeBase(ctx, absRepoRoot, "main")
		if err != nil {
			logging.Fatal("could not compute merge-base with main; use --base-ref to specify explicitly", "err", err)
		}
	}

	baseSHA, err := git.ResolveRef(ctx, absRepoRoot, effectiveBaseRef)
	if err != nil {
		logging.Fatal("resolving base ref", "err", err)
	}

	headSHA, err := git.ResolveRef(ctx, absRepoRoot, "HEAD")
	if err != nil {
		logging.Fatal("resolving HEAD", "err", err)
	}
	slog.Info("Comparing refs", "head", headSHA, "base", baseSHA)

	// Step 1: Get changed files
	changedFiles, err := git.ChangedFiles(ctx, absRepoRoot, effectiveBaseRef)
	if err != nil {
		logging.Fatal("getting changed files", "err", err)
	}
	if len(changedFiles) == 0 {
		fmt.Println("No changed files detected — nothing to diff.")
		return
	}
	slog.Info("Changed files detected", "count", len(changedFiles))

	// Step 2: Create worktree at base ref
	worktreePath, cleanup, err := git.CreateWorktree(ctx, absRepoRoot, effectiveBaseRef)
	if err != nil {
		logging.Fatal("creating worktree", "err", err)
	}
	defer cleanup()

	headRef := detector.NewRepoRef(absRepoRoot)
	baseRefRepo := detector.NewRepoRef(worktreePath)

	// Step 3: Detect affected components
	slog.Info("Detecting affected components...")
	d, err := detector.NewDetector(headRef, baseRefRepo, *overlaysDir)
	if err != nil {
		logging.Fatal("initializing detector", "err", err)
	}
	affected, err := d.AffectedComponents(changedFiles)
	if err != nil {
		logging.Fatal("detecting affected components", "err", err)
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
	engine := renderdiff.NewEngine(headRef, baseRefRepo, totalJobs)

	switch OutputMode(*outputMode) {
	case OutputModeLocal:
		runLocal(engine, affected, *color, *openDiff, *outputDir)
	case OutputModeCISummary:
		runCISummary(engine, affected)
	case OutputModeCIComment:
		runCIComment(ctx, engine, affected, headSHA, baseSHA)
	case OutputModeCIArtifact:
		if *outputDir == "" {
			logging.Fatal("--output-dir is required for ci-artifact-dir mode")
		}
		runCIArtifactDir(engine, affected, *outputDir)
	default:
		logging.Fatal("unknown output mode", "mode", *outputMode)
	}
}

// runLocal handles the default local output mode with progressive output.
func runLocal(engine *renderdiff.Engine, affected map[detector.Environment][]appset.ComponentPath, colorMode string, openDiff bool, outputDir string) {
	useColor := shouldUseColor(colorMode)

	if outputDir != "" {
		// Write to directory mode
		result, err := engine.Run(affected)
		if err != nil {
			logging.Fatal("render-diff failed", "err", err)
		}
		if err := writeDiffFiles(result, outputDir); err != nil {
			logging.Fatal("writing diff files", "err", err)
		}
		printSummary(result)
		return
	}

	if openDiff {
		// Open in external diff tool
		result, err := engine.Run(affected)
		if err != nil {
			logging.Fatal("render-diff failed", "err", err)
		}
		if err := openInDiffTool(result); err != nil {
			logging.Fatal("opening diff tool", "err", err)
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
		logging.Fatal("render-diff failed", "err", err)
	}
	printSummary(result)
}

// runCISummary generates markdown for GITHUB_STEP_SUMMARY.
func runCISummary(engine *renderdiff.Engine, affected map[detector.Environment][]appset.ComponentPath) {
	result, err := engine.Run(affected)
	if err != nil {
		logging.Fatal("render-diff failed", "err", err)
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
		if d.Error != "" {
			summary := fmt.Sprintf("%s (%s) — build error", d.Path, d.Env)
			fmt.Printf("<details>\n<summary>%s</summary>\n\n", summary)
			fmt.Printf("```\n%s\n```\n\n", d.Error)
			fmt.Println("</details>")
			fmt.Println()
			continue
		}
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

// runCIComment generates the PR comment markdown and posts it to GitHub.
// CI-specific configuration is read from environment variables:
//   - GITHUB_TOKEN: API token for authentication
//   - GITHUB_REPOSITORY: repository in "owner/repo" format
//   - PR_NUMBER: pull request number to comment on
//
// If any of these are missing, the comment body is printed to stdout instead.
func runCIComment(ctx context.Context, engine *renderdiff.Engine, affected map[detector.Environment][]appset.ComponentPath, headSHA, baseSHA string) {
	result, err := engine.Run(affected)
	if err != nil {
		logging.Fatal("render-diff failed", "err", err)
	}

	body := buildCommentBody(result, headSHA, baseSHA)

	token := os.Getenv("GITHUB_TOKEN")
	repo := os.Getenv("GITHUB_REPOSITORY")
	prStr := os.Getenv("PR_NUMBER")

	if token == "" || repo == "" || prStr == "" {
		// Missing CI env vars — print to stdout as fallback.
		fmt.Print(body)
		return
	}

	prNumber := 0
	if _, err := fmt.Sscanf(prStr, "%d", &prNumber); err != nil || prNumber == 0 {
		logging.Fatal("invalid PR_NUMBER", "value", prStr)
	}

	client, err := ghclient.NewCommentClient(token, repo)
	if err != nil {
		logging.Fatal("creating GitHub client", "err", err)
	}
	if err := client.UpsertComment(ctx, prNumber, body); err != nil {
		logging.Fatal("posting PR comment", "err", err)
	}
	slog.Info("PR comment posted", "pr", prNumber)
}

// buildCommentBody generates the markdown for a PR comment.
func buildCommentBody(result *renderdiff.DiffResult, headSHA, baseSHA string) string {
	var b strings.Builder

	fmt.Fprintln(&b, "<!-- render-diff-comment -->")
	fmt.Fprintln(&b, "### Kustomize Render Diff")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Comparing `%s` → `%s`\n\n", baseSHA, headSHA)

	if len(result.Diffs) == 0 {
		fmt.Fprintln(&b, "No render differences detected.")
		return b.String()
	}

	sortDiffs(result.Diffs)

	fmt.Fprintln(&b, "| Component | Environment | Changes |")
	fmt.Fprintln(&b, "|-----------|-------------|---------|")
	for _, d := range result.Diffs {
		if d.Error != "" {
			fmt.Fprintf(&b, "| `%s` | %s | build error |\n", d.Path, d.Env)
		} else {
			fmt.Fprintf(&b, "| `%s` | %s | +%d -%d |\n", d.Path, d.Env, d.Added, d.Removed)
		}
	}
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "**Total:** %d components, +%d -%d lines\n\n", len(result.Diffs), result.TotalAdded, result.TotalRemoved)
	fmt.Fprintln(&b, "📋 Full diff available in the [workflow summary](../actions) and as a downloadable artifact.")
	return b.String()
}

// runCIArtifactDir writes raw .diff files to a directory.
func runCIArtifactDir(engine *renderdiff.Engine, affected map[detector.Environment][]appset.ComponentPath, dir string) {
	result, err := engine.Run(affected)
	if err != nil {
		logging.Fatal("render-diff failed", "err", err)
	}
	if err := writeDiffFiles(result, dir); err != nil {
		logging.Fatal("writing artifact diff files", "err", err)
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
	if cd.Error != "" {
		header := fmt.Sprintf("=== %s (%s) === BUILD ERROR", cd.Path, cd.Env)
		if useColor {
			fmt.Printf("\033[1;31m%s\033[0m\n", header)
			fmt.Printf("\033[31m%s\033[0m\n", cd.Error)
		} else {
			fmt.Println(header)
			fmt.Println(cd.Error)
		}
		fmt.Println()
		return
	}
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
		if d.Error != "" {
			fmt.Printf("  %s (%s): BUILD ERROR\n", d.Path, d.Env)
		} else {
			fmt.Printf("  %s (%s): +%d -%d\n", d.Path, d.Env, d.Added, d.Removed)
		}
	}
	fmt.Printf("\nTotal: %d components, +%d -%d lines\n", len(result.Diffs), result.TotalAdded, result.TotalRemoved)
}

// shouldUseColor determines whether to use ANSI colors based on the --color flag.
func shouldUseColor(mode string) bool {
	switch mode {
	case "always":
		return true
	case "never":
		return false
	default: // "auto"
		return term.IsTerminal(int(os.Stdout.Fd()))
	}
}

// openInDiffTool writes all base and head YAML files into two temporary
// directories and opens them in the user's preferred diff tool for a
// side-by-side folder comparison. Files are named after their component
// and environment so they are easy to identify.
func openInDiffTool(result *renderdiff.DiffResult) error {
	if len(result.Diffs) == 0 {
		fmt.Println("No render differences to display.")
		return nil
	}

	// Create temp directories for the base and head YAML files.
	// These are intentionally not cleaned up: GUI diff tools like meld may
	// return immediately while still reading the files, and keeping them
	// lets the user re-inspect after the tool closes. The OS cleans /tmp.
	baseDir, err := os.MkdirTemp("", "render-diff-base-*")
	if err != nil {
		return fmt.Errorf("creating base temp dir: %w", err)
	}

	headDir, err := os.MkdirTemp("", "render-diff-head-*")
	if err != nil {
		return fmt.Errorf("creating head temp dir: %w", err)
	}

	for _, d := range result.Diffs {
		name := diffFileName(d.Path, string(d.Env))
		// Replace .diff extension with .yaml for clarity in the diff tool.
		name = strings.TrimSuffix(name, ".diff") + ".yaml"

		if err := os.WriteFile(filepath.Join(baseDir, name), d.BaseYAML, 0o644); err != nil {
			return fmt.Errorf("writing base file for %s: %w", d.Path, err)
		}
		if err := os.WriteFile(filepath.Join(headDir, name), d.HeadYAML, 0o644); err != nil {
			return fmt.Errorf("writing head file for %s: %w", d.Path, err)
		}
	}

	toolName := os.Getenv("DIFFTOOL")
	var cmd *exec.Cmd
	if toolName != "" {
		cmd = exec.Command(toolName, baseDir, headDir)
	} else {
		cmd = exec.Command("git", "difftool", "--no-index", "--dir-diff", baseDir, headDir)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Printf("Opening folder diff: %s vs %s\n", baseDir, headDir)
	if err := cmd.Run(); err != nil {
		// diff tools return non-zero when files differ, which is expected
		slog.Debug("diff tool exited", "err", err)
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
