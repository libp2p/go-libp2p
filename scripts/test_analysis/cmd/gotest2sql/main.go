// gotest2sql inserts the output of go test -json ./... into a sqlite database
package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

type TestEvent struct {
	Time    time.Time // encodes as an RFC3339-format string
	Action  string
	Package string
	Test    string
	Elapsed float64 // seconds
	Output  string
}

func main() {
	outputPath := flag.String("output", "", "output db file")
	verbose := flag.Bool("v", false, "Print test output to stdout")
	flag.Parse()

	if *outputPath == "" {
		log.Fatal("-output path is required")
	}

	db, err := sql.Open("sqlite", *outputPath)
	if err != nil {
		log.Fatal(err)
	}

	// Create a table to store test results.
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS test_results (
			Time TEXT,
			Action TEXT,
			Package TEXT,
			Test TEXT,
			Elapsed REAL,
			Output TEXT,
			BatchInsertTime TEXT
	)`)
	if err != nil {
		log.Fatal(err)
	}

	tx, err := db.Begin()
	if err != nil {
		log.Fatal(err)
	}

	// Prepare the insert statement once
	insertTime := time.Now().Format(time.RFC3339Nano)
	stmt, err := tx.Prepare(`
    INSERT INTO test_results (Time, Action, Package, Test, Elapsed, Output, BatchInsertTime)
    VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		log.Fatal(err)
	}
	defer stmt.Close() // Ensure the statement is closed after use

	s := bufio.NewScanner(os.Stdin)
	for s.Scan() {
		line := s.Bytes()
		var ev TestEvent
		err = json.Unmarshal(line, &ev)
		if err != nil {
			log.Fatal(err)
		}
		if *verbose && ev.Action == "output" {
			fmt.Print(ev.Output)
		}

		_, err = stmt.Exec(
			ev.Time.Format(time.RFC3339Nano),
			ev.Action,
			ev.Package,
			ev.Test,
			ev.Elapsed,
			ev.Output,
			insertTime,
		)
		if err != nil {
			log.Fatal(err)
		}
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}
}
