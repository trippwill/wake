// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

//go:build browser

package admin

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"go.trippwill.dev/wake/internal/testbrowser"
)

func TestBrowserThemePreference(t *testing.T) {
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()

	dir := privateAuthDir(t)
	if err := SetPassword(dir, []byte("synthetic-theme-unlock")); err != nil {
		t.Fatal(err)
	}

	auth, _, err := Open(dir, origin)
	if err != nil {
		t.Fatal(err)
	}

	server.Config.Handler, err = NewHandler(auth, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	server.Start()
	defer server.Close()

	ctx, deadline := context.WithTimeout(testbrowser.Start(t), 30*time.Second)
	defer deadline()

	var gzipAssets atomic.Int64
	if err := chromedp.Listen(ctx, func(event any) {
		if response, ok := event.(*network.EventResponseReceived); ok &&
			strings.Contains(response.Response.URL, "/assets/") &&
			response.Response.Headers["Content-Encoding"] == "gzip" {
			gzipAssets.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}

	session := httptest.NewRecorder()
	issueTestSession(t, auth, session)
	cookie := session.Result().Cookies()[0]
	check := func(actions ...chromedp.Action[chromedp.Void]) {
		t.Helper()

		if err := chromedp.Do(ctx, actions...); err != nil {
			t.Fatal(err)
		}
	}
	check(
		testbrowser.Command(network.Enable, network.EnableParams{}),
		testbrowser.SetViewport(1280, 900),
		appearanceDocumentScript(`
				const beforeStyles = new MutationObserver(() => {
					if (!document.querySelector('link[rel="stylesheet"]')) return;
					const {palette,theme,typography} = document.documentElement.dataset;
					window.appearanceBeforeStyles = [palette,theme,typography,document.querySelector('meta[name="theme-color"]').content].join(' ');
					beforeStyles.disconnect();
				});
				beforeStyles.observe(document, {childList:true,subtree:true});
		`),
		testbrowser.Command(
			network.SetCookie,
			network.SetCookieParams{Name: cookie.Name, Value: cookie.Value, URL: origin, HTTPOnly: new(true)},
		),
		testbrowser.NewDocument(chromedp.Navigate(origin+"/system")),
		chromedp.Poll[chromedp.Void](`!document.querySelector('.wake-toolbar,[data-theme-preference]')`),
		testbrowser.NewDocument(
			chromedp.Click(chromedp.CSS(`nav[aria-label="System views"] a[href="/system/settings"]`)),
		),
		chromedp.WaitVisible(chromedp.CSS("[data-theme-preference]")),
		chromedp.Poll[chromedp.Void](
			`document.querySelector('nav[aria-label="System views"] [aria-current="page"]')?.textContent==='Settings'`,
		),
	)

	if gzipAssets.Load() == 0 {
		t.Fatal("browser did not receive any compressed assets")
	}

	for _, preference := range []string{"light", "dark"} {
		ready := `document.documentElement.dataset.theme===` + strconv.Quote("wake-"+preference) +
			` && document.querySelector('[data-theme-preference]').value===` + strconv.Quote(preference) +
			` && localStorage.getItem('wake.theme')===` + strconv.Quote(preference)
		check(selectAppearance("theme", preference))
		check(
			chromedp.Poll[chromedp.Void](ready),
			testbrowser.NewDocument(chromedp.Reload()),
			chromedp.WaitVisible(chromedp.CSS("h1")),
			chromedp.Poll[chromedp.Void](ready),
		)
	}

	check(
		chromedp.NavigateBack(),
		chromedp.Poll[chromedp.Void](
			`location.pathname==='/system' && document.documentElement.dataset.theme==='wake-dark'`,
		),
		chromedp.NavigateForward(),
		chromedp.Poll[chromedp.Void](
			`location.pathname==='/system/settings' && document.querySelector('[data-theme-preference]')?.value==='dark'`,
		),
		testbrowser.NewDocument(chromedp.Navigate(origin+"/login")),
		chromedp.WaitVisible(chromedp.CSS("#password")),
		chromedp.Poll[chromedp.Void](
			`document.documentElement.dataset.theme==='wake-dark' && !document.querySelector('.wake-toolbar,[data-theme-preference],#daemon-contact')`,
		),
		chromedp.NavigateBack(),
		chromedp.WaitVisible(chromedp.CSS("[data-theme-preference]")),
	)
	check(selectAppearance("theme", "system"))

	verifyAppearanceCombinations(t, check)

	check(
		selectAppearance("palette", "reader"),
		selectAppearance("typography", "sans"),
		selectAppearance("theme", "dark"),
		testbrowser.NewDocument(chromedp.Reload()),
	)
	assertAppearanceBeforeStyles(t, check, "reader wake-dark sans #0f211c")
	check(chromedp.Poll[chromedp.Void](`
		document.querySelector('[data-palette-preference]').value==='reader' &&
		document.querySelector('[data-theme-preference]').value==='dark' &&
		document.querySelector('[data-typography-preference]').value==='sans'
	`))
	check(testbrowser.NewDocument(chromedp.Navigate(origin + "/login")))
	assertAppearanceBeforeStyles(t, check, "reader wake-dark sans #0f211c")
	check(chromedp.Poll[chromedp.Void](`!document.querySelector('[data-appearance-controls]')`))
	check(chromedp.NavigateBack())
	check(chromedp.WaitVisible(chromedp.CSS("[data-appearance-controls]")))
	verifyAppearanceStorage(t, ctx, origin, check)
	check(selectAppearance("theme", "system"))

	check(
		chromedp.Poll[chromedp.Void](`localStorage.getItem('wake.theme')==='system'`),
		testbrowser.EmulateSystemTheme("dark"),
		testbrowser.EmulateSystemTheme("light"),
		// Headless browser history may reload instead of restoring a cached page.
		chromedp.Evaluate[chromedp.Void](
			`localStorage.setItem('wake.palette','steel'); localStorage.removeItem('wake.typography'); localStorage.setItem('wake.theme','dark'); window.dispatchEvent(new PageTransitionEvent('pageshow',{persisted:true}))`,
		),
		chromedp.Poll[chromedp.Void](
			`
				document.documentElement.dataset.theme==='wake-dark' &&
				document.querySelector('[data-theme-preference]')?.value==='dark' &&
				document.querySelector('[data-palette-preference]')?.value==='steel' &&
				document.querySelector('[data-typography-preference]')?.value==='default'
			`,
		),
	)

	verifySettingsReflow(t, check)
	verifyAppearanceWithoutStorage(t, check)
}

func verifySettingsReflow(t *testing.T, check func(...chromedp.Action[chromedp.Void])) {
	t.Helper()

	check(
		chromedp.EmulateViewport(320, 800),
		chromedp.Evaluate[chromedp.Void](`document.documentElement.style.fontSize='200%'`),
		chromedp.WaitVisible(chromedp.CSS(`.wake-menu-toggle[aria-expanded="false"]`)),
	)

	for _, menuOpen := range []bool{false, true} {
		if menuOpen {
			check(
				chromedp.Click(chromedp.CSS(".wake-menu-toggle")),
				chromedp.WaitVisible(chromedp.CSS("#main-navigation")),
			)
		}

		var reflow struct {
			Viewport, Content int
			ClippedLabels     []string
		}
		check(testbrowser.Evaluate(`(()=>{
			const range = document.createRange();
			const labels = [...document.querySelectorAll('nav[aria-label="System views"] a,.wake-appearance h2,#main-navigation:not([hidden]) a')];
			const clippedLabels = labels.filter(label => {
				range.selectNodeContents(label);
				const bounds = label.getBoundingClientRect();
				const lines = [...range.getClientRects()];
				return !lines.length || lines.some(line => line.left < bounds.left - 1 || line.right > bounds.right + 1);
			}).map(label => label.textContent.trim());
			return {viewport: innerWidth, content: document.documentElement.scrollWidth, clippedLabels};
		})()`, &reflow))

		if reflow.Content > reflow.Viewport+1 || len(reflow.ClippedLabels) != 0 {
			t.Fatalf(
				"Settings does not reflow at 200 percent text (menu open: %t): content %dpx in %dpx viewport, clipped labels %v",
				menuOpen,
				reflow.Content,
				reflow.Viewport,
				reflow.ClippedLabels,
			)
		}
	}
}

func selectAppearance(preference, value string) chromedp.Action[chromedp.Void] {
	selector := "[data-" + preference + "-preference]"

	return chromedp.Steps(
		chromedp.SetValue(chromedp.CSS(selector), value),
		chromedp.Evaluate[chromedp.Void](
			fmt.Sprintf(`document.querySelector(%q).dispatchEvent(new Event('change',{bubbles:true}))`, selector),
		),
	)
}

func verifyAppearanceCombinations(t *testing.T, check func(...chromedp.Action[chromedp.Void])) {
	t.Helper()

	check(
		chromedp.Poll[chromedp.Void](`
			[...document.querySelectorAll('[data-appearance-controls] select')]
				.map(control => control.labels[0].firstElementChild.textContent).join(',') === 'Theme,Mode,Typography'
		`),
		chromedp.Focus(chromedp.CSS("[data-palette-preference]")),
		chromedp.KeyEvent(kb.Tab),
		chromedp.Poll[chromedp.Void](`document.activeElement.matches('[data-theme-preference]:focus-visible')`),
		chromedp.KeyEvent(kb.Tab),
		chromedp.Poll[chromedp.Void](
			`document.activeElement.matches('[data-typography-preference]:focus-visible')`,
		),
	)

	for _, palette := range []struct {
		name, defaultType       string
		lightCanvas, darkCanvas string
		lightChrome, darkChrome string
	}{
		{"steel", "sans", "rgb(237, 240, 242)", "rgb(43, 48, 54)", "#293039", "#2b3036"},
		{"reader", "serif", "rgb(248, 248, 239)", "rgb(21, 36, 30)", "#18342d", "#0f211c"},
	} {
		for _, mode := range []struct{ name, canvas, chrome string }{
			{"light", palette.lightCanvas, palette.lightChrome},
			{"dark", palette.darkCanvas, palette.darkChrome},
		} {
			for _, typography := range []string{"default", "sans", "serif"} {
				resolved := typography
				if resolved == "default" {
					resolved = palette.defaultType
				}

				check(
					selectAppearance("palette", palette.name),
					selectAppearance("theme", mode.name),
					selectAppearance("typography", typography),
				)

				var rendered struct {
					Canvas, Masthead, Chrome, Scheme, Heading, Controls string
					Contrast                                            map[string]float64
				}

				check(testbrowser.Evaluate(`(() => {
					const style = (selector) => getComputedStyle(document.querySelector(selector));
					const luminance = (color) => color.match(/[\d.]+/g).slice(0,3)
						.map(n => n/255).map(x => x <= 0.04045 ? x/12.92 : ((x+0.055)/1.055)**2.4)
						.reduce((sum,n,i) => sum + n*[0.2126,0.7152,0.0722][i],0);
					const contrast = (fg,bg) => {
						const a = luminance(fg), b = luminance(bg);
						return (Math.max(a,b)+0.05)/(Math.min(a,b)+0.05);
					};
					const canvas=style('.wake-app').backgroundColor, masthead=style('.wake-masthead');
					const note=style('.wake-appearance').backgroundColor;
					const heading=style('h1'), controls=style('[data-palette-preference]');
					return {
						canvas,
						masthead:'#'+masthead.backgroundColor.match(/\d+/g)
							.map(n => Number(n).toString(16).padStart(2,'0')).join(''),
						chrome:document.querySelector('meta[name="theme-color"]').content,
						scheme:style('html').colorScheme,
						heading:heading.fontFamily,
						controls:controls.fontFamily,
						contrast:{
							heading:contrast(heading.color,canvas),
							help:contrast(style('#appearance-mode-help').color,note),
							control:contrast(controls.color,controls.backgroundColor),
							controlEdge:Math.min(contrast(controls.borderTopColor,note),contrast(controls.borderTopColor,controls.backgroundColor)),
							masthead:contrast(masthead.color,masthead.backgroundColor),
							mastheadSelected:contrast(style('.wake-nav [aria-current=page]').color,masthead.backgroundColor),
						},
					};
				})()`, &rendered))

				if rendered.Canvas != mode.canvas || rendered.Chrome != mode.chrome ||
					rendered.Masthead != mode.chrome ||
					rendered.Scheme != mode.name ||
					strings.Contains(rendered.Heading, "STIX Two Text") != (resolved == "serif") ||
					strings.Contains(rendered.Heading, "system-ui") != (resolved == "sans") ||
					!strings.Contains(rendered.Controls, "system-ui") {
					t.Fatalf("%s/%s/%s rendered %+v", palette.name, mode.name, typography, rendered)
				}

				for label, ratio := range rendered.Contrast {
					minimum := 4.5
					if label == "controlEdge" {
						minimum = 3
					}

					if ratio < minimum {
						t.Errorf(
							"%s/%s/%s %s contrast %.2f:1, want %.1f:1",
							palette.name,
							mode.name,
							typography,
							label,
							ratio,
							minimum,
						)
					}
				}

				t.Logf(
					"%s/%s/%s uses %s on %s, contrast %v",
					palette.name,
					mode.name,
					typography,
					rendered.Heading,
					rendered.Canvas,
					rendered.Contrast,
				)
			}
		}
	}

	check(chromedp.Poll[chromedp.Void](`
		[...document.fonts].some(face => face.family.includes('STIX Two Text') && face.status==='loaded')
	`))
}

func verifyAppearanceStorage(
	t *testing.T,
	ctx context.Context,
	origin string,
	check func(...chromedp.Action[chromedp.Void]),
) {
	t.Helper()

	otherTab, closeTab := chromedp.NewContext(ctx)
	defer closeTab()

	updateStorage := func(script string) {
		t.Helper()

		if err := chromedp.Do(otherTab, chromedp.Evaluate[chromedp.Void](script)); err != nil {
			t.Fatal(err)
		}
	}

	if err := chromedp.Do(otherTab, chromedp.Navigate(origin+"/login")); err != nil {
		t.Fatal(err)
	}

	updateStorage(`localStorage.setItem('wake.palette','steel')`)
	check(chromedp.Poll[chromedp.Void](`
		document.documentElement.dataset.palette==='steel' &&
		document.querySelector('[data-palette-preference]').value==='steel' &&
		document.querySelector('[data-typography-preference]').value==='sans' &&
		document.querySelector('[data-theme-preference]').value==='dark'
	`))

	updateStorage(
		`localStorage.setItem('wake.palette','unknown'); localStorage.setItem('wake.typography','unknown'); localStorage.setItem('wake.theme','unknown')`,
	)
	check(chromedp.Poll[chromedp.Void](`
		document.querySelector('[data-palette-preference]').value==='steel' &&
		document.querySelector('[data-typography-preference]').value==='default' &&
		document.querySelector('[data-theme-preference]').value==='system'
	`))

	updateStorage(
		`localStorage.setItem('wake.palette','reader'); localStorage.setItem('wake.typography','serif'); localStorage.setItem('wake.theme','dark')`,
	)
	check(chromedp.Poll[chromedp.Void](`
		document.documentElement.dataset.palette==='reader' &&
		document.documentElement.dataset.typography==='serif' &&
		document.documentElement.dataset.theme==='wake-dark'
	`))

	updateStorage(`localStorage.clear()`)
	check(chromedp.Poll[chromedp.Void](`
		document.querySelector('[data-palette-preference]').value==='steel' &&
		document.querySelector('[data-typography-preference]').value==='default' &&
		document.querySelector('[data-theme-preference]').value==='system'
	`))
}

func verifyAppearanceWithoutStorage(t *testing.T, check func(...chromedp.Action[chromedp.Void])) {
	t.Helper()
	check(
		appearanceDocumentScript(`
				Object.defineProperty(window,'localStorage',{
					get() { throw new DOMException('Storage unavailable','SecurityError'); }
				});
		`),
		testbrowser.NewDocument(chromedp.Reload()),
	)
	assertAppearanceBeforeStyles(t, check, "steel wake-light default #293039")
	check(chromedp.WaitVisible(chromedp.CSS("[data-appearance-controls]")))
	check(chromedp.Poll[chromedp.Void](`document.querySelector('[data-theme-preference]').value==='system'`))
	check(
		selectAppearance("palette", "reader"),
		selectAppearance("theme", "dark"),
		selectAppearance("typography", "sans"),
		chromedp.Poll[chromedp.Void](`
			document.documentElement.dataset.theme==='wake-dark' &&
			document.documentElement.dataset.palette==='reader' &&
			document.documentElement.dataset.typography==='sans' &&
			document.querySelector('meta[name="theme-color"]').content==='#0f211c'
		`),
	)
}

func appearanceDocumentScript(source string) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
		_, err := chromedp.Call(
			ctx,
			cdppage.AddScriptToEvaluateOnNewDocument,
			cdppage.AddScriptToEvaluateOnNewDocumentParams{
				Source: source,
			},
		)

		return err
	})
}

func assertAppearanceBeforeStyles(t *testing.T, check func(...chromedp.Action[chromedp.Void]), want string) {
	t.Helper()

	var initial string
	check(testbrowser.Evaluate(`window.appearanceBeforeStyles ?? ""`, &initial))

	if initial != want {
		t.Fatalf("appearance before stylesheets: got %q, want %q", initial, want)
	}
}
