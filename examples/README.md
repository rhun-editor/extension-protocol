# Examples

Extensions built on the Go SDK (`go/rhunext`) that show protocol 1.1's views; rhun's own tests
drive them. Build one with `go build -o bin/NAME-macos-arm64 .` in its folder (or -linux-x86_64, -windows-x64.exe), then link the folder into
rhun's extensions (Extensions page, Add, a folder, linked).

- **git-history**: the project's commits in a sidebar tree (`view-tree`): 200 at a time with
  "Load more" (`vmore`), each commit's changed files asked for when it is expanded (`vkids`),
  a click on a file opening it.
- **http**: a request form in the panel (`view-form`); Send checks the URL, shows progress with
  Cancel (`progress`) and opens the response as a Markdown document in a tab (`view-doc`), JSON
  indented and highlighted.
