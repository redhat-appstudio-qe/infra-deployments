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

	// Parse and validate output modes (comma-separated).
	modes := parseOutputModes(*outputMode)
	if len(modes) == 0 {
		fmt.Fprintf(os.Stderr, "invalid --output-mode %q: must be one or more of local, ci-summary, ci-comment, ci-artifact-dir (comma-separated)\n", *outputMode)
		os.Exit(1)
	}

	switch *color {
	case "auto", "always", "never":
		// valid
	default:
		fmt.Fprintf(os.Stderr, "invalid --color %q: must be one of auto, always, never\n", *color)
		os.Exit(1)
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

	// Step 4: Run render-diff engine (once for all output modes).
	engine := renderdiff.NewEngine(headRef, baseRefRepo, totalJobs)

	// For local mode (single mode only), use progressive output.
	if len(modes) == 1 && modes[0] == OutputModeLocal {
		runLocal(ctx, engine, affected, *color, *openDiff, *outputDir)
		return
	}

	// For CI modes (possibly multiple), build once and share the result.
	result, err := engine.Run(ctx, affected)
	if err != nil {
		logging.Fatal("render-diff failed", "err", err)
	}

	var hadError bool
	for _, m := range modes {
		if err := runOutputMode(ctx, m, result, *color, *openDiff, *outputDir, headSHA, baseSHA); err != nil {
			slog.Error("output mode failed, continuing with remaining modes", "mode", m, "err", err)
			hadError = true
		}
	}
	if hadError {
		os.Exit(1)
	}
}

// runOutputMode executes a single output mode against a pre-computed result.
// Returns an error instead of calling Fatal, so the caller can continue with
// remaining modes.
func runOutputMode(ctx context.Context, mode OutputMode, result *renderdiff.DiffResult, colorMode string, openDiff bool, outputDir, headSHA, baseSHA string) error {
	switch mode {
	case OutputModeLocal:
		useColor := shouldUseColor(colorMode)
		if outputDir != "" {
			if err := writeDiffFiles(result, outputDir); err != nil {
				return fmt.Errorf("writing diff files: %w", err)
			}
		}
		if openDiff {
			if err := openInDiffTool(result); err != nil {
				return fmt.Errorf("opening diff tool: %w", err)
			}
			return nil
		}
		for _, cd := range result.Diffs {
			printComponentDiff(cd, useColor)
		}
		printSummary(result)
	case OutputModeCISummary:
		if err := writeCISummary(result); err != nil {
			return err
		}
	case OutputModeCIComment:
		if err := postCIComment(ctx, result, headSHA, baseSHA); err != nil {
			return err
		}
	case OutputModeCIArtifact:
		if outputDir == "" {
			return fmt.Errorf("--output-dir is required for ci-artifact-dir mode")
		}
		if err := writeDiffFiles(result, outputDir); err != nil {
			return fmt.Errorf("writing artifact diff files: %w", err)
		}
		fmt.Printf("Wrote %d diff files to %s\n", len(result.Diffs), outputDir)
	}
	return nil
}

