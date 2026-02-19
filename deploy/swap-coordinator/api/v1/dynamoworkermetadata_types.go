// SPDX-FileCopyrightText: Copyright (c) 2024-2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package v1 contains API Schema definitions for the nvidia.com/v1alpha1 API group
package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// NOTE: This Go implementation mirrors the existing DynamoWorkerMetadata CRD
// defined in the Helm chart at:
// deploy/helm/charts/crds/templates/nvidia.com_dynamoworkermetadatas.yaml
//
// The Rust implementation can be found at:
// lib/runtime/src/discovery/kube/crd.rs

var (
	// GroupVersion is the API group version used for the CRD
	GroupVersion = schema.GroupVersion{Group: "nvidia.com", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group-version to the given scheme
	AddToScheme = SchemeBuilder.AddToScheme
)

// DynamoWorkerMetadataSpec defines the desired state of DynamoWorkerMetadata
// The Data field stores the serialized DiscoveryMetadata as a JSON blob.
type DynamoWorkerMetadataSpec struct {
	// Data is a raw JSON blob containing the DiscoveryMetadata
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	Data runtime.RawExtension `json:"data" yaml:"data"`
}

// DynamoWorkerMetadataStatus defines the observed state of DynamoWorkerMetadata
// Currently unused, but included for future extensibility
type DynamoWorkerMetadataStatus struct {
}

// DynamoWorkerMetadata is the Schema for the dynamoworkermetadatas API
// It stores discovery metadata for a Dynamo worker pod in Kubernetes.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=dwm
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type DynamoWorkerMetadata struct {
	metav1.TypeMeta   `json:",inline" yaml:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty" yaml:"metadata,omitempty"`

	Spec   DynamoWorkerMetadataSpec   `json:"spec,omitempty" yaml:"spec,omitempty"`
	Status DynamoWorkerMetadataStatus `json:"status,omitempty" yaml:"status,omitempty"`
}

// DynamoWorkerMetadataList contains a list of DynamoWorkerMetadata
//
// +kubebuilder:object:root=true
type DynamoWorkerMetadataList struct {
	metav1.TypeMeta `json:",inline" yaml:",inline"`
	metav1.ListMeta `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	Items           []DynamoWorkerMetadata `json:"items" yaml:"items"`
}

// addKnownTypes adds the list of known types to the given scheme
func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&DynamoWorkerMetadata{},
		&DynamoWorkerMetadataList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
