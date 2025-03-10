// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"fmt"

	"github.com/ironcore-dev/controller-utils/metautils"
	irimeta "github.com/ironcore-dev/ironcore/iri/apis/meta/v1alpha1"
	apiutils "github.com/ironcore-dev/provider-utils/apiutils/api"
)

func GetObjectMetadata(o apiutils.Metadata) (*irimeta.ObjectMetadata, error) {
	annotations, err := GetAnnotationsAnnotation(o)
	if err != nil {
		return nil, err
	}

	labels, err := GetLabelsAnnotation(o)
	if err != nil {
		return nil, err
	}

	var deletedAt int64
	if o.DeletedAt != nil && !o.DeletedAt.IsZero() {
		deletedAt = o.DeletedAt.UnixNano()
	}

	return &irimeta.ObjectMetadata{
		Id:          o.ID,
		Annotations: annotations,
		Labels:      labels,
		Generation:  o.GetGeneration(),
		CreatedAt:   o.CreatedAt.UnixNano(),
		DeletedAt:   deletedAt,
	}, nil
}

func SetObjectMetadata(o apiutils.Object, metadata *irimeta.ObjectMetadata) error {
	if err := SetAnnotationsAnnotation(o, metadata.Annotations); err != nil {
		return err
	}
	if err := SetLabelsAnnotation(o, metadata.Labels); err != nil {
		return err
	}
	return nil
}

func SetLabelsAnnotation(o apiutils.Object, labels map[string]string) error {
	data, err := json.Marshal(labels)
	if err != nil {
		return fmt.Errorf("error marshalling labels: %w", err)
	}
	metautils.SetAnnotation(o, LabelsAnnotation, string(data))
	return nil
}

func GetLabelsAnnotation(o apiutils.Metadata) (map[string]string, error) {
	data, ok := o.GetAnnotations()[LabelsAnnotation]
	if !ok {
		return nil, fmt.Errorf("object has no labels at %s", LabelsAnnotation)
	}

	var labels map[string]string
	if err := json.Unmarshal([]byte(data), &labels); err != nil {
		return nil, err
	}

	return labels, nil
}

func SetAnnotationsAnnotation(o apiutils.Object, annotations map[string]string) error {
	data, err := json.Marshal(annotations)
	if err != nil {
		return fmt.Errorf("error marshalling annotations: %w", err)
	}
	metautils.SetAnnotation(o, AnnotationsAnnotation, string(data))

	return nil
}

func GetAnnotationsAnnotation(o apiutils.Metadata) (map[string]string, error) {
	data, ok := o.GetAnnotations()[AnnotationsAnnotation]
	if !ok {
		return nil, fmt.Errorf("object has no annotations at %s", AnnotationsAnnotation)
	}

	var annotations map[string]string
	if err := json.Unmarshal([]byte(data), &annotations); err != nil {
		return nil, err
	}

	return annotations, nil
}

func SetManagerLabel(o apiutils.Object, manager string) {
	metautils.SetLabel(o, ManagerLabel, manager)
}

func SetClassLabel(o apiutils.Object, class string) {
	metautils.SetLabel(o, ClassLabel, class)
}

func GetClassLabel(o apiutils.Object) (string, bool) {
	class, found := o.GetLabels()[ClassLabel]
	return class, found
}

func IsManagedBy(o apiutils.Object, manager string) bool {
	actual, ok := o.GetLabels()[ManagerLabel]
	return ok && actual == manager
}
