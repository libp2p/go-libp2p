package main

import (
	"os"
	"testing"
)

func TestFailsOnConsistentFailure(t *testing.T) {
	tmpDir := t.TempDir() + "/"
	os.WriteFile(tmpDir+"/main.go", []byte(`package main
func main() {}`), 0644)
	// Add a test that fails consistently.
	os.WriteFile(tmpDir+"/main_test.go", []byte(`package main

import (
	"testing"
)
func TestConsistentFailure(t *testing.T) {
	t.Fatal("consistent failure")
}`), 0644)
	os.WriteFile(tmpDir+"/go.mod", []byte(`module example.com/test`), 0644)

	tstr := tester{Dir: tmpDir}
	err := tstr.runTests(nil)
	if err == nil {
		t.Fatal("Should have failed with a consistent failure")
	}
}

func TestPassesOnFlakyFailure(t *testing.T) {
	tmpDir := t.TempDir() + "/"
	os.WriteFile(tmpDir+"/main.go", []byte(`package main
func main() {
}`), 0644)
	// Add a test that fails the first time.
	os.WriteFile(tmpDir+"/main_test.go", []byte(`package main
import (
	"os"
	"testing"
)
func TestFlakyFailure(t *testing.T) {
	_, err := os.Stat("foo")
	if err != nil {
		os.WriteFile("foo", []byte("hello"), 0644)
		t.Fatal("flaky failure")
	}
}`), 0644)
	os.WriteFile(tmpDir+"/go.mod", []byte(`module example.com/test`), 0644)

	// Run the test.
	tstr := tester{Dir: tmpDir}
	err := tstr.runTests(nil)
	if err != nil {
		t.Fatal("Should have passed with a flaky test")
	}
}
