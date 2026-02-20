package renderdiff

import (
	"fmt"
	"log/slog"
	"runtime"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/redhat-appstudio/infra-deployments/infra-tools/internal/appset"
	"github.com/redhat-appstudio/infra-deployments/infra-tools/internal/detector"
)

// RepoBuilder abstracts the ability to check directory existence and build
// kustomizations on a specific git ref.
type RepoBuilder interface {
	DirExists(rel string) bool
	BuildKustomization(rel string) ([]byte, error)
}

// Engine computes kustomize render diffs for affected component paths.
type Engine struct {
	head        RepoBuilder
	base        RepoBuilder
	concurrency int
}

// NewEngine creates an Engine with the given head and base repo references.
// Concurrency defaults to runtime.NumCPU() if zero.
func NewEngine(head, base RepoBuilder, concurrency int) *Engine {
	if concurrency <= 0 {
		concurrency = runtime.NumCPU()
	}
	return &Engine{head: head, base: base, concurrency: concurrency}
}

// DiffResult holds the complete output of a render-diff run.
type DiffResult struct {
	// Diffs contains only components with actual differences.
	Diffs []ComponentDiff
	// TotalAdded is the aggregate lines added across all diffs.
	TotalAdded int
	// TotalRemoved is the aggregate lines removed across all diffs.
	TotalRemoved int
}

// Run builds each affected component path on both refs in parallel, computes
// unified diffs, and returns only those with actual differences.
func (e *Engine) Run(affected map[detector.Environment][]appset.ComponentPath) (*DiffResult, error) {
	// Collect all (component, env) pairs to process.
	type job struct {
		cp  appset.ComponentPath
		env detector.Environment
	}
	var jobs []job
	for env, paths := range affected {
		for _, cp := range paths {
			jobs = append(jobs, job{cp: cp, env: env})
		}
	}

	if len(jobs) == 0 {
		return &DiffResult{}, nil
	}

	var (
		mu      sync.Mutex
		results []ComponentDiff
		g       errgroup.Group
	)
	g.SetLimit(e.concurrency)

	for _, j := range jobs {
		g.Go(func() error {
			cd := FromComponentPath(j.cp, j.env)

			if err := e.buildPair(cd); err != nil {
				slog.Warn("skipping component due to build error",
					"path", j.cp.Path, "env", j.env, "err", err)
				return nil // non-fatal: skip and continue
			}

			if err := cd.computeDiff(); err != nil {
				return fmt.Errorf("computing diff for %s (%s): %w", j.cp.Path, j.env, err)
			}

			if cd.HasDiff() {
				mu.Lock()
				results = append(results, *cd)
				mu.Unlock()
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	dr := &DiffResult{Diffs: results}
	for _, d := range results {
		dr.TotalAdded += d.Added
		dr.TotalRemoved += d.Removed
	}
	return dr, nil
}

// RunProgressive is like Run but sends each completed diff to the provided
// channel as it finishes, enabling progressive output. The channel is closed
// when all jobs complete. Returns aggregate stats and any error.
func (e *Engine) RunProgressive(affected map[detector.Environment][]appset.ComponentPath, out chan<- ComponentDiff) (*DiffResult, error) {
	defer close(out)

	type job struct {
		cp  appset.ComponentPath
		env detector.Environment
	}
	var jobs []job
	for env, paths := range affected {
		for _, cp := range paths {
			jobs = append(jobs, job{cp: cp, env: env})
		}
	}

	if len(jobs) == 0 {
		return &DiffResult{}, nil
	}

	var (
		mu      sync.Mutex
		result  DiffResult
		g       errgroup.Group
	)
	g.SetLimit(e.concurrency)

	for _, j := range jobs {
		g.Go(func() error {
			cd := FromComponentPath(j.cp, j.env)

			if err := e.buildPair(cd); err != nil {
				slog.Warn("skipping component due to build error",
					"path", j.cp.Path, "env", j.env, "err", err)
				return nil
			}

			if err := cd.computeDiff(); err != nil {
				return fmt.Errorf("computing diff for %s (%s): %w", j.cp.Path, j.env, err)
			}

			if cd.HasDiff() {
				out <- *cd
				mu.Lock()
				result.Diffs = append(result.Diffs, *cd)
				result.TotalAdded += cd.Added
				result.TotalRemoved += cd.Removed
				mu.Unlock()
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}
	return &result, nil
}

// buildPair builds the kustomization on both refs, populating BaseYAML and HeadYAML.
// Handles new components (no base), removed components (no head), and build errors.
func (e *Engine) buildPair(cd *ComponentDiff) error {
	var headErr, baseErr error

	// Build HEAD
	if e.head.DirExists(cd.Path) {
		cd.HeadYAML, headErr = e.head.BuildKustomization(cd.Path)
		if headErr != nil {
			return fmt.Errorf("building %s on HEAD: %w", cd.Path, headErr)
		}
	}

	// Build base
	if e.base.DirExists(cd.Path) {
		cd.BaseYAML, baseErr = e.base.BuildKustomization(cd.Path)
		if baseErr != nil {
			return fmt.Errorf("building %s on base: %w", cd.Path, baseErr)
		}
	}

	// If neither side has the directory, nothing to diff.
	if cd.HeadYAML == nil && cd.BaseYAML == nil {
		return fmt.Errorf("component %s does not exist on either ref", cd.Path)
	}

	return nil
}
