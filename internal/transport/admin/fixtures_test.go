// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0
package admin

import (
	"context"

	"go.trippwill.dev/wake/internal/application"
)

func fixtureSource(v fixtureView) sourceView {
	connections := append([]application.ConnectionView(nil), v.Connections...)
	for i := range connections {
		if connections[i].ID == v.Account.Connection() {
			connections[i].Account = v.Account
		}
	}

	return sourceView{
		Prerequisites: application.Prerequisites{
			Storage:          v.Storage,
			Account:          v.Account,
			ReferenceKey:     v.ReferenceKey,
			ReferenceFailure: v.ReferenceFailure,
			ConnectionCount:  int64(len(v.Connections)),
		},
		Connections: connections,
	}
}

func fixtureOverview(v fixtureView) overviewView { return overviewView{sourceView: fixtureSource(v)} }

func (s workspaceStore) Accounts(context.Context) ([]application.Account, error) {
	if s.view.Account.ID() == "" {
		return nil, nil
	}

	return []application.Account{s.view.Account}, nil
}

func (s workspaceStore) Prerequisites(context.Context) (application.Prerequisites, error) {
	return fixtureSource(s.view).Prerequisites, nil
}

func (s workspaceStore) SourcePage(context.Context, application.ListQuery) (application.SourcePage, error) {
	v := fixtureSource(s.view)
	return application.SourcePage{Prerequisites: v.Prerequisites, Connections: v.Connections}, nil
}

func (s workspaceStore) SourceDetail(
	_ context.Context,
	id string,
	_ application.ListQuery,
) (application.SourceDetail, error) {
	v := fixtureSource(s.view)
	for _, c := range v.Connections {
		if c.ID == id {
			return application.SourceDetail{Prerequisites: v.Prerequisites, Connection: c}, nil
		}
	}

	return application.SourceDetail{}, application.ErrNotFound
}

func (s workspaceStore) SystemStatus(context.Context) (application.SystemStatus, error) {
	v := fixtureSource(s.view)

	return application.SystemStatus{
		Prerequisites: v.Prerequisites,
		Connections:   v.Connections,
	}, nil
}

func (s workspaceStore) Overview(ctx context.Context) (application.OverviewView, error) {
	v, _ := s.SystemStatus(ctx)
	return application.OverviewView{Status: v}, nil
}

func (s workspaceStore) ActivityList(
	context.Context,
	application.ListQuery,
) (application.ActivityList, error) {
	return application.ActivityList{}, nil
}

func (s workspaceStore) InstrumentList(
	context.Context,
	application.ListQuery,
) (application.InstrumentList, error) {
	return application.InstrumentList{}, nil
}

func (s workspaceStore) ReviewList(context.Context, application.ListQuery) (application.ReviewList, error) {
	return application.ReviewList{}, nil
}

type fixtureView struct {
	Storage          application.StorageStatus
	Account          application.Account
	Connections      []application.ConnectionView
	ReferenceKey     bool
	ReferenceFailure string
}

func (s workspaceStore) SourceAccessPage(
	ctx context.Context,
	query application.ListQuery,
) (application.SourcePage, error) {
	return s.SourcePage(ctx, query)
}

func (s workspaceStore) SourceAccess(ctx context.Context) (application.AccessSnapshot, error) {
	v, err := s.SourcePage(ctx, application.ListQuery{})
	return application.AccessSnapshot{Prerequisites: v.Prerequisites, Connections: v.Connections}, err
}

func (s workspaceStore) PositionsList(
	context.Context,
	application.ListQuery,
) (application.PositionsList, error) {
	return application.PositionsList{Account: s.view.Account}, nil
}
