// Command convergedb is the ConvergeDB CLI: serve a replica, run the
// seeded partition-heal demo, inspect/verify live nodes, benchmark, and
// doctor the environment.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe(os.Args[2:])
	case "demo":
		err = cmdDemo(os.Args[2:])
	case "inspect":
		err = cmdInspect(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "bench":
		err = cmdBench(os.Args[2:])
	case "doctor":
		err = cmdDoctor(os.Args[2:])
	case "version", "--version":
		fmt.Println("convergedb " + version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

const version = "v0.1.0"

func usage() {
	fmt.Fprintf(os.Stderr, `ConvergeDB — local-first replicated document database (protocol v1)

Usage:
  convergedb serve --id alpha --db ./var/alpha.db --http 127.0.0.1:8101 [options]
  convergedb demo partition-heal [--out report.json] [--seed N] [--dir ./var/demo]
  convergedb inspect --node http://127.0.0.1:8101 [--document doc-id]
  convergedb verify --nodes alpha=http://127.0.0.1:8101,... --out report.json
  convergedb bench --writes 500 --out ./var/bench
  convergedb doctor

Run 'convergedb <command> -h' for command options.
`)
}
