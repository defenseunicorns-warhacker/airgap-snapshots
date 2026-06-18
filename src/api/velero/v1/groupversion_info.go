// Copyright 2026 Defense Unicorns
// SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Defense-Unicorns-Commercial

// Package v1 is a MINIMAL, hand-maintained subset of the Velero velero.io/v1
// API — only the fields Snapback reads. It exists so the operator can watch and
// resolve Velero resources without taking a dependency on the entire Velero
// module (and its transitive version constraints).
//
// To switch to the upstream types later, replace imports of this package with
// "github.com/vmware-tanzu/velero/pkg/apis/velero/v1"; the field names here
// match upstream so the swap is mechanical.
//
// +kubebuilder:object:generate=true
// +groupName=velero.io
package v1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

// GroupVersion is the velero.io/v1 group/version.
var GroupVersion = schema.GroupVersion{Group: "velero.io", Version: "v1"}

// SchemeBuilder registers the (subset) Velero types with a runtime.Scheme.
var SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

// AddToScheme adds the (subset) Velero types to a runtime.Scheme.
var AddToScheme = SchemeBuilder.AddToScheme
