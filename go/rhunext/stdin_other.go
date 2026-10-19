//go:build !wasip1

package rhunext

import "os"

// stdin is rhun's end of the pipe (stdin_wasip1.go says why wasip1 differs).
func stdin() *os.File { return os.Stdin }