// runLocal handles the default local output mode with progressive output.
func runLocal(ctx context.Context, engine *renderdiff.Engine, affected map[detector.Environment][]appset.ComponentPath, colorMode string, openDiff bool, outputDir string) {
	useColor := shouldUseColor(colorMode)

	if outputDir != "" {
		// Write to directory mode
		result, err := engine.Run(ctx, affected)
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
		result, err := engine.Run(ctx, affected)
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

	result, err := engine.RunProgressive(ctx, affected, ch)
	if err != nil {
		logging.Fatal("render-diff failed", "err", err)
	}
	printSummary(result)
}

// writeCISummary generates markdown for $GITHUB_STEP_SUMMARY.
// When the GITHUB_STEP_SUMMARY environment variable is set, output is written
// directly to that file so it doesn't mix with other modes' stdout output.
// Falls back to stdout when the variable is unset.
func writeCISummary(result *renderdiff.DiffResult) error {
	w := os.Stdout
	if summaryPath := os.Getenv("GITHUB_STEP_SUMMARY"); summaryPath != "" {
		f, err := os.OpenFile(summaryPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("opening $GITHUB_STEP_SUMMARY: %w", err)
		}
		defer f.Close()
		w = f
	}

	if len(result.Diffs) == 0 {
		fmt.Fprintln(w, "No render differences detected.")
		return nil
	}

	sortDiffs(result.Diffs)

	fmt.Fprintln(w, "# Kustomize Render Diff")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "**%d components** with differences (+%d -%d lines)\n\n", len(result.Diffs), result.TotalAdded, result.TotalRemoved)

	const truncateThreshold = 50 * 1024 // 50KB
	for _, d := range result.Diffs {
		if d.Error != "" {
			summary := fmt.Sprintf("%s (%s) — build error", d.Path, d.Env)
			fmt.Fprintf(w, "<details>\n<summary>%s</summary>\n\n", summary)
			fmt.Fprintf(w, "```\n%s\n```\n\n", d.Error)
			fmt.Fprintln(w, "</details>")
			fmt.Fprintln(w)
			continue
		}
		summary := fmt.Sprintf("%s (%s) — +%d -%d", d.Path, d.Env, d.Added, d.Removed)
		fmt.Fprintf(w, "<details>\n<summary>%s</summary>\n\n", summary)
		if len(d.Diff) > truncateThreshold {
			fmt.Fprintf(w, "```diff\n%s\n```\n\n", d.Diff[:truncateThreshold])
			fmt.Fprintln(w, "⚠️ Diff truncated. Download the full artifact for the complete diff.")
		} else {
			fmt.Fprintf(w, "```diff\n%s\n```\n\n", d.Diff)
		}
		fmt.Fprintln(w, "</details>")
		fmt.Fprintln(w)
	}
	return nil
}

// postCIComment generates the PR comment markdown and posts it to GitHub.
// CI-specific configuration is read from environment variables:
//   - GITHUB_TOKEN: API token for authentication
//   - GITHUB_REPOSITORY: repository in "owner/repo" format
//   - PR_NUMBER: pull request number to comment on
//
// If any of these are missing, the comment body is printed to stdout instead.
func postCIComment(ctx context.Context, result *renderdiff.DiffResult, headSHA, baseSHA string) error {
	body := buildCommentBody(result, headSHA, baseSHA)

	token := os.Getenv("GITHUB_TOKEN")
	repo := os.Getenv("GITHUB_REPOSITORY")
	prStr := os.Getenv("PR_NUMBER")

	if token == "" || repo == "" || prStr == "" {
		// Missing CI env vars — print to stdout as fallback.
		fmt.Print(body)
		return nil
	}

	prNumber := 0
	if _, err := fmt.Sscanf(prStr, "%d", &prNumber); err != nil || prNumber == 0 {
		return fmt.Errorf("invalid PR_NUMBER %q", prStr)
	}

	client, err := ghclient.NewCommentClient(token, repo)
	if err != nil {
		return fmt.Errorf("creating GitHub client: %w", err)
	}
	if err := client.UpsertComment(ctx, prNumber, body); err != nil {
		return fmt.Errorf("posting PR comment: %w", err)
	}
	slog.Info("PR comment posted", "pr", prNumber)
	return nil
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

// parseOutputModes splits a comma-separated output-mode string, validates each
// value, and returns the deduplicated list. Returns nil if any value is invalid.
func parseOutputModes(raw string) []OutputMode {
	seen := make(map[OutputMode]bool)
	var modes []OutputMode
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		m := OutputMode(s)
		switch m {
		case OutputModeLocal, OutputModeCISummary, OutputModeCIComment, OutputModeCIArtifact:
			if !seen[m] {
				seen[m] = true
				modes = append(modes, m)
			}
		default:
			return nil
		}
	}
	return modes
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
