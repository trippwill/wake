# Vendored browser assets

Wake embeds pinned upstream distributions without a browser build step.

| Project | Version | License |
| --- | --- | --- |
| daisyUI | 5.5.14 | MIT |
| Tabulator | 6.3.1 | MIT |
| htmx | 2.0.11 | 0BSD |
| htmx SSE extension | 2.2.4 | 0BSD |
| idiomorph | 0.7.4 | 0BSD |

Tabulator distributions are vendored from the upstream npm release. Application styling and initialization live outside vendor files.

SHA-256:

- `tabulator-6.3.1/tabulator.min.js`: `e952272c3b2afa4ebb60cef5db8cbe9cbaabaa52b50c3cd3d22993ca5215a6ff`.
- `tabulator-6.3.1/tabulator.min.css`: `a46d8051944c745cae8a7976b4fb9d93d894d20876a4521cc4f6f035cfef52ea`.
- `tabulator-6.3.1/LICENSE`: `6031e51b8450fa6047887f018951c8cf1456d98a5a08291774410c13696de18e`.

htmx 2.0.11 is the upstream published distribution from
`https://unpkg.com/htmx.org@2.0.11/`. The SSE and idiomorph distributions include their upstream licenses. All scripts stay local; there is no CDN
request or browser build step at runtime.

- `htmx-2.0.11/htmx.min.js`: `d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717`.

- `htmx-2.0.11/LICENSE`: `d3d2456f76414f2456104660ebd65aff1c04cd7966b942bdabd63f3cdb316a38`.

- `htmx-sse-2.2.4/sse.min.js`: `98a46496de0c3605fbffdce9167ba427bdd9553184f83f149c261891a92c0136`.

- `htmx-sse-2.2.4/LICENSE`: `a17d224ce9afdc21ec9491b76a2f5d036d19aa641ac4f85ca29c21f9f7e54fdf`.

- `idiomorph-0.7.4/idiomorph-ext.min.js`: `a6437e55b1b6a07bc421f0d230266a39399b6826c6ed19e0ed9c63b707444a5f`.

- `idiomorph-0.7.4/LICENSE`: `d3d2456f76414f2456104660ebd65aff1c04cd7966b942bdabd63f3cdb316a38`.
