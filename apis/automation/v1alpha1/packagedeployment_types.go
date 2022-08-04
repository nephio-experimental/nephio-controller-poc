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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// PackageDeploymentSpec defines the desired state of PackageDeployment
type PackageDeploymentSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Foo is an example field of PackageDeployment. Edit packagedeployment_types.go to remove/update
	Foo string `json:"foo,omitempty"`
}

// PackageDeploymentStatus defines the observed state of PackageDeployment
type PackageDeploymentStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
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
