// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package devenv

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/config"
	"go.trippwill.dev/wake/internal/credential"
	"go.trippwill.dev/wake/internal/devfixture"
	"go.trippwill.dev/wake/internal/host"
	persistence "go.trippwill.dev/wake/internal/persistence/sqlite"
	"go.trippwill.dev/wake/internal/reference"
	"go.trippwill.dev/wake/internal/reference/figi"
	"go.trippwill.dev/wake/providers/ibkrflex"
)

func advanceReport(
	ctx context.Context,
	cfg config.Config,
	profile persistence.Profile,
	stage string,
) (result error) {
	store, err := persistence.Open(ctx, profile, nil)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, store.Close()) }()

	if stage == "identity-merge" {
		q, _ := application.NewListQuery(application.ListOptions{}, application.InstrumentScope)

		roots, err := store.InstrumentList(ctx, q)
		if err != nil {
			return err
		}

		matched := 0
		for _, item := range roots.Items {
			detail, e := store.InstrumentDetail(ctx, item.Resource.ID)
			if e != nil {
				return e
			}

			switch detail.Instrument.Reported().Reference().Value() {
			case "99001001", "99009999":
			default:
				continue
			}

			e = store.ApplyReference(
				ctx,
				item.Resource.ID,
				reference.Result{
					Status: "resolved",
					Next:   time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC),
					Records: []figi.Record{
						{
							FIGI:         "BBG000BPHFS9",
							Composite:    "BBG000BPH459",
							ShareClass:   "BBG001S5TD05",
							Market:       "Equity",
							SecurityType: "Common Stock",
						},
					},
				},
			)
			if e != nil {
				return e
			}

			matched++
		}

		if matched != 2 {
			return errors.New("expected two synthetic instrument roots")
		}

		return nil
	}

	initial, err := devfixture.ReadReport("confirmations/initial.xml")
	if err != nil {
		return err
	}

	report := string(initial)
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	if stage == "acquisition" {
		row := regexp.MustCompile(`<TradeConfirm [^>]*tradeID="900000001"[^>]*/>`).FindString(report)
		row = strings.NewReplacer(`conid="99001001"`, `conid="99009999"`, `tradeID="900000001"`, `tradeID="concurrent-trade"`, `execID="0000F001.00000001.01.01"`, `execID="concurrent-fill"`).
			Replace(row)
		report = strings.Replace(report, "</TradeConfirms>", row+"</TradeConfirms>", 1)
	} else {
		now = now.Add(24 * time.Hour)

		row := regexp.MustCompile(`<TradeConfirm [^>]*tradeID="900000002"[^>]*/>`).FindString(report)
		pending := strings.NewReplacer(`origTradeID=""`, `origTradeID="late-original"`, `code="O;P"`, `code="Co"`).
			Replace(row)
		report = strings.Replace(report, row, pending, 1)
		if stage == "source-resolution" {
			report = strings.Replace(report, `tradeID="900000001"`, `tradeID="late-original"`, 1)
			now = now.Add(24 * time.Hour)
		}
	}

	upstream := httptest.NewTLSServer(devfixture.Upstream(func() []byte { return []byte(report) }, nil))
	defer upstream.Close()

	client := upstream.Client()
	client.Transport = fixtureTransport{client.Transport, upstream.URL}

	reg, err := ibkrflex.Registration(ibkrflex.Options{HTTPClient: client, BaseURL: upstream.URL})
	if err != nil {
		return err
	}

	key, err := credential.LoadMasterKey(cfg.Credentials.KeyPath)
	if err != nil {
		return err
	}
	defer key.Clear()

	envelope, err := credential.NewEnvelope(key)
	if err != nil {
		return err
	}
	defer envelope.Clear()

	clock := func() time.Time { return now }

	h, err := host.New(ctx, store, envelope, nil, clock, nil, reg)
	if err != nil {
		return err
	}
	defer h.Close()

	flow := application.New(store, h.Registrations(), h, nil, clock, nil)

	data, err := os.ReadFile(filepath.Join(cfg.Paths.ConfigDir, "connection.id"))
	if err != nil {
		return err
	}

	work := host.WorkKey{Connection: string(data), Stream: ibkrflex.Confirmations, Operation: "acquire"}
	if err = store.Schedule(ctx, work, true); err != nil {
		return err
	}

	if err = store.Retry(ctx, work); err != nil {
		return err
	}

	for range 2 {
		now = now.Add(15 * time.Second)
		if err = flow.RunOnce(ctx, now); err != nil {
			return err
		}
	}

	if err = store.Schedule(ctx, work, false); err != nil {
		return err
	}

	return settleReferences(ctx, store, now)
}
