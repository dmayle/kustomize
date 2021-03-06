// Copyright 2019 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package runfn

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/kustomize/kyaml/copyutil"
	"sigs.k8s.io/kustomize/kyaml/kio"
	"sigs.k8s.io/kustomize/kyaml/kio/filters"
	"sigs.k8s.io/kustomize/kyaml/yaml"
)

const (
	ValueReplacerYAMLData = `apiVersion: v1
kind: ValueReplacer
metadata:
  annotations:
    config.kubernetes.io/function: |
      container:
        image: gcr.io/example.com/image:version
    config.kubernetes.io/local-config: "true"
stringMatch: Deployment
replace: StatefulSet
`
)

func TestRunFns_Execute(t *testing.T) {
	instance := RunFns{}
	instance.init()
	api, err := yaml.Parse(`apiVersion: apps/v1
kind: 
`)
	if !assert.NoError(t, err) {
		return
	}
	filter := instance.containerFilterProvider("example.com:version", "", api)
	assert.Equal(t, &filters.ContainerFilter{Image: "example.com:version", Config: api}, filter)
}

func TestRunFns_Execute_globalScope(t *testing.T) {
	instance := RunFns{GlobalScope: true}
	instance.init()
	api, err := yaml.Parse(`apiVersion: apps/v1
kind: 
`)
	if !assert.NoError(t, err) {
		return
	}
	filter := instance.containerFilterProvider("example.com:version", "", api)
	assert.Equal(t, &filters.ContainerFilter{
		Image: "example.com:version", Config: api, GlobalScope: true}, filter)
}

var tru = true
var fls = false

