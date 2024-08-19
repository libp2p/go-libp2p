package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"

	_ "github.com/glebarez/go-sqlite"
)

const dbPath = "./test_results.db"
const retryCount = 4 // For a total of 5 runs

var coverRegex = regexp.MustCompile(`-cover`)

func main() {
	if len(os.Args) >= 2 {
		if os.Args[1] == "summarize" {
			md, err := summarize()
			if err != nil {
				log.Fatal(err)
			}
			fmt.Print(md)
			return
		}
	}

	passThruFlags := os.Args[1:]

	err := goTestAll(passThruFlags)
	if err == nil {
		// No failed tests, nothing to do
		return
	}
	log.Printf("Not all tests passed: %v", err)

	failedTests, err := findFailedTests(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Found %d failed tests. Retrying them %d times", len(failedTests), retryCount)
	hasOneNonFlakyFailure := false

	for _, ft := range failedTests {
		isFlaky := false
		for i := 0; i < retryCount; i++ {
			log.Printf("Retrying %s.%s", ft.Package, ft.Test)
			if err := goTestPkgTest(ft.Package, ft.Test, filterOutFlags(passThruFlags, coverRegex)); err != nil {
				log.Printf("Failed to run %s.%s: %v", ft.Package, ft.Test, err)
			} else {
				isFlaky = true
				log.Printf("Test %s.%s is flaky.", ft.Package, ft.Test)
			}
		}
		if !isFlaky {
			hasOneNonFlakyFailure = true
		}
	}

	// A test consistently failed, so we should exit with a non-zero exit code.
	if hasOneNonFlakyFailure {
		os.Exit(1)
	}
}

func goTestAll(extraFlags []string) error {
	flags := []string{"./..."}
	flags = append(flags, extraFlags...)
	return goTest(flags)
}

func goTestPkgTest(pkg, testname string, extraFlags []string) error {
	flags := []string{
		pkg, "-run", "^" + testname + "$", "-count", "1",
	}
	flags = append(flags, extraFlags...)
	return goTest(flags)
}

func goTest(extraFlags []string) error {
	flags := []string{
		"test", "-json",
	}
	flags = append(flags, extraFlags...)
	cmd := exec.Command("go", flags...)
	cmd.Stderr = os.Stderr

	gotest2sql := exec.Command("gotest2sql", "-v", "-output", dbPath)
	gotest2sql.Stdin, _ = cmd.StdoutPipe()
	gotest2sql.Stdout = os.Stdout
	gotest2sql.Stderr = os.Stderr
	err := gotest2sql.Start()
	if err != nil {
		return err
	}

	err = cmd.Run()
	return errors.Join(err, gotest2sql.Wait())
}

type failedTest struct {
	Package string
	Test    string
}

func findFailedTests(ctx context.Context) ([]failedTest, error) {
	// connect
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatal(err)
	}

	rows, err := db.QueryContext(ctx, "SELECT DISTINCT Package, Test FROM test_results where Action='fail' and Test != ''")
	if err != nil {
		return nil, err
	}
	var out []failedTest
	for rows.Next() {
		var pkg, test string
		if err := rows.Scan(&pkg, &test); err != nil {
			return nil, err
		}
		out = append(out, failedTest{pkg, test})
	}
	return out, nil
}

func filterOutFlags(flags []string, exclude *regexp.Regexp) []string {
	out := make([]string, 0, len(flags))
	for _, f := range flags {
		if !exclude.MatchString(f) {
			out = append(out, f)
		}
	}
	fmt.Println(out)
	return out
}

// summarize returns a markdown string of the test results.
func summarize() (string, error) {
	ctx := context.Background()
	var out strings.Builder

	testFailures, err := findFailedTests(ctx)
	if err != nil {
		return "", err
	}

	plural := "s"
	if len(testFailures) == 1 {
		plural = ""
	}
	out.WriteString(fmt.Sprintf("## %d Test Failure%s\n\n", len(testFailures), plural))

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return "", err
	}

	rows, err := db.QueryContext(ctx, `SELECT
    tr_output.Package,
    tr_output.Test,
    GROUP_CONCAT(tr_output.Output,  x'0a') AS Outputs
FROM
    test_results tr_fail
JOIN
    test_results tr_output
ON
    tr_fail.Test = tr_output.Test
    AND tr_fail.BatchInsertTime = tr_output.BatchInsertTime
    AND tr_fail.Package = tr_output.Package
WHERE
    tr_fail.Action = 'fail'
    AND tr_output.Test != ''
GROUP BY
    tr_output.BatchInsertTime,
    tr_output.Package,
    tr_output.Test
ORDER BY
    MIN(tr_output.Time);`)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var pkg, test, outputs string
		if err := rows.Scan(&pkg, &test, &outputs); err != nil {
			return "", err
		}
		_, err = out.WriteString(fmt.Sprintf(`<details>
<summary>%s.%s</summary>
<pre>
%s
</pre>
</details>`, pkg, test, outputs))
		if err != nil {
			return "", err
		}
	}
	return out.String(), nil
}
