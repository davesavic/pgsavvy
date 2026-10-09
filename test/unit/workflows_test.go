package unit

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflow struct {
	Name        string            `yaml:"name"`
	On          map[string]any    `yaml:"on"`
	Permissions map[string]string `yaml:"permissions"`
	Concurrency struct {
		Group            string `yaml:"group"`
		CancelInProgress bool   `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
	Jobs map[string]struct {
		TimeoutMinutes int               `yaml:"timeout-minutes"`
		Permissions    map[string]string `yaml:"permissions"`
		Steps          []struct {
			Name           string `yaml:"name"`
			TimeoutMinutes int    `yaml:"timeout-minutes"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func readWorkflow(t *testing.T, name string) workflow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name+".yml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc workflow
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Jobs) == 0 {
		t.Fatal("workflow has no jobs")
	}
	return doc
}

func TestCITokenPermissions(t *testing.T) {
	ci := readWorkflow(t, "ci")
	want := map[string]string{"contents": "read"}
	if !reflect.DeepEqual(ci.Permissions, want) {
		t.Errorf("CI permissions=%v, want %v", ci.Permissions, want)
	}
	for name, job := range ci.Jobs {
		if job.Permissions != nil && !reflect.DeepEqual(job.Permissions, want) {
			t.Errorf("CI job %s overrides read-only permissions: %v", name, job.Permissions)
		}
	}
	release := readWorkflow(t, "release")
	if got := release.Jobs["release"].Permissions; !reflect.DeepEqual(got, map[string]string{"contents": "write"}) {
		t.Errorf("release publishing permissions changed: %v", got)
	}
}

func TestWorkflowTimeouts(t *testing.T) {
	for _, tc := range []struct {
		file string
		jobs map[string]int
	}{
		{"ci", map[string]int{"build": 20, "integration": 20}},
		{"release", map[string]int{"release": 45}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			doc := readWorkflow(t, tc.file)
			if len(doc.Jobs) != len(tc.jobs) {
				t.Errorf("jobs=%d, want %d", len(doc.Jobs), len(tc.jobs))
			}
			for name, want := range tc.jobs {
				if got := doc.Jobs[name].TimeoutMinutes; got != want {
					t.Errorf("job %s timeout=%d, want %d minutes", name, got, want)
				}
			}
			for name, job := range doc.Jobs {
				if job.TimeoutMinutes <= 0 || job.TimeoutMinutes > 60 {
					t.Errorf("job %s has unbounded timeout=%d", name, job.TimeoutMinutes)
				}
			}
		})
	}
	ci := readWorkflow(t, "ci")
	found := false
	for _, step := range ci.Jobs["integration"].Steps {
		if step.Name == "Wait for postgres" {
			found = true
			if step.TimeoutMinutes != 2 {
				t.Errorf("PostgreSQL readiness timeout=%d, want 2 minutes", step.TimeoutMinutes)
			}
		}
	}
	if !found {
		t.Fatal("PostgreSQL readiness check missing")
	}
}

func TestCIConcurrency(t *testing.T) {
	ci := readWorkflow(t, "ci")
	const expression = "${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}"
	if ci.Concurrency.Group != expression || !ci.Concurrency.CancelInProgress {
		t.Fatalf("CI concurrency=%+v, want PR/ref group and cancellation", ci.Concurrency)
	}
	for _, event := range []string{"push", "pull_request"} {
		if _, ok := ci.On[event]; !ok {
			t.Errorf("CI trigger %q missing", event)
		}
	}
	for _, tc := range []struct {
		name     string
		workflow string
		pr       int
		ref      string
		want     string
	}{
		{"push", ci.Name, 0, "refs/heads/master", "ci-refs/heads/master"},
		{"new push same ref", ci.Name, 0, "refs/heads/master", "ci-refs/heads/master"},
		{"different ref", ci.Name, 0, "refs/heads/next", "ci-refs/heads/next"},
		{"pull request", ci.Name, 7, "refs/pull/7/merge", "ci-7"},
		{"updated pull request", ci.Name, 7, "refs/pull/7/merge", "ci-7"},
		{"fork PR same head branch", ci.Name, 8, "refs/pull/8/merge", "ci-8"},
		{"numeric branch", ci.Name, 0, "refs/heads/7", "ci-refs/heads/7"},
		{"different workflow", "other", 7, "refs/pull/7/merge", "other-7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Model only the two expressions locked above, not a general runner.
			identity := tc.ref
			if tc.pr != 0 {
				identity = strconv.Itoa(tc.pr)
			}
			group := strings.NewReplacer(
				"${{ github.workflow }}", tc.workflow,
				"${{ github.event.pull_request.number || github.ref }}", identity,
			).Replace(ci.Concurrency.Group)
			if group != tc.want {
				t.Errorf("group=%q, want %q", group, tc.want)
			}
		})
	}
	release := readWorkflow(t, "release")
	if release.Concurrency.Group != "" || release.Concurrency.CancelInProgress {
		t.Errorf("release cancellation must not change: %+v", release.Concurrency)
	}
	if got := release.On["push"]; !reflect.DeepEqual(got, map[string]any{"tags": []any{"v*"}}) {
		t.Errorf("release trigger changed: %v", got)
	}
}
