// Copyright 2026 Defense Unicorns
// SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Defense-Unicorns-Commercial

// Package v1alpha1 contains the snapback.uds.dev/v1alpha1 API types.
//
// +kubebuilder:object:generate=true
// +groupName=snapback.uds.dev
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

// GroupVersion is the group/version for the Snapback API.
var GroupVersion = schema.GroupVersion{Group: "snapback.uds.dev", Version: "v1alpha1"}

// SchemeBuilder registers the Snapback types with a runtime.Scheme.
var SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

// AddToScheme adds the Snapback types to a runtime.Scheme.
var AddToScheme = SchemeBuilder.AddToScheme
