// Package renderdiff computes unified diffs of kustomize-rendered YAML for
// affected component paths across environments.
package renderdiff

import (
	"strings"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/redhat-appstudio/infra-deployments/infra-tools/internal/appset"
	"github.com/redhat-appstudio/infra-deployments/infra-tools/internal/detector"
)

// ComponentDiff holds the diff result for a single (component, environment) pair.
type ComponentDiff struct {
	// Path is the component path relative to the repo root.
	Path string
	// ClusterDir is non-empty when the component targets a specific cluster.
	ClusterDir string
	// Env is the environment this component belongs to.
	Env detector.Environment
	// BaseYAML is the rendered YAML from the base ref (nil for new components).
	BaseYAML []byte
	// HeadYAML is the rendered YAML from HEAD (nil for removed components).
	HeadYAML []byte
	// Diff is the unified diff text between base and head.
	Diff string
	// Added is the number of lines added.
	Added int
	// Removed is the number of lines removed.
	Removed int
}

// computeDiff populates the Diff, Added, and Removed fields from BaseYAML and HeadYAML.
func (cd *ComponentDiff) computeDiff() error {
	baseStr := string(cd.BaseYAML)
	headStr := string(cd.HeadYAML)

	if baseStr == headStr {
		return nil
	}

	ud := difflib.UnifiedDiff{
		A:        difflib.SplitLines(baseStr),
		B:        difflib.SplitLines(headStr),
		FromFile: cd.Path + " (base)",
		ToFile:   cd.Path + " (head)",
		Context:  3,
	}

	text, err := difflib.GetUnifiedDiffString(ud)
	if err != nil {
		return err
	}

	cd.Diff = text
	cd.Added, cd.Removed = countStats(text)
	return nil
}

// HasDiff returns true if this component has a non-empty diff.
func (cd *ComponentDiff) HasDiff() bool {
	return cd.Diff != ""
}

// FromComponentPath creates a ComponentDiff from an appset.ComponentPath and environment.
func FromComponentPath(cp appset.ComponentPath, env detector.Environment) *ComponentDiff {
	return &ComponentDiff{
		Path:       cp.Path,
		ClusterDir: cp.ClusterDir,
		Env:        env,
	}
}

// countStats counts the number of added and removed lines in a unified diff.
func countStats(diff string) (added, removed int) {
	for _, line := range strings.Split(diff, "\n") {
		if len(line) == 0 {
			continue
		}
		switch line[0] {
		case '+':
			if !strings.HasPrefix(line, "+++") {
				added++
			}
		case '-':
			if !strings.HasPrefix(line, "---") {
				removed++
			}
		}
	}
	return
}
