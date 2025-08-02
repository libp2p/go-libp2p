package config

import (
	"testing"
)

func TestNilOption(t *testing.T) {
	var cfg Config
	optsRun := 0
	opt := func(_ *Config) error {
		optsRun++
		return nil
	}
	if err := cfg.Apply(nil); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Apply(opt, nil, nil, opt, opt, nil); err != nil {
		t.Fatal(err)
	}
	if optsRun != 3 {
		t.Fatalf("expected to have handled 3 options, handled %d", optsRun)
	}
}
