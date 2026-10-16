# rhun extension protocol

How a program becomes a [rhun](https://rhun.app) extension: rhun starts it and talks to it over
its standard input and output, one text line per message. This repository holds the protocol's
versions, a Go package that speaks it, and the conformance tools.

| Path | What |
| --- | --- |
| `v1/v1.md` | the specification, written to be read by people and by coding agents |
| `v1/v1.abnf` | the grammar of every line |
| `v1/v1.json` | the manifest (`extension.ini`) as a JSON Schema, with every key, pattern and default |
| `go/rhunext` | a Go package: framing, ids, bodies, requests that block until their reply |
| `conformance/ext` | `rhun-ext-check DIR`: plays rhun against an extension and reports what it gets wrong |
| `conformance/host` | scripted extensions and headless scripts that check rhun itself against the spec |

Status: **v1 is frozen** (2026-10-16, rhun 0.18.0). Later versions add messages announced
after `hello 1`; nothing in v1 changes. The kernel sandbox behind `[needs]` lands with
rhun 0.18.0's confinement work.

## Writing an extension

Start from [rhun-editor/extension-template](https://github.com/rhun-editor/extension-template):
a Go program on `go/rhunext`, with the release workflow that builds Linux, macOS and Windows
binaries and attests them. Test with the checker, `rhun-ext-check DIR` (`go build -o rhun-ext-check ./conformance/ext`
here, or `go run github.com/rhun-editor/extension-protocol/conformance/ext@latest DIR`), where
`DIR` is the extension laid out as installed (`OWNER/NAME/extension.ini` and `bin/`), then
headlessly against rhun as section 10 of the spec describes:
`rhun PROJECT --headless 1000x600 --extensions EXTS --script test.txt`.

## Checking a host

`python3 conformance/host/run.py RHUN_BINARY` runs rhun against extensions written only from
the spec and prints one line per check.
