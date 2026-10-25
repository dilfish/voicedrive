// agent-eval scores external recordings; it never connects to the platform.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Study-of-slag-weng/voicedrive1/internal/agenteval"
)

func load(path string, target any) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	err = agenteval.Decode(f, target)
	closeErr := f.Close()
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if closeErr != nil {
		return fmt.Errorf("close %s: %w", path, closeErr)
	}
	return nil
}

func run(args []string, output, errors io.Writer) int {
	flags := flag.NewFlagSet("agent-eval", flag.ContinueOnError)
	flags.SetOutput(errors)
	corpus := flags.String("corpus", "docs/agent-v2/commands.json", "versioned command corpus")
	results := flags.String("results", "", "recorded results JSON (required)")
	report := flags.String("report", "", "write JSON report to this new file; default stdout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *results == "" || flags.NArg() != 0 {
		fmt.Fprintln(errors, "-results is required; positional arguments are not supported")
		return 2
	}
	var c agenteval.Corpus
	var o agenteval.Observations
	if err := load(*corpus, &c); err != nil {
		fmt.Fprintln(errors, err)
		return 2
	}
	if err := load(*results, &o); err != nil {
		fmt.Fprintln(errors, err)
		return 2
	}
	r, err := agenteval.Evaluate(c, o)
	if err != nil {
		fmt.Fprintln(errors, err)
		return 2
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		fmt.Fprintln(errors, "encode report:", err)
		return 2
	}
	data = append(data, '\n')
	if *report != "" {
		// Exclusive creation prevents a report path from destroying input or evidence.
		f, err := os.OpenFile(*report, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			fmt.Fprintln(errors, "create report:", err)
			return 2
		}
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr != nil {
			fmt.Fprintln(errors, "write report:", writeErr)
			return 2
		}
		if closeErr != nil {
			fmt.Fprintln(errors, "close report:", closeErr)
			return 2
		}
	} else if _, err := output.Write(data); err != nil {
		fmt.Fprintln(errors, "write report:", err)
		return 2
	}
	if !r.Passed {
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
