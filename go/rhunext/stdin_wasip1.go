//go:build wasip1

package rhunext

import (
	"os"
	"syscall"
)

// stdin is rhun's end of the pipe. Compiled to wasip1 (rhun runs such extensions itself:
// run.wasm32-wasip1 in the manifest), Go has one thread, and a read blocked on standard input
// would stop every goroutine, the handler waiting for its reply among them. A non-blocking
// descriptor goes through the runtime's poller (WASI poll_oneoff) instead, so the reader
// goroutine parks and the others run.
func stdin() *os.File {
	if err := syscall.SetNonblock(0, true); err != nil {
		return os.Stdin
	}
	return os.NewFile(0, "/dev/stdin")
}
