// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
	"go.trippwill.dev/wake/internal/application"
	"go.trippwill.dev/wake/internal/testbrowser"
	"go.trippwill.dev/wake/source"
)

func TestBrowserSynchronizationSummary(t *testing.T) {
	browser := os.Getenv("WAKE_BROWSER")
	if browser == "" {
		for _, name := range []string{"brave-browser", "chromium", "google-chrome"} {
			if path, err := exec.LookPath(name); err == nil {
				browser = path
				break
			}
		}
	}

	if browser == "" {
		t.Fatal("Chromium required; set WAKE_BROWSER")
	}

	ref, _ := source.NewReference("synthetic", "account", "", "one")
	details, _ := application.NewAccountDetails("Synthetic account", "USD")

	account, err := application.NewAccount("one", "source", "synthetic", ref, details, "fixture")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	last, next := now.Add(-time.Hour), now.Add(time.Hour)
	newView := func(work ...application.WorkStatus) overviewView {
		return fixtureOverview(fixtureView{Account: account, Connections: []application.ConnectionView{{
			ID: "source", Name: "ibkr-flex", CanAcquire: true, Work: work,
		}}})
	}
	activity := application.WorkStatus{
		Stream: "activity", Operation: "acquire", Progress: "retained",
		Scheduled: true, LastRetrieved: last, Next: next,
	}
	confirmations := application.WorkStatus{
		Stream: "confirmations", Operation: "acquire", Progress: "retained",
		Scheduled: true, LastRetrieved: last, Next: next,
	}
	blocked, retry := activity, confirmations
	blocked.Failure, blocked.RetryBlocked = "Source input unavailable", true
	retry.Failure = "Upstream retry"
	first := application.WorkStatus{Stream: "initial", Operation: "acquire", Scheduled: true}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /assets/", serveAsset)

	serve := func(w http.ResponseWriter, r *http.Request, view overviewView) {
		templ.Handler(SectionShell("Overview", "synthetic-csrf", "Overview", DashboardOverview(view))).
			ServeHTTP(w, pageRequest(r))
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		current := activity
		if r.Header.Get("HX-Request") == "true" {
			current.LastRetrieved = last.Add(time.Minute)
		}

		serve(w, r, newView(current, confirmations))
	})
	mux.HandleFunc("GET /mixed", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, newView(blocked, retry, first))
	})
	mux.HandleFunc("GET /paused", func(w http.ResponseWriter, r *http.Request) {
		paused := newView(activity)
		paused.Storage.Paused = true
		serve(w, r, paused)
	})
	mux.HandleFunc("GET /access", func(w http.ResponseWriter, r *http.Request) {
		access := newView(activity)
		access.Connections[0].MissingAccess = true
		serve(w, r, access)
	})
	mux.HandleFunc("GET /idle", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, newView())
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	allocator, stop := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(browser))...)
	defer stop()

	ctx, cancel := chromedp.NewContext(allocator)
	defer cancel()

	ctx, deadline := context.WithTimeout(ctx, 45*time.Second)
	defer deadline()

	if err = chromedp.Run(ctx, testbrowser.NewDocument(chromedp.Navigate(server.URL)),
		chromedp.WaitVisible("#synchronization-title", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}

	capture := func(name string) {
		dir := os.Getenv("WAKE_SCREENSHOT_DIR")
		if dir == "" {
			return
		}

		var png []byte
		if err = chromedp.Run(
			ctx,
			chromedp.Screenshot(`section[aria-labelledby="synchronization-title"]`, &png, chromedp.ByQuery),
		); err != nil {
			t.Fatal(err)
		}
		//nolint:gosec // Caller-selected synthetic screenshot directory and fixed filenames.
		if err = os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		//nolint:gosec // Caller-selected synthetic screenshot directory and fixed filenames.
		if err = os.WriteFile(filepath.Join(dir, name+".png"), png, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, theme := range []string{"light", "dark"} {
		for _, width := range []int64{1280, 768, 390, 320} {
			var valid bool
			if err = chromedp.Run(ctx,
				emulation.SetDeviceMetricsOverride(width, 900, 1, false),
				chromedp.Evaluate(`document.documentElement.dataset.theme=`+strconv.Quote("wake-"+theme), nil),
				chromedp.Evaluate(`(()=>{
					const rows=Array.from(document.querySelectorAll('.wake-sync-work > div'));
					return document.documentElement.scrollWidth<=innerWidth+1&&rows.length===2&&
						rows.map(row=>row.querySelector('dt').textContent.trim()).join(',')==='Activity,Confirmations'&&
						rows.every((row,index)=>{
							const label=row.querySelector('dt').getBoundingClientRect(),status=row.querySelector('dd').getBoundingClientRect();
							const values=row.querySelectorAll('time');
							return row.querySelector('dd').textContent.includes('Evidence retained')&&
								row.querySelector('dd').textContent.includes('Last successful retrieval')&&
								row.querySelector('dd').textContent.includes('Next event')&&
								row.querySelector('dd').textContent.includes('Attempt')&&
								values.length===2&&values[0].textContent.endsWith('UTC')&&
								values[1].dateTime===`+strconv.Quote(next.Format(time.RFC3339Nano))+`&&label.right<=status.left&&
								(index===0||rows[index-1].getBoundingClientRect().bottom<=row.getBoundingClientRect().top);
						})&&document.querySelector('.wake-source-summary a').getAttribute('href')==='/connections/source?view=status';
				})()`, &valid),
			); err != nil || !valid {
				t.Fatalf("%s at %dpx: stream rows overlap, overflow or lose their meaning: %v", theme, width, err)
			}

			if width == 1280 || width == 390 {
				capture("synchronization-" + theme + "-" + strconv.FormatInt(width, 10))
			}
		}
	}

	if err = chromedp.Run(
		ctx,
		chromedp.Evaluate(
			`window.htmx.trigger(document.querySelector('#workspace-content'), 'sse:refresh')`,
			nil,
		),
		chromedp.Poll(`document.querySelector('.wake-sync-work time').dateTime===`+
			strconv.Quote(last.Add(time.Minute).Format(time.RFC3339Nano)), nil),
	); err != nil {
		t.Fatal("live refresh did not replace the retrieval time", err)
	}

	if err = chromedp.Run(
		ctx,
		testbrowser.NewDocument(chromedp.Navigate(server.URL+"/mixed")),
		chromedp.WaitVisible("#synchronization-title", chromedp.ByQuery),
	); err != nil {
		t.Fatal(err)
	}

	for _, theme := range []string{"light", "dark"} {
		for _, width := range []int64{1280, 768, 390, 320} {
			var valid bool

			if err = chromedp.Run(ctx, emulation.SetDeviceMetricsOverride(width, 900, 1, false),
				chromedp.Evaluate(`document.documentElement.dataset.theme=`+strconv.Quote("wake-"+theme), nil),
				chromedp.Evaluate(`(()=>{
     const rows=Array.from(document.querySelectorAll('.wake-sync-work > div'));
     return document.documentElement.scrollWidth<=innerWidth+1&&rows.length===3&&
      rows[0].querySelector('dt').textContent.trim()==='Activity'&&rows[0].querySelector('dd').textContent.includes('failed')&&
      rows[0].querySelector('dd').textContent.includes('Source input unavailable')&&
      rows[0].textContent.includes('Operator action required')&&rows[0].querySelectorAll('time').length===1&&
      rows[1].querySelector('dt').textContent.trim()==='Confirmations'&&rows[1].textContent.includes('Retry ·')&&
      rows[1].querySelectorAll('time').length===2&&rows[2].querySelectorAll('time').length===0&&
      rows[2].textContent.includes('No successful retrieval yet')&&rows[2].textContent.includes('Eligible now');
    })()`, &valid)); err != nil || !valid {
				t.Fatalf("%s at %dpx: a failed stream hid healthy progress: %v", theme, width, err)
			}

			if width == 1280 || width == 390 {
				capture("synchronization-mixed-" + theme + "-" + strconv.FormatInt(width, 10))
			}
		}
	}

	for _, tc := range []struct {
		path, state string
		times       int
	}{
		{"/paused", "Paused", 1},
		{"/access", "Access review required", 1},
		{"/idle", "No automatic work scheduled", 0},
	} {
		if err = chromedp.Run(ctx,
			testbrowser.NewDocument(chromedp.Navigate(server.URL+tc.path)),
			chromedp.Poll(`(()=>{
				const card=document.querySelector('section[aria-labelledby="synchronization-title"]');
				const last=card.querySelectorAll('time');
				return card.textContent.includes(`+strconv.Quote(tc.state)+`) &&
					last.length===`+strconv.Itoa(tc.times)+` &&
					(last.length===0 || last[0].dateTime===`+strconv.Quote(last.Format(time.RFC3339Nano))+`);
			})()`, nil),
		); err != nil {
			t.Fatal(tc.path, err)
		}
	}

	if err = chromedp.Run(ctx, chromedp.Evaluate(`(()=>{
		document.body.style.minHeight='200vh';
		const make=options=>{
			const node=document.createElement('div');document.body.append(node);
			return node.animate([{opacity:0},{opacity:1}],options);
		};
		window.pausedCapture=make({duration:1000,fill:'forwards'});window.pausedCapture.pause();
		window.progressCapture=make({duration:1000,iterations:Infinity});
		window.scrollCapture=make({timeline:new ScrollTimeline({source:document.scrollingElement}),fill:'both'});
		window.finiteCapture=make({duration:500,fill:'forwards'});
  Object.defineProperty(window.finiteCapture,'finished',{get:()=>new Promise(()=>{})});
  window.finalCapturePaint=false;
  const frame=requestAnimationFrame.bind(window);
  window.requestAnimationFrame=callback=>frame(time=>{
   if(window.finiteCapture.playState==='finished')window.finalCapturePaint=true;
   callback(time);
  });
	})()`, nil)); err != nil {
		t.Fatal(err)
	}

	settle, stopSettle := context.WithTimeout(ctx, 2*time.Second)
	defer stopSettle()

	if err = chromedp.Run(settle, testbrowser.SettleAnimations()); err != nil {
		t.Fatal("capture must not wait for paused, progress or scroll-driven animations", err)
	}

	var settled bool
	if err = chromedp.Run(
		ctx,
		chromedp.Evaluate(`window.finalCapturePaint&&window.finiteCapture.playState==='finished'&&
		window.pausedCapture.playState==='paused'&&window.progressCapture.playState==='running'&&
		window.scrollCapture.playState!=='finished'`, &settled),
	); err != nil ||
		!settled {
		t.Fatal("capture did not settle finite transitions while preserving other animations", settled, err)
	}
}
