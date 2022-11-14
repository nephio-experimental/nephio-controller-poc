package porch

import (
	"bytes"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/kustomize/kyaml/kio"
	"sigs.k8s.io/kustomize/kyaml/kio/kioutil"
	"sigs.k8s.io/kustomize/kyaml/yaml"
)

// Code adapted from Porch internal cmdrpkgpull and cmdrpkgpush
func ResourcesToPackageBuffer(resources map[string]string) (*kio.PackageBuffer, error) {
	keys := make([]string, 0, len(resources))
	for k := range resources {
		if !includeFile(k) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Create kio readers
	inputs := []kio.Reader{}
	for _, k := range keys {
		v := resources[k]
		inputs = append(inputs, &kio.ByteReader{
			Reader: strings.NewReader(v),
			SetAnnotations: map[string]string{
				kioutil.PathAnnotation: k,
			},
			DisableUnwrapping: true,
		})
	}

	var pb kio.PackageBuffer
	err := kio.Pipeline{
		Inputs:  inputs,
		Outputs: []kio.Writer{&pb},
	}.Execute()

	if err != nil {
		return nil, err
	}

	return &pb, nil
}

type resourceWriter struct {
	resources map[string]string
}

var _ kio.Writer = &resourceWriter{}

func (w *resourceWriter) Write(nodes []*yaml.RNode) error {
	paths := map[string][]*yaml.RNode{}
	for _, node := range nodes {
		path := getPath(node)
		paths[path] = append(paths[path], node)
	}

	buf := &bytes.Buffer{}
	for path, nodes := range paths {
		bw := kio.ByteWriter{
			Writer: buf,
			ClearAnnotations: []string{
				kioutil.PathAnnotation,
				kioutil.IndexAnnotation,
			},
		}
		if err := bw.Write(nodes); err != nil {
			return err
		}
		w.resources[path] = buf.String()
		buf.Reset()
	}
	return nil
}

func getPath(node *yaml.RNode) string {
	ann := node.GetAnnotations()
	if path, ok := ann[kioutil.PathAnnotation]; ok {
		return path
	}
	ns := node.GetNamespace()
	if ns == "" {
		ns = "non-namespaced"
	}
	name := node.GetName()
	if name == "" {
		name = "unnamed"
	}
	// TODO: harden for escaping etc.
	return path.Join(ns, fmt.Sprintf("%s.yaml", name))
}

func CreateUpdatedResources(origResources map[string]string, pb *kio.PackageBuffer) (map[string]string, error) {
	newResources := make(map[string]string, len(origResources))
	for k, v := range origResources {
		// Copy ALL non-KRM files
		if !includeFile(k) {
			newResources[k] = v
		}
	}

	// Copy the KRM resources from the PackageBuffer
	rw := &resourceWriter{
		resources: newResources,
	}

	if err := (kio.Pipeline{
		Inputs:  []kio.Reader{pb},
		Outputs: []kio.Writer{rw},
	}.Execute()); err != nil {
		return nil, err
	}
	return rw.resources, nil
}

// import fails with kptfile/v1
var matchResourceContents = append(kio.MatchAll, "Kptfile")

func includeFile(path string) bool {
	for _, m := range matchResourceContents {
		file := filepath.Base(path)
		if matched, err := filepath.Match(m, file); err == nil && matched {
			return true
		}
	}
	return false
}
