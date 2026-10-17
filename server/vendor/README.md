# vendored: KaTeX

Embedded into the binary (`go:embed`) and served at `/vendor/katex/…`; the page never
loads anything from a CDN. Only the files below are served (exact match; any other
`/vendor/…` path is 404).

- Package: `katex@0.16.47` from the npm registry (`npm pack katex@0.16`)
- Tarball: `katex-0.16.47.tgz`
  - sha1   `0a13a42c2deb4f74e61f162d440b9165a548030f` (equals `dist.shasum` reported by the registry)
  - sha256 `ad2d1ab2f7fcc227019784320e8a2c5218ff6bb333b36df4136d10365ae29375`
- License: MIT (`katex/LICENSE`, copied unchanged)

| file | source | sha256 (as stored here) |
|------|--------|-------------------------|
| `katex/katex.min.js` | `package/dist/katex.min.js` unchanged | `a29d2961d3146de5949d78ac7c1a9d93ae54955bad22a6db4fbe836e88e8bf48` |
| `katex/katex.min.css` | `package/dist/katex.min.css`, **modified** (see below) | `72884fe9b836c49ba91fce6a8875037fc430a1bc86157426136a20314831fa76` |
| `katex/LICENSE` | `package/LICENSE` unchanged | `766ccc1f306c885aa45542a9846bbd0a505b27a0374f146778171c2254ce18e3` |
| `katex/fonts/*.woff2` (20 files) | `package/dist/fonts/*.woff2` unchanged | listed below |

Unmodified `package/dist/katex.min.css` sha256: `0289a02cf451a44dd73add683a09644252363871ac11713a647b732cee8b1ee3`.

## Font file hashes (sha256)

```
0cdd387c9590a1a9f9794560022dbb59654a7d86f187aa0c81495ad42d3a7308  KaTeX_AMS-Regular.woff2
de7701e42cf1f4cf0b766c03fb27977207eee2f4fd5d76fa82188406da43ea4c  KaTeX_Caligraphic-Bold.woff2
5d53e70ad607c2352162dec9e0923fb54ecdafaccbf604cd8dcf7d00facb989b  KaTeX_Caligraphic-Regular.woff2
74444efd593c005e3f4573b44524704c0af0a937fe911cca9e94068d0d140d3f  KaTeX_Fraktur-Bold.woff2
51814d270d06ff0255dba0799994fa4d8c84d11f09951d47595f4abb1f3602dc  KaTeX_Fraktur-Regular.woff2
0f60d1b897938ec918c8ce073092411baf9438f6739465693ff18b0f9d20b021  KaTeX_Main-Bold.woff2
99cd42a3c072d918f2f44984a807cf7aa16e13545fd0875fc07c6c65f99e715b  KaTeX_Main-BoldItalic.woff2
97479ca6cce906abc961ecac96faa5f9ca2e61b8e7670d475826bcdee9a7c267  KaTeX_Main-Italic.woff2
c2342cd8b869e01752a9321dc17213fc40d4d04c79688c1d43f2cf316abd7866  KaTeX_Main-Regular.woff2
dc47344dbb6cb5b655c8460d561f4df5f501b90c804ad3c6cec65fe322351ab1  KaTeX_Math-BoldItalic.woff2
7af58c5ec8f132a2ddde9027c6d7814decce4d3b822a11192a42a20e2e973264  KaTeX_Math-Italic.woff2
e99ae51144bf1232efcc1bfe5add36262c6866b0faab24fa75740e1b98577a62  KaTeX_SansSerif-Bold.woff2
00b26ac825e2095056396e0553b8ac26d3f8ad158c3826e28b4c45b385c4714a  KaTeX_SansSerif-Italic.woff2
68e8c73ef42afd3ccec58bf0fba302cce448938e7fc020a5e31f8a952eee1342  KaTeX_SansSerif-Regular.woff2
036d4e95149b69ff9bcc0cd55771efeb25ffa3947293e69acd78d5ac328c684b  KaTeX_Script-Regular.woff2
6b47c40166b6dbe21a5dfca7718413f2147fd2399be1ba605d8ad39cedf25dfe  KaTeX_Size1-Regular.woff2
d04c54219f9eaec6d4d4fd42dfb28785975a4794d6b2fc71e566b9cd6db842dd  KaTeX_Size2-Regular.woff2
73d591271b1604960cb10bb90fee021670af7297017e0e98480b332d11f51995  KaTeX_Size3-Regular.woff2
a4af7d414440a1c1790825cfb700cf9cf43b0f2c4b04f0ebc523011ad9853ec0  KaTeX_Size4-Regular.woff2
71d517d67827787cfabdf186914cc3358eda539e37931941f2b2fd4a21f68c0b  KaTeX_Typewriter-Regular.woff2
```

## The one modification

In `katex.min.css` every `@font-face` `src` is reduced to the woff2 file only and
points at the absolute path `/vendor/katex/fonts/<name>.woff2` (the original also listed
`.woff` and `.ttf` fallbacks, which are not shipped). All browsers that can run the
practice page support woff2.

Not shipped: `contrib/auto-render.min.js` (the page renders math itself with
`katex.render`, after its own math tokenizer has split the text), `.woff`/`.ttf` fonts,
other contrib files.

## Upgrading
1. `npm pack katex@<version>`, unpack, copy the same files, redo the css edit
   (`rewrite-css.py`), update the table above.
2. `cd server && make test` (the embed test lists the exact served file set).
