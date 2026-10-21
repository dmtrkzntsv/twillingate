package app

import (
	"os"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/engine"
)

const appEngineChild = "APP_TEST_ENGINE_CHILD"

// TestMain lets the test binary act as the engine child the tests spawn:
// the production child is `twillingate engine`. The variable is set for
// the whole run, so the engines EngineOptions lays out (Serve's and
// Migrate's: this binary, with engine as its first argument, which the
// child skips) spawn a child that serves too, not another test run.
func TestMain(m *testing.M) {
	if os.Getenv(appEngineChild) == "1" {
		args := os.Args[1:]
		if len(args) > 0 && args[0] == "engine" {
			args = args[1:]
		}
		if err := engine.ServeChild(args, os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Setenv(appEngineChild, "1")
	os.Exit(m.Run())
}
