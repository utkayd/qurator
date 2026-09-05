// Command qrdecode decodes a QR image with the independent gozxing decoder and writes
// the recovered payload bytes to stdout, so quickstart scenarios can pipe output through
// `cmp` against the original input.
//
//	qrdecode [-svg] [-size 512] [-ec] FILE
//
// The format is inferred from the file extension (.svg → vector, anything else → PNG)
// unless -svg is given. -ec prints the symbol's error-correction level to stderr.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/utkayd/qurator/tools/qrdecode/decode"
)

func main() {
	svg := flag.Bool("svg", false, "treat the input as SVG (default: by .svg extension)")
	size := flag.Int("size", 0, "rasterisation side in pixels for SVG input (0 = document size)")
	ec := flag.Bool("ec", false, "print the error-correction level to stderr")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: qrdecode [-svg] [-size N] [-ec] FILE")
		os.Exit(2)
	}
	path := flag.Arg(0)
	data, err := os.ReadFile(path) //nolint:gosec // path is the CLI's own FILE argument, its sole purpose is to read the file the operator names
	if err != nil {
		fmt.Fprintln(os.Stderr, "qrdecode:", err)
		os.Exit(1)
	}
	var res *decode.Result
	if *svg || strings.HasSuffix(strings.ToLower(path), ".svg") {
		res, err = decode.SVG(data, *size)
	} else {
		res, err = decode.PNG(data)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "qrdecode:", err)
		os.Exit(1)
	}
	if *ec {
		fmt.Fprintln(os.Stderr, "ec_level:", res.ECLevel)
	}
	if _, err := os.Stdout.Write(res.Bytes); err != nil {
		fmt.Fprintln(os.Stderr, "qrdecode:", err)
		os.Exit(1)
	}
}
