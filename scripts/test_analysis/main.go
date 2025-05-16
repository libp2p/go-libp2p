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

	_ "modernc.org/sqlite"
)

const dbPath = "./test_results.db"
const retryCount = 4 // For a total of 5 runs

var coverRegex = regexp.MustCompile(`-cover`)

func main() {
	var t tester
	if len(os.Args) >= 2 {
		if os.Args[1] == "summarize" {
			md, err := t.summarize()
			if err != nil {
				log.Fatal(err)
			}
			fmt.Print(md)
			return
		}
	}

	passThruFlags := os.Args[1:]
	err := t.runTests(passThruFlags)
	if err != nil {
		log.Fatal(err)
	}
}

type tester struct {
	Dir string
}

func (t *tester) runTests(passThruFlags []string) error {
	err := t.goTestAll(passThruFlags)
	if err == nil {
		// No failed tests, nothing to do
		return nil
	}
	log.Printf("Not all tests passed: %v", err)

	timedOutPackages, err := t.findTimedoutTests(context.Background())
	if err != nil {
		return err
	}
	if len(timedOutPackages) > 0 {
		// Fail immediately if we find any timeouts. We'd have to run all tests
		// in the package, and this could take a long time.
		log.Printf("Found %d timed out packages. Failing", len(timedOutPackages))
		return errors.New("one or more tests timed out")
	}

	failedTests, err := t.findFailedTests(context.Background())
	if err != nil {
		return err
	}

	log.Printf("Found %d failed tests. Retrying them %d times", len(failedTests), retryCount)
	hasOneNonFlakyFailure := false
	loggedFlaky := map[string]struct{}{}

	for _, ft := range failedTests {
		isFlaky := false
		for i := 0; i < retryCount; i++ {
			log.Printf("Retrying %s.%s", ft.Package, ft.Test)
			if err := t.goTestPkgTest(ft.Package, ft.Test, filterOutFlags(passThruFlags, coverRegex)); err != nil {
				log.Printf("Failed to run %s.%s: %v", ft.Package, ft.Test, err)
			} else {
				isFlaky = true
				flakyName := ft.Package + "." + ft.Test
				if _, ok := loggedFlaky[flakyName]; !ok {
					loggedFlaky[flakyName] = struct{}{}
					log.Printf("Test %s.%s is flaky.", ft.Package, ft.Test)
				}
			}
		}
		if !isFlaky {
			hasOneNonFlakyFailure = true
		}
	}

	// A test consistently failed, so we should exit with a non-zero exit code.
	if hasOneNonFlakyFailure {
		return errors.New("one or more tests consistently failed")
	}
	return nil
}

func (t *tester) goTestAll(extraFlags []string) error {
	flags := []string{"./..."}
	flags = append(flags, extraFlags...)
	return t.goTest(flags)
}

func (t *tester) goTestPkgTest(pkg, testname string, extraFlags []string) error {
	flags := []string{
		pkg, "-run", "^" + testname + "$", "-count", "1",
	}
	flags = append(flags, extraFlags...)
	return t.goTest(flags)
}

func (t *tester) goTest(extraFlags []string) error {
	flags := []string{
		"test", "-json",
	}
	flags = append(flags, extraFlags...)
	cmd := exec.Command("go", flags...)
	cmd.Dir = t.Dir
	cmd.Stderr = os.Stderr

	gotest2sql := exec.Command("gotest2sql", "-v", "-output", dbPath)
	gotest2sql.Dir = t.Dir
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

type timedOutPackage struct {
	Package string
	Outputs string
}

func (t *tester) findFailedTests(ctx context.Context) ([]failedTest, error) {
	db, err := sql.Open("sqlite", t.Dir+dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

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

func (t *tester) findTimedoutTests(ctx context.Context) ([]timedOutPackage, error) {
	db, err := sql.Open("sqlite", t.Dir+dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `WITH failed_packages AS (
    SELECT
        Package
    FROM
        test_results
    WHERE
        Action = 'fail'
        AND Elapsed > 300
)
SELECT
	test_results.Package, GROUP_CONCAT(Output, "") as Outputs
FROM
    test_results
INNER JOIN
    failed_packages
ON
    test_results.Package = failed_packages.Package
GROUP BY
    test_results.Package
HAVING
    Outputs LIKE '%timed out%'
ORDER BY Time;`)
	if err != nil {
		return nil, err
	}
	var out []timedOutPackage
	for rows.Next() {
		var pkg, outputs string
		if err := rows.Scan(&pkg, &outputs); err != nil {
			return nil, err
		}
		out = append(out, timedOutPackage{pkg, outputs})
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
	return out
}

// summarize returns a markdown string of the test results.
func (t *tester) summarize() (string, error) {
	ctx := context.Background()
	var out strings.Builder

	testFailures, err := t.findFailedTests(ctx)
	if err != nil {
		return "", err
	}
	timeouts, err := t.findTimedoutTests(ctx)
	if err != nil {
		return "", err
	}

	testFailureCount := len(testFailures) + len(timeouts)

	plural := "s"
	if testFailureCount == 1 {
		plural = ""
	}
	out.WriteString(fmt.Sprintf("## %d Test Failure%s\n\n", testFailureCount, plural))

	if len(timeouts) > 0 {
		out.WriteString("### Timed Out Tests\n\n")
		for _, timeout := range timeouts {
			_, err = out.WriteString(fmt.Sprintf(`<details>
<summary>%s</summary>
<pre>
%s
</pre>
</details>`, timeout.Package, timeout.Outputs))
			if err != nil {
				return "", err
			}
		}
		out.WriteString("\n")
	}

	if len(testFailures) > 0 {
		out.WriteString("### Failed Tests\n\n")

		db, err := sql.Open("sqlite", t.Dir+dbPath)
		if err != nil {
			return "", err
		}
		defer db.Close()

		rows, err := db.QueryContext(ctx, `SELECT
    tr_output.Package,
    tr_output.Test,
    GROUP_CONCAT(tr_output.Output,  "") AS Outputs
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
	}
	return out.String(), nil
}