// TestRunFns_getFilters tests how filters are found and sorted
func TestRunFns_getFilters(t *testing.T) {
	type f struct {
		// path to function file and string value to write
		path, value string
		// if true, create the function in a separate directory from
		// the config, and provide it through FunctionPaths
		outOfPackage bool
		// if true and outOfPackage is true, create a new directory
		// for this function separate from the previous one.  If
		// false and outOfPackage is true, create the function in
		// the directory created for the last outOfPackage function.
		newFnPath bool
	}
	var tests = []struct {
		// function files to write
		in []f
		// images to be run in a specific order
		out []string
		// name of the test
		name string
		// value to set for NoFunctionsFromInput
		noFunctionsFromInput *bool
	}{
		// Test
		//
		//
		{name: "single implicit function",
			in: []f{
				{
					path: filepath.Join("foo", "bar.yaml"),
					value: `
apiVersion: example.com/v1alpha1
kind: ExampleFunction
metadata:
  annotations:
    config.kubernetes.io/function: |
      container:
        image: gcr.io/example.com/image:v1.0.0
    config.kubernetes.io/local-config: "true"
`,
				},
			},
			out: []string{"gcr.io/example.com/image:v1.0.0"},
		},

		// Test
		//
		//
		{name: "sort functions -- deepest first",
			in: []f{
				{
					path: filepath.Join("a.yaml"),
					value: `
metadata:
  annotations:
    config.kubernetes.io/function: |
      container:
        image: a
`,
				},
				{
					path: filepath.Join("foo", "b.yaml"),
					value: `
metadata:
  annotations:
    config.kubernetes.io/function: |
      container:
        image: b
`,
				},
			},
			out: []string{"b", "a"},
		},

		// Test
		//
		//
		{name: "sort functions -- skip implicit with output of package",
			in: []f{
				{
					path:         filepath.Join("foo", "a.yaml"),
					outOfPackage: true, // out of package is run last
					value: `
metadata:
  annotations:
    config.kubernetes.io/function: |
      container:
        image: a
`,
				},
				{
					path: filepath.Join("b.yaml"),
					value: `
metadata:
  annotations:
    config.kubernetes.io/function: |
      container:
        image: b
`,
				},
			},
			out: []string{"a"},
		},

		// Test
		//
		//
		{name: "sort functions -- skip implicit",
			noFunctionsFromInput: &tru,
			in: []f{
				{
					path: filepath.Join("foo", "a.yaml"),
					value: `
metadata:
  annotations:
    config.kubernetes.io/function: |
      container:
        image: a
`,
				},
				{
					path: filepath.Join("b.yaml"),
					value: `
metadata:
  annotations:
    config.kubernetes.io/function: |
      container:
        image: b
`,
				},
			},
			out: nil,
		},

		// Test
		//
		//
		{name: "sort functions -- include implicit",
			noFunctionsFromInput: &fls,
			in: []f{
				{
					path: filepath.Join("foo", "a.yaml"),
					value: `
metadata:
  annotations:
    config.kubernetes.io/function: |
      container:
        image: a
`,
				},
				{
					path: filepath.Join("b.yaml"),
					value: `
metadata:
  annotations:
    config.kubernetes.io/function: |
      container:
        image: b
`,
				},
			},
			out: []string{"a", "b"},
		},

		// Test
		//
		//
		{name: "sort functions -- implicit first",
			noFunctionsFromInput: &fls,
			in: []f{
				{
					path:         filepath.Join("foo", "a.yaml"),
					outOfPackage: true, // out of package is run last
					value: `
metadata:
  annotations:
    config.kubernetes.io/function: |
      container:
        image: a
`,
				},
				{
					path: filepath.Join("b.yaml"),
					value: `
metadata:
  annotations:
    config.kubernetes.io/function: |
      container:
        image: b
`,
				},
			},
			out: []string{"b", "a"},
		},
	}

	for i := range tests {
		tt := tests[i]
		t.Run(tt.name, func(t *testing.T) {
			// setup the test directory
			d := setupTest(t)
			defer os.RemoveAll(d)

			// write the functions to files
			var fnPaths []string
			var fnPath string
			var err error
			for _, f := range tt.in {
				// get the location for the file
				var dir string
				if f.outOfPackage {
					// if out of package, write to a separate temp directory
					if f.newFnPath || fnPath == "" {
						// create a new fn directory
						fnPath, err = ioutil.TempDir("", "kustomize-test")
						if !assert.NoError(t, err) {
							t.FailNow()
						}
						defer os.RemoveAll(fnPath)
						fnPaths = append(fnPaths, fnPath)
					}
					dir = fnPath
				} else {
					// if in package, write to the dir containing the configs
					dir = d
				}

				// create the parent dir and write the file
				err = os.MkdirAll(filepath.Join(dir, filepath.Dir(f.path)), 0700)
				if !assert.NoError(t, err) {
					t.FailNow()
				}
				err := ioutil.WriteFile(filepath.Join(dir, f.path), []byte(f.value), 0600)
				if !assert.NoError(t, err) {
					t.FailNow()
				}
			}

			// init the instance
			r := &RunFns{
				FunctionPaths:        fnPaths,
				Path:                 d,
				NoFunctionsFromInput: tt.noFunctionsFromInput,
			}
			r.init()

			// get the filters which would be run
			var results []string
			fltrs, err := r.getFilters()
			if !assert.NoError(t, err) {
				t.FailNow()
			}
			for _, f := range fltrs {
				results = append(results, strings.TrimSpace(fmt.Sprintf("%v", f)))
			}

			// compare the actual ordering to the expected ordering
			if !assert.Equal(t, tt.out, results) {
				t.FailNow()
			}
		})
	}
}

func TestCmd_Execute(t *testing.T) {
	dir := setupTest(t)
	defer os.RemoveAll(dir)

	// write a test filter to the directory of configuration
	if !assert.NoError(t, ioutil.WriteFile(
		filepath.Join(dir, "filter.yaml"), []byte(ValueReplacerYAMLData), 0600)) {
		return
	}

	instance := RunFns{Path: dir, containerFilterProvider: getFilterProvider(t)}
	if !assert.NoError(t, instance.Execute()) {
		t.FailNow()
	}
	b, err := ioutil.ReadFile(
		filepath.Join(dir, "java", "java-deployment.resource.yaml"))
	if !assert.NoError(t, err) {
		t.FailNow()
	}
	assert.Contains(t, string(b), "kind: StatefulSet")
}

