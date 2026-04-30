package sharedeaterylist

import (
	"context"
)

func (f *SharedEateryListFeature) addEatery(ctx context.Context, tenantID string, input EateryInput, source sourceMeta) (*AddEateryResult, error) {
	if f == nil || f.store == nil {
		return nil, errEateryNotFound
	}
	applySource(&input, source)
	stored, err := validateEateryInput(input)
	if err != nil {
		return nil, err
	}
	entry, created, err := f.store.addEatery(tenantID, stored)
	if err != nil {
		return nil, err
	}
	return &AddEateryResult{
		Entry:             entry,
		Created:           created,
		DuplicateDetected: !created,
	}, nil
}

func (f *SharedEateryListFeature) listEateries(_ context.Context, tenantID string, filter EateryFilter) (*ListEateriesResult, error) {
	if f == nil || f.store == nil {
		return nil, errEateryNotFound
	}
	filter = normalizeFilter(filter)
	entries, err := f.store.listEateries(tenantID, filter)
	if err != nil {
		return nil, err
	}
	return &ListEateriesResult{
		Entries: entries,
		Count:   len(entries),
		Filters: filter,
	}, nil
}

func (f *SharedEateryListFeature) randomEatery(_ context.Context, tenantID string, filter EateryFilter) (*RandomEateryResult, error) {
	if f == nil || f.store == nil {
		return nil, errEateryNotFound
	}
	filter = normalizeFilter(filter)
	entry, err := f.store.randomEatery(tenantID, filter)
	if err != nil {
		return nil, err
	}
	return &RandomEateryResult{
		Entry:   entry,
		Filters: filter,
	}, nil
}
