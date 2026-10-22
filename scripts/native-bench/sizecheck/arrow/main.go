// Command arrow opens a Parquet file with arrow-go's reader and prints its
// row count, so its stripped size is what that reader costs a binary.
package main

import (
	"fmt"
	"os"

	"github.com/apache/arrow-go/v18/parquet/file"
)

func main() {
	rdr, err := file.OpenParquetFile(os.Args[1], false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer rdr.Close()
	fmt.Println(rdr.NumRows())
}
