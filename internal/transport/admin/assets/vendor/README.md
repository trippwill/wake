# Vendored browser assets

Wake embeds pinned upstream distributions without a browser build step.

| Project | Version | License |
| --- | --- | --- |
| daisyUI | 5.7.50 | MIT |
| Tabulator | 6.7.1 | MIT |
| htmx | 2.0.11 | 0BSD |
| htmx SSE extension | 2.2.4 | 0BSD |

daisyUI and Tabulator come from their published npm distributions. Application styling and
initialization live outside vendor files.

The htmx and SSE bundles also come from their published upstream distributions. Wake serves
all browser assets locally at runtime.

Verify the upstream files with `sha256sum --check` using this manifest.

```text
6a2cf91a0e9a7da09cf8109c45c3fae171e8967122576043f2b94da47fdf6b64  daisyui-5.7.50/daisyui.css
8709e3ac65c84637c422dc8082b893d9997fce093751c77b2f1983bf29dbf9ea  daisyui-5.7.50/LICENSE
4e218e8d309e9b704089e7808f5484f5257ea645ba4e4808c0ae13969500d55c  tabulator-6.7.1/tabulator.min.js
ff598d8e961398e09a8e52ab21740e5ed952c84feebbdacec8f8a362dd3f73db  tabulator-6.7.1/tabulator.min.css
191a2ee554684e1064c897b432f0e1bc6dfa714ca045d3f6ea2cf692cbd398b7  tabulator-6.7.1/LICENSE
d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717  htmx-2.0.11/htmx.min.js
d3d2456f76414f2456104660ebd65aff1c04cd7966b942bdabd63f3cdb316a38  htmx-2.0.11/LICENSE
98a46496de0c3605fbffdce9167ba427bdd9553184f83f149c261891a92c0136  htmx-sse-2.2.4/sse.min.js
a17d224ce9afdc21ec9491b76a2f5d036d19aa641ac4f85ca29c21f9f7e54fdf  htmx-sse-2.2.4/LICENSE
```
