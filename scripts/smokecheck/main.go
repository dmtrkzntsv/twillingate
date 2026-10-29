// Command smokecheck prints the number of events of the two raw families in
// an events directory (data/<project>/<day>.log and <day>.N.closed), counted
// from the lines themselves. Used by scripts/smoke.sh to verify ingestion
// end-to-end without requiring a sqlite3 CLI on the host.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: smokecheck <events-dir>")
		os.Exit(2)
	}
	counts := map[string]int{}
	err := filepath.WalkDir(os.Args[1], func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !(strings.HasSuffix(path, ".log") || strings.HasSuffix(path, ".closed")) {
			return err
		}
		return count(path, counts)
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("views=%d events=%d\n", counts["views"], counts["product"])
}

func count(path string, counts map[string]int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var l struct {
			Family string `json:"family"`
		}
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		counts[l.Family]++
	}
	return sc.Err()
}
