package rhunext

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"testing"
)

func snippetBase64NativeVectors() []byte {
	inputs := [][]byte{}
	for _, s := range []string{"", "Zg==", "Zm8=", "Zm9v", "Zm9vYg==", "Zm9vYmE=", "Zm9vYmFy", "A", "AA", "AAA", "A===", "====", "AA=A", "AA==AAAA", "AB==", "AAB=", "AA--", "AA__", "AA\r\n==", "AA\n=", "AA =", "AA\x00="} {
		inputs = append(inputs, []byte(s))
	}
	var all []byte
	for i := 0; i < 256; i++ {
		all = append(all, byte(i))
		inputs = append(inputs, []byte(base64.StdEncoding.EncodeToString([]byte{byte(i)})))
	}
	full := []byte(base64.StdEncoding.EncodeToString(all))
	inputs = append(inputs, full)
	for n := 0; n < len(full); n++ {
		inputs = append(inputs, bytes.Clone(full[:n]))
	}
	for i := range full {
		bad := bytes.Clone(full)
		bad[i] ^= 255
		inputs = append(inputs, bad)
	}
	for at := 0; at < 4; at++ {
		for value := 0; value < 256; value++ {
			b := []byte("Zm8=")
			b[at] = byte(value)
			inputs = append(inputs, b)
		}
	}
	maximum := base64.StdEncoding.EncodeToString(make([]byte, MaxSnippetBody))
	inputs = append(inputs, []byte(maximum), []byte(maximum[:len(maximum)-2]+"A="), []byte(maximum[:len(maximum)-3]+"B=="))
	var rows, data bytes.Buffer
	for i, input := range inputs {
		output, e := base64.StdEncoding.Strict().DecodeString(string(input))
		valid := 1
		if e != nil || len(input) > 5592408 || len(input)%4 != 0 || bytes.ContainsAny(input, "\r\n") || len(output) > MaxSnippetBody {
			valid = 0
			output = nil
		}
		fmt.Fprintf(&rows, ".quad .Lsb64_input_%d,%d,.Lsb64_output_%d,%d,%d\n", i, len(input), i, len(output), valid)
		fmt.Fprintf(&data, ".Lsb64_input_%d:\n", i)
		if len(input) > 1<<20 {
			n := 0
			for n < len(input) && input[n] == 'A' {
				n++
			}
			fmt.Fprintf(&data, ".zero %d,65\n", n)
			snippetRegexEmitBytes(&data, input[n:])
		} else {
			snippetRegexEmitBytes(&data, input)
		}
		fmt.Fprintf(&data, ".Lsb64_output_%d:\n", i)
		snippetRegexEmitBytes(&data, output)
	}
	var out bytes.Buffer
	out.WriteString(".p2align 3\n.Lsb64_vectors:\n")
	out.Write(rows.Bytes())
	out.WriteString(".quad 0\n")
	out.Write(data.Bytes())
	return out.Bytes()
}
func TestSnippetBase64NativeVectorExport(t *testing.T) {
	first, second := snippetBase64NativeVectors(), snippetBase64NativeVectors()
	if !bytes.Equal(first, second) {
		t.Fatal("nondeterministic export")
	}
	if path := os.Getenv("RHUN_BASE64_ASM_OUT"); path != "" {
		if e := snippetRegexWriteExport(path, first); e != nil {
			t.Fatal(e)
		}
	}
}
