# rhun extension protocol

How a program becomes a [rhun](https://rhun.app) extension: rhun starts it and talks to it over
its standard input and output, one text line per message. This repository holds the protocol's
versions, a Go package that speaks it, and the conformance tools.

| Path | What |
| --- | --- |
| `v1/v1.md` | the specification, written to be read by people and by coding agents |
| `v1/v1.abnf` | the grammar of every line |
| `go/rhunext` | a Go package: framing, ids, bodies, requests that block until their reply |
| `conformance/ext` | `rhun-ext-check DIR`: plays rhun against an extension and reports what it gets wrong |
| `conformance/host` | scripted extensions and headless scripts that check rhun itself against the spec |

Status: v1 is a draft while rhun 0.18.0 is built; it freezes when the first five extensions
work. Changes until then are listed at the top of `v1/v1.md`.

## Writing an extension

Start from [rhun-editor/extension-template](https://github.com/rhun-editor/extension-template):
a Go program on `go/rhunext`, with the release workflow that builds Linux, macOS and Windows
binaries and attests them. Test with `go run github.com/rhun-editor/extension-protocol/conformance/ext@latest DIR`
where `DIR` holds your `extension.ini` and the built program, then headlessly against rhun:
`rhun PROJECT --headless 1000x600 --extensions EXTS --script test.txt`.

## Checking a host

`python3 conformance/host/run.py RHUN_BINARY` runs rhun against extensions written only from
the spec and prints one line per check.
