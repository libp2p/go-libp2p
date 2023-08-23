//go:build !go1.21

package testutils

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
)

// A LogHarness runs sets of assertions against the log output of a function. Assertions are grouped
// into sequences of messages that are expected to be found in the log output. Calling one of the Expect
// methods on the harness adds an expectation to the default sequence of messages. Additional sequences
// can be created by calling NewSequence.
type LogHarness struct {
	buf       bytes.Buffer
	sequences []*Sequence
}

type Expectation interface {
	IsMatch(line string) bool
	String() string
}

// Run executes the function f and captures any output written using Go's standard log. Each sequence
// of expected messages is then
func (h *LogHarness) Run(t *testing.T, f func()) {
	// Capture raw log output
	fl := log.Flags()
	log.SetFlags(0)
	log.SetOutput(&h.buf)
	f()
	log.SetFlags(fl)
	log.SetOutput(os.Stderr)

	for _, seq := range h.sequences {
		seq.Assert(t, bufio.NewScanner(bytes.NewReader(h.buf.Bytes())))
	}
}

// Expect adds an expectation to the default sequence that the log contains a line equal to s
func (h *LogHarness) Expect(s string) {
	if len(h.sequences) == 0 {
		h.sequences = append(h.sequences, &Sequence{name: ""})
	}
	h.sequences[0].Expect(s)
}

// ExpectPrefix adds an to the default sequence expectation that the log contains a line starting with s
func (h *LogHarness) ExpectPrefix(s string) {
	if len(h.sequences) == 0 {
		h.sequences = append(h.sequences, &Sequence{name: ""})
	}
	h.sequences[0].ExpectPrefix(s)
}

// NewSequence creates a new sequence of expected log messages
func (h *LogHarness) NewSequence(name string) *Sequence {
	seq := &Sequence{name: name}
	h.sequences = append(h.sequences, seq)
	return seq
}

type prefix string

func (p prefix) IsMatch(line string) bool {
	return strings.HasPrefix(line, string(p))
}

func (p prefix) String() string {
	return fmt.Sprintf("prefix %q", string(p))
}

type text string

func (t text) IsMatch(line string) bool {
	return line == string(t)
}

func (t text) String() string {
	return fmt.Sprintf("text %q", string(t))
}

type Sequence struct {
	name string
	exp  []Expectation
}

func (seq *Sequence) Assert(t *testing.T, s *bufio.Scanner) {
	var tag string
	if seq.name != "" {
		tag = fmt.Sprintf("[%s] ", seq.name)
	}
	// Match raw log lines against expectations
exploop:
	for _, e := range seq.exp {
		for s.Scan() {
			if e.IsMatch(s.Text()) {
				t.Logf("%ssaw: %s", tag, s.Text())
				continue exploop
			}
		}
		if s.Err() == nil {
			t.Errorf("%sdid not see expected %s", tag, e.String())
			return
		}
	}
}

// Expect adds an expectation that the log contains a line equal to s
func (seq *Sequence) Expect(s string) {
	seq.exp = append(seq.exp, text(s))
}

// ExpectPrefix adds an expectation that the log contains a line starting with s
func (seq *Sequence) ExpectPrefix(s string) {
	seq.exp = append(seq.exp, prefix(s))
}
