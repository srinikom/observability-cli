package injector

import (
	"bufio"
	"bytes"
	"github.com/pkg/errors"
	"io"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kyamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/kube-openapi/pkg/util/sets"
	"os"
	"path/filepath"
)

type (
	Injector interface {
		// Injects the given sidecar into all valid Deployment Objects and returns the result as a list of unstructured objects.
		Inject(sidecar v1.Container) ([]*unstructured.Unstructured, error)
		// Returns a list of namespaces that contain injectable objects.
		InjectableNamespaces() ([]string, error)
	}
	injectorImpl struct {
		// The list of Kubernetes objects to traverse during injection. This is a list of
		// unstructured objects because we likely won't know the type of all objects
		// ahead of time (e.g., when reading multiple objects from a YAML file).
		objects []*unstructured.Unstructured
	}
)

// Constructs a new Injector with Kubernetes objects derived from the given file path.
func FromYAML(filePath string) (Injector, error) {
	yamlContent, err := getFile(filePath)
	if err != nil {
		return nil, err
	}

	// Read the YAML file into a list of unstructured objects.
	// This is necessary because the YAML file may contain multiple Kubernetes objects.
	// We only want to inject the sidecar into Deployment objects, but we still need to parse all resources.
	multidocReader := kyamlutil.NewYAMLReader(bufio.NewReader(bytes.NewReader(yamlContent)))

	var objList []*unstructured.Unstructured
	for {
		raw, err := multidocReader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}

		obj, err := fromRawObject(raw)
		if err != nil {
			return nil, err
		}

		result, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			return nil, err
		}

		objList = append(objList, &unstructured.Unstructured{Object: result})
	}

	return &injectorImpl{objects: objList}, nil
}

func (i *injectorImpl) InjectableNamespaces() ([]string, error) {
	set := make(map[string]struct{})
	for _, obj := range i.objects {
		gvk := obj.GetObjectKind().GroupVersionKind()

		if !isInjectable(gvk) {
			continue
		}

		var deployment *appsv1.Deployment
		err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &deployment)
		if err != nil {
			return nil, err
		}

		if deployment.Namespace == "" {
			set["default"] = struct{}{}
		} else {
			set[deployment.Namespace] = struct{}{}
		}
	}

	return sets.StringKeySet(set).List(), nil
}

func (i *injectorImpl) Inject(sidecar v1.Container) ([]*unstructured.Unstructured, error) {
	onMap := func(obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
		out := obj.DeepCopy()
		gvk := out.GetObjectKind().GroupVersionKind()

		if !isInjectable(gvk) {
			return out, nil
		}

		var deployment *appsv1.Deployment
		err := runtime.DefaultUnstructuredConverter.FromUnstructured(out.Object, &deployment)
		if err != nil {
			return nil, err
		}

		injectedDeployment := deployment.DeepCopy()
		if err != nil {
			return nil, err
		}

		containers := injectedDeployment.Spec.Template.Spec.Containers
		injectedDeployment.Spec.Template.Spec.Containers = append(containers, sidecar)

		if err != nil {
			return nil, err
		}

		out.Object, err = runtime.DefaultUnstructuredConverter.ToUnstructured(injectedDeployment)
		if err != nil {
			return nil, err
		}

		return out, nil
	}

	return mapUnstructured(i.objects, onMap)
}

func isInjectable(kind schema.GroupVersionKind) bool {
	return kind.Group == "apps" && kind.Version == "v1" && kind.Kind == "Deployment"
}

// fromRawObject converts the given raw Kubernetes object into a Kubernetes object.
func fromRawObject(raw []byte) (*unstructured.Unstructured, error) {
	jConfigMap, err := kyamlutil.ToJSON(raw)
	if err != nil {
		return nil, err
	}

	object, err := runtime.Decode(unstructured.UnstructuredJSONScheme, jConfigMap)
	if err != nil {
		return nil, err
	}

	unstruct, ok := object.(*unstructured.Unstructured)
	if !ok {
		return nil, errors.New("unstructured conversion failed")
	}

	return unstruct, nil
}

func getFile(filePath string) ([]byte, error) {
	fileDir, fileName := filepath.Split(filePath)

	absOutputDir, err := filepath.Abs(fileDir)
	if err != nil {
		return nil, err
	}

	// Check for directory existence
	if _, staterr := os.Stat(absOutputDir); os.IsNotExist(staterr) {
		return nil, errors.Wrapf(staterr, "directory %s does not exist", absOutputDir)
	}

	absPath := filepath.Join(absOutputDir, fileName)

	// Check for existence of file
	if _, staterr := os.Stat(absPath); os.IsNotExist(staterr) {
		return nil, errors.Wrapf(staterr, "file %s does not exist", absPath)
	}

	return os.ReadFile(absPath)
}

// mapUnstructured applies the given transformer function to each unstructured Kubernetes item in the given list.
// If the transformer function returns an error, the error is returned immediately.
func mapUnstructured(
	objList []*unstructured.Unstructured,
	transformer func(*unstructured.Unstructured) (*unstructured.Unstructured, error),
) ([]*unstructured.Unstructured, error) {
	if objList == nil {
		return nil, nil
	}

	results := make([]*unstructured.Unstructured, 0, len(objList))
	for _, item := range objList {
		result, err := transformer(item)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}

	return results, nil
}
