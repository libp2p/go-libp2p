package di

import (
	"errors"
	"strings"
	"testing"
)

// Test successful build with a simple dependency chain A -> B.
func TestBuildSuccess(t *testing.T) {
	type A struct {
		val string
	}
	type C struct {
		val int
	}
	type B struct {
		a  *A
		cs []C
	}

	type Config struct {
		MakeA  Provide[*A]
		MakeB  Provide[*B]
		MakeCs []Provide[C]
	}

	cfg := Config{
		MakeA: MustProvide[*A](func() (*A, error) {
			return &A{val: "hello"}, nil
		}),
		MakeB: MustProvide[*B](func(a *A, cs []C) *B {
			return &B{a: a, cs: cs}
		}),
		MakeCs: []Provide[C]{
			MustProvide[C](C{val: 1}),
			MustProvide[C](func() (C, error) {
				return C{val: 2}, nil
			})},
	}

	type Result struct {
		A *A
		B *B
	}
	var res Result
	err := Build(cfg, &res)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if res.A == nil {
		t.Fatalf("expected res.A to be populated")
	}
	if res.B == nil {
		t.Fatalf("expected res.B to be populated")
	}
	if res.B.a != res.A {
		t.Fatalf("expected B.a to reference A instance")
	}
	if len(res.B.cs) != 2 {
		t.Fatalf("wrong count. Saw %d", len(res.B.cs))
	}
	if res.B.cs[0].val != 1 {
		t.Fatalf("wrong value")
	}
	if res.B.cs[1].val != 2 {
		t.Fatalf("wrong value")
	}
	if res.A.val != "hello" {
		t.Fatalf("unexpected A value: %s", res.A.val)
	}
}

// Test successful build with a simple dependency chain A -> B.
func TestBuildSuccess2(t *testing.T) {
	type A struct {
		val string
	}
	type B struct {
		a *A
	}

	type Config struct {
		MakeA func() (*A, error)
		MakeB func(*A) (*B, error)
	}

	type Result struct {
		A *A
	}

	cfg := Config{
		MakeA: func() (*A, error) {
			return &A{val: "hello"}, nil
		},
		MakeB: func(a *A) (*B, error) {
			panic("Unexpected call to MakeB")
			// (removed unreachable code after panic)
		},
	}

	var res Result
	err := Build(cfg, &res)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if res.A == nil {
		t.Fatalf("expected res.A to be populated")
	}
	if res.A.val != "hello" {
		t.Fatalf("unexpected A value: %s", res.A.val)
	}
}

// Test that constructor error is propagated.
func TestBuildConstructorError(t *testing.T) {
	type A struct{}
	sentinel := errors.New("boom")

	type Config struct {
		MakeA func() (*A, error)
	}
	type Result struct {
		A *A
	}

	cfg := Config{
		MakeA: func() (*A, error) {
			return nil, sentinel
		},
	}
	var res Result
	err := Build(cfg, &res)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "MakeA") {
		t.Fatalf("expected error to mention constructor name, got: %v", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected original error message, got: %v", err)
	}
	if res.A != nil {
		t.Fatalf("result A should not be populated on constructor failure")
	}
}

// Test missing dependency (constructor requires *A but *A not provided).
func TestBuildMissingDependency(t *testing.T) {
	type A struct{}
	type B struct {
		a *A
	}

	type Config struct {
		MakeB func(*A) (*B, error)
	}
	type Result struct {
		B *B
	}

	cfg := Config{
		MakeB: func(a *A) (*B, error) {
			return &B{a: a}, nil
		},
	}
	var res Result
	err := Build(cfg, &res)
	if err == nil {
		t.Fatalf("expected missing dependency error")
	}
	// Parameter type string should appear (may be *di.A).
	if !strings.Contains(err.Error(), "*di.A") && !strings.Contains(err.Error(), "di.A") {
		t.Fatalf("expected error to mention missing type *di.A, got: %v", err)
	}
	if res.B != nil {
		t.Fatalf("result B should not be populated")
	}
}

// Test cycle detection between X and Y.
func TestBuildCycleDetection(t *testing.T) {
	type X struct{}
	type Y struct{}

	type Config struct {
		MakeX func(*Y) *X
		MakeY func(*X) *Y
	}
	type Result struct {
		X *X
		Y *Y
	}

	cfg := Config{
		MakeX: func(y *Y) *X {
			return &X{}
		},
		MakeY: func(x *X) *Y {
			return &Y{}
		},
	}

	var res Result
	err := Build(cfg, &res)
	if err == nil {
		t.Fatalf("expected cycle detection error")
	}
	// Both constructors should still be listed as remaining.
	if !strings.Contains(err.Error(), "MakeX") || !strings.Contains(err.Error(), "MakeY") {
		t.Fatalf("expected error to list remaining constructors MakeX and MakeY, got: %v", err)
	}
	if res.X != nil || res.Y != nil {
		t.Fatalf("cycle should prevent any construction; got X=%v Y=%v", res.X, res.Y)
	}
}

// Ensure that providing pre-supplied value satisfies dependency without constructor for it.
func TestBuildWithPreSuppliedValue(t *testing.T) {
	type A struct {
		v int
	}
	type B struct {
		a *A
	}

	type Config struct {
		// Only constructor for B; A provided directly in config.
		A  *A
		MB func(*A) (*B, error)
	}
	type Result struct {
		A *A
		B *B
	}

	cfg := Config{
		A: &A{v: 42},
		MB: func(a *A) (*B, error) {
			return &B{a: a}, nil
		},
	}

	var res Result
	err := Build(cfg, &res)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.A == nil || res.A.v != 42 {
		t.Fatalf("expected pre-supplied A (42), got %+v", res.A)
	}
	if res.B == nil || res.B.a != res.A {
		t.Fatalf("expected B referencing A, got %+v", res.B)
	}
}

func TestTypeAlias(t *testing.T) {
	type ANum int
	type Config struct {
		A ANum
		B int
	}

	type Result struct {
		A ANum
	}
	var res Result
	err := Build(Config{3, 4}, &res)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.A != 3 {
		t.Fatalf("expected A=3, got %v", res.A)
	}
}

func TestReferenceConfig(t *testing.T) {
	type Config struct {
		SomeSetting bool
		Inner       func(c Config) int
	}

	type Result struct {
		A int
	}
	var res Result
	err := Build(Config{
		SomeSetting: true,
		Inner: func(c Config) int {
			if c.SomeSetting {
				return 1
			}
			return 0
		},
	}, &res)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.A != 1 {
		t.Fatalf("expected A=1, got %v", res.A)
	}
}
