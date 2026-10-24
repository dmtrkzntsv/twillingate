// Command pqgo opens a Parquet file with parquet-go's reader and prints its
// row count, so its stripped size is what that reader costs a binary.
package main

import (
	"fmt"
	"os"

	"github.com/parquet-go/parquet-go"
)

func main() {
	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pf, err := parquet.OpenFile(f, fi.Size())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(pf.NumRows())
}
