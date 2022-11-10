/*
Copyright 2022 The Nephio Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RepositoryReference is used to refer to a repository resource.
type RepositoryReference struct {
	// Namespace defines the space within which the repository name must be unique.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Name is unique within a namespace to reference a repository resource.
	Name string `json:"name"`
}

// PackageRevisionReference is used to reference a particular package revision.
type PackageRevisionReference struct {
	// Namespace is the namespace for both the repository and package revision
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Repository is the name of the repository containing the package
	RepositoryName string `json:"repository"`

	// PackageName is the name of the package for the revision
	PackageName string `json:"packageName"`

	// Revision is the specific version number of the revision of the package
	Revision string `json:"revision"`
}

// PackageDeploymentSpec defines the desired state of PackageDeployment
type PackageDeploymentSpec struct {
	// Label selector for Clusters on which to deploy the package
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// PackageRef identifies the package revision to deploy
	PackageRef PackageRevisionReference `json:"packageRef"`

	// Namespace identifies the namespace in which to deploy the package
	// The namespace will be added to the resource list of the package
	// If not present, the package will be installed in the default namespace
	Namespace *string `json:"namespace,omitempty"`
}

// PackageDeploymentStatus defines the observed state of PackageDeployment
type PackageDeploymentStatus struct {
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// PackageDeployment is the Schema for the packagedeployments API
type PackageDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PackageDeploymentSpec   `json:"spec,omitempty"`
	Status PackageDeploymentStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// PackageDeploymentList contains a list of PackageDeployment
type PackageDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PackageDeployment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PackageDeployment{}, &PackageDeploymentList{})
}
