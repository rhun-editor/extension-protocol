# rhun extension protocol

How a program becomes a [rhun](https://rhun.app) extension: rhun starts it and talks to it over
its standard input and output, one text line per message. This repository holds the protocol's
versions, a Go package that speaks it, and the conformance tools.

| Path | What |
| --- | --- |
| `v1/v1.md` | the specification, written to be read by people and by coding agents |
| `v1/v1.abnf` | the grammar of every line |
| `v1/v1.json` | the manifest (`extension.ini`) as a JSON Schema, with every key, pattern and default |
| `rust/rhun-ext` | the Rust crate (the first choice): framing, requests, events, views with pictures and charts, completion and signature help |
| `go/rhunext` | a Go package: framing, ids, bodies, requests that block until their reply, section 12 features |
| `c/rhunext.h` | one header for C and C++: the same, with nothing but the C library |
| `conformance/ext` | `rhun-ext-check DIR`: plays rhun against an extension and reports what it gets wrong |
| `conformance/host` | scripted extensions and headless scripts that check rhun itself against the spec |

Status: **v1 is frozen** (2026-10-16, rhun 0.18.0). Later additions are features announced after
`hello 1` (section 12: views, completion, signature help); nothing in v1 changes.

## Writing an extension

Start from [template-rust](https://github.com/rhun-editor/template-rust) (or
[template-go](https://github.com/rhun-editor/template-go)): a working extension with a headless
test and a release workflow that builds Linux, macOS and Windows binaries. In C or C++, copy
`c/rhunext.h`; [wordfreq](https://github.com/rhun-editor/wordfreq) and
[words](https://github.com/rhun-editor/words) are examples. Coding agents: install the `rhun`
plugin from [rhun-editor/skills](https://github.com/rhun-editor/skills).

```toml
rhun-ext = { git = "https://github.com/rhun-editor/extension-protocol", tag = "rust-v1.1.0" }
```

```sh
go get github.com/rhun-editor/extension-protocol@v1.1.0
```

Test with the checker, `rhun-ext-check DIR` (`go run github.com/rhun-editor/extension-protocol/conformance/ext@latest DIR`),
where `DIR` is the extension laid out as installed (`OWNER/NAME/extension.ini` and `bin/`), then
headlessly against rhun as section 10 of the spec describes:
`rhun PROJECT --headless 1000x600 --extensions EXTS --script test.txt`.

## Checking a host

`python3 conformance/host/run.py RHUN_BINARY` runs rhun against extensions written only from
the spec and prints one line per check.
