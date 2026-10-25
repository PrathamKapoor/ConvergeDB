package main

import (
	"flag"
	"fmt"

	"github.com/PrathamKapoor/ConvergeDB/internal/store"
)

// cmdAdmin holds operational subcommands (backup/restore helpers).
func cmdAdmin(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: convergedb admin backup --db <path> --out <file>")
	}
	switch args[0] {
	case "backup":
		fs := flag.NewFlagSet("admin backup", flag.ContinueOnError)
		db := fs.String("db", "", "database file of the replica to back up")
		out := fs.String("out", "", "backup output file (consistent snapshot incl. WAL content)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *db == "" || *out == "" {
			return fmt.Errorf("--db and --out are required")
		}
		st, err := store.Open(*db)
		if err != nil {
			return err
		}
		defer st.Close()
		if err := st.Backup(*out); err != nil {
			return err
		}
		fmt.Printf("backup written: %s\n", *out)
		return nil
	default:
		return fmt.Errorf("unknown admin command %q", args[0])
	}
}
