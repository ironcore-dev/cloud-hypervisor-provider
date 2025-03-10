// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"

	"github.com/ironcore-dev/cloud-hypervisor-provider/api"
	irievent "github.com/ironcore-dev/ironcore/iri/apis/event/v1alpha1"
	iri "github.com/ironcore-dev/ironcore/iri/apis/machine/v1alpha1"
	"github.com/ironcore-dev/provider-utils/eventutils/recorder"
	"k8s.io/apimachinery/pkg/labels"
)

func (s *Server) filterEvents(events []*recorder.Event, filter *iri.EventFilter) []*recorder.Event {
	if filter == nil {
		return events
	}

	var (
		res []*recorder.Event
		sel = labels.SelectorFromSet(filter.LabelSelector)
	)
	for _, iriEvent := range events {
		if !sel.Matches(labels.Set(iriEvent.InvolvedObjectMeta.Labels)) {
			continue
		}

		if filter.EventsFromTime > 0 && filter.EventsToTime > 0 {
			if iriEvent.EventTime < filter.EventsFromTime || iriEvent.EventTime > filter.EventsToTime {
				continue
			}
		}

		res = append(res, iriEvent)
	}
	return res
}

func (s *Server) convertEventToIRIEvent(events []*recorder.Event) ([]*irievent.Event, error) {
	var (
		res []*irievent.Event
	)
	for _, event := range events {
		metadata, err := api.GetObjectMetadata(event.InvolvedObjectMeta)
		if err != nil {
			return nil, fmt.Errorf("failed to get object metadata: %w", err)
		}

		res = append(res, &irievent.Event{
			Spec: &irievent.EventSpec{
				InvolvedObjectMeta: metadata,
				Reason:             event.Reason,
				Message:            event.Message,
				Type:               event.Type,
				EventTime:          event.EventTime,
			},
		})
	}
	return res, nil
}

func (s *Server) ListEvents(ctx context.Context, req *iri.ListEventsRequest) (*iri.ListEventsResponse, error) {
	events := s.filterEvents(s.eventStore.ListEvents(), req.Filter)

	iriEvents, err := s.convertEventToIRIEvent(events)
	if err != nil {
		return nil, err
	}

	return &iri.ListEventsResponse{
		Events: iriEvents,
	}, nil
}
