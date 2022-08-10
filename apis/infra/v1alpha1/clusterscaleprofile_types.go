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

//+kubebuilder:object:root=true

// ClusterScaleProfile is the Schema for the clusterscaleprofiles API
type ClusterScaleProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	ClusterScaleProfileSpec `json:"spec,omitempty"`
}

type ClusterScaleProfileSpec struct {
	// Autoscaling indicates whether or not this cluster supports cluster
	// autoscaler.
	Autoscaling *bool `json:"autoscaling,omitempty"`

	// NodeMax indicates the number of nodes for non-autoscaling clusters,
	// and the maximum number of nodes for autoscaling clusters
	NodeMax *int32 `json:"nodeMax,omitempty"`

	// SiteDensity indicates the population density at the site in which the
	// cluster is located.
	SiteDensity *string `json:"siteDensity,omitempty"`
}

//+kubebuilder:object:root=true

// ClusterScaleProfileList contains a list of ClusterScaleProfile
type ClusterScaleProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterScaleProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterScaleProfile{}, &ClusterScaleProfileList{})
}
