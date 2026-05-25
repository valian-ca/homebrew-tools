// review-triage — interactive triage of code-review findings for the
// valian:review skill. Reads findings as JSON, presents an interactive TUI,
// and writes the fix/skip/discuss decisions back as JSON.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/valian-ca/homebrew-tools/internal/review-triage/contract"
	"github.com/valian-ca/homebrew-tools/internal/review-triage/tui"
)

// version is stamped in by the Homebrew formula via -ldflags.
var version = "dev"

const (
	exitOK       = 0
	exitCancel   = 1
	exitInternal = 2
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	fs := flag.NewFlagSet("review-triage", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	fs.Usage = func() { printUsage(fs.Output()) }
	inputPath := fs.String("input", "", "path to the input JSON written by the skill")
	outputPath := fs.String("output", "", "path to write the decisions JSON on submit")
	showVersion := fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitInternal
	}
	if *showVersion {
		fmt.Printf("review-triage %s\n", version)
		return exitOK
	}
	if *inputPath == "" || *outputPath == "" {
		fmt.Fprintln(os.Stderr, "review-triage: --input and --output are required")
		fs.Usage()
		return exitInternal
	}

	in, err := contract.Load(*inputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "review-triage: %v\n", err)
		return exitInternal
	}

	res, err := tui.Run(in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "review-triage: %v\n", err)
		return exitInternal
	}

	switch res.Outcome {
	case tui.OutcomeSubmit:
		out := contract.Output{SchemaVersion: contract.SchemaVersion, Decisions: res.Decisions}
		if err := out.Write(*outputPath); err != nil {
			fmt.Fprintf(os.Stderr, "review-triage: %v\n", err)
			return exitInternal
		}
		return exitOK
	case tui.OutcomeCancel:
		return exitCancel
	default:
		fmt.Fprintln(os.Stderr, "review-triage: TUI exited without a decision")
		return exitCancel
	}
}

func printUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `review-triage — interactive triage of code-review findings.

USAGE
  review-triage --input <path> --output <path>

OPTIONS
  --input <path>    Input JSON written by the valian:review skill (required).
  --output <path>   Path to write the decisions JSON on submit (required).
  --version         Print version and exit.
  -h, --help        Show this help.

EXIT CODES
  0   Decisions written to --output.
  1   Cancelled by the user (Ctrl-C / quit). No output written.
  2   Internal error (malformed input, schema mismatch, write failure).

ENVIRONMENT
  NO_COLOR   When set, render with no colour — diff uses -/+ markers and
             badges render as plain [FIX]/[SKIP]/[DISCUSS]/[?].

EXAMPLES
  review-triage --input /tmp/rt-in-abc.json --output /tmp/rt-out-abc.json
`)
}