func TestCmd_Execute_setFunctionPaths(t *testing.T) {
	dir := setupTest(t)
	defer os.RemoveAll(dir)

	// write a test filter to a separate directory
	tmpF, err := ioutil.TempFile("", "filter*.yaml")
	if !assert.NoError(t, err) {
		return
	}
	os.RemoveAll(tmpF.Name())
	if !assert.NoError(t, ioutil.WriteFile(tmpF.Name(), []byte(ValueReplacerYAMLData), 0600)) {
		return
	}

	// run the functions, providing the path to the directory of filters
	instance := RunFns{
		FunctionPaths:           []string{tmpF.Name()},
		Path:                    dir,
		containerFilterProvider: getFilterProvider(t),
	}
	err = instance.Execute()
	if !assert.NoError(t, err) {
		return
	}
	b, err := ioutil.ReadFile(
		filepath.Join(dir, "java", "java-deployment.resource.yaml"))
	if !assert.NoError(t, err) {
		return
	}
	assert.Contains(t, string(b), "kind: StatefulSet")
}

func TestCmd_Execute_setOutput(t *testing.T) {
	dir := setupTest(t)
	defer os.RemoveAll(dir)

	// write a test filter
	if !assert.NoError(t, ioutil.WriteFile(
		filepath.Join(dir, "filter.yaml"), []byte(ValueReplacerYAMLData), 0600)) {
		return
	}

	out := &bytes.Buffer{}
	instance := RunFns{
		Output:                  out, // write to out
		Path:                    dir,
		containerFilterProvider: getFilterProvider(t),
	}

	if !assert.NoError(t, instance.Execute()) {
		return
	}
	b, err := ioutil.ReadFile(
		filepath.Join(dir, "java", "java-deployment.resource.yaml"))
	if !assert.NoError(t, err) {
		return
	}
	assert.NotContains(t, string(b), "kind: StatefulSet")
	assert.Contains(t, out.String(), "kind: StatefulSet")
}

// setupTest initializes a temp test directory containing test data
func setupTest(t *testing.T) string {
	dir, err := ioutil.TempDir("", "kustomize-kyaml-test")
	if !assert.NoError(t, err) {
		t.FailNow()
	}

	_, filename, _, ok := runtime.Caller(0)
	if !assert.True(t, ok) {
		t.FailNow()
	}
	ds, err := filepath.Abs(filepath.Join(filepath.Dir(filename), "test", "testdata"))
	if !assert.NoError(t, err) {
		t.FailNow()
	}
	if !assert.NoError(t, copyutil.CopyDir(ds, dir)) {
		t.FailNow()
	}
	if !assert.NoError(t, os.Chdir(filepath.Dir(dir))) {
		t.FailNow()
	}
	return dir
}

// getFilterProvider fakes the creation of a filter, replacing the ContainerFiler with
// a filter to s/kind: Deployment/kind: StatefulSet/g.
// this can be used to simulate running a filter.
func getFilterProvider(t *testing.T) func(string, string, *yaml.RNode) kio.Filter {
	return func(s, _ string, node *yaml.RNode) kio.Filter {
		// parse the filter from the input
		filter := yaml.YFilter{}
		b := &bytes.Buffer{}
		e := yaml.NewEncoder(b)
		if !assert.NoError(t, e.Encode(node.YNode())) {
			t.FailNow()
		}
		e.Close()
		d := yaml.NewDecoder(b)
		if !assert.NoError(t, d.Decode(&filter)) {
			t.FailNow()
		}

		return filters.Modifier{
			Filters: []yaml.YFilter{{Filter: yaml.Lookup("kind")}, filter},
		}
	}
}
