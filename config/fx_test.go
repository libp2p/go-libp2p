package config

import (
	"errors"
	"testing"

	"go.uber.org/fx"
)

type TestType struct {
	Value string
}

type TestOut struct {
	fx.Out
	Result TestType
}

type TestOutWithMultiple struct {
	fx.Out
	Result TestType
	Other  int
}

func TestNewTypedFxOption_ValidConstructors(t *testing.T) {
	tests := []struct {
		name        string
		constructor any
	}{
		{
			name: "direct return",
			constructor: func() TestType {
				return TestType{Value: "test"}
			},
		},
		{
			name: "return with error",
			constructor: func() (TestType, error) {
				return TestType{Value: "test"}, nil
			},
		},
		{
			name: "fx.Out struct",
			constructor: func() TestOut {
				return TestOut{Result: TestType{Value: "test"}}
			},
		},
		{
			name: "fx.Out struct with error",
			constructor: func() (TestOut, error) {
				return TestOut{Result: TestType{Value: "test"}}, nil
			},
		},
		{
			name: "fx.Out struct with multiple fields",
			constructor: func() TestOutWithMultiple {
				return TestOutWithMultiple{
					Result: TestType{Value: "test"},
					Other:  42,
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			option, err := NewTypedFxProvide[TestType](tt.constructor)
			if err != nil {
				t.Errorf("expected no error, got: %v", err)
			}
			if option == nil {
				t.Error("expected non-nil option")
			}
		})
	}
}

func TestNewTypedFxOption_InvalidConstructors(t *testing.T) {
	tests := []struct {
		name        string
		constructor any
		expectError string
	}{
		{
			name:        "not a function",
			constructor: "not a function",
			expectError: "constructor must be a function",
		},
		{
			name: "no return values",
			constructor: func() {
				// no return
			},
			expectError: "constructor must return at least one value",
		},
		{
			name: "wrong return type",
			constructor: func() int {
				return 42
			},
			expectError: "constructor return type does not match expected type",
		},
		{
			name: "wrong second return type",
			constructor: func() (TestType, int) {
				return TestType{}, 42
			},
			expectError: "constructor return type does not match expected type",
		},
		{
			name: "struct without fx.Out",
			constructor: func() struct {
				Result TestType
			} {
				return struct{ Result TestType }{Result: TestType{}}
			},
			expectError: "constructor return type does not match expected type",
		},
		{
			name: "fx.Out struct without target field",
			constructor: func() struct {
				fx.Out
				Other int
			} {
				return struct {
					fx.Out
					Other int
				}{Other: 42}
			},
			expectError: "constructor return type does not match expected type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewTypedFxProvide[TestType](tt.constructor)
			if err == nil {
				t.Error("expected error but none occurred")
			} else if tt.expectError != "" && !contains(err.Error(), tt.expectError) {
				t.Errorf("expected error containing %q, got %q", tt.expectError, err.Error())
			}
		})
	}
}

func TestNewTypedFxOption_WithDifferentTypes(t *testing.T) {
	// Test with string type
	stringConstructor := func() string {
		return "hello"
	}
	stringOption, err := NewTypedFxProvide[string](stringConstructor)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if stringOption == nil {
		t.Error("expected non-nil string option")
	}

	// Test with int type
	intConstructor := func() (int, error) {
		return 42, nil
	}
	intOption, err := NewTypedFxProvide[int](intConstructor)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if intOption == nil {
		t.Error("expected non-nil int option")
	}

	// Test with pointer type
	pointerConstructor := func() *TestType {
		return &TestType{Value: "pointer"}
	}
	pointerOption, err := NewTypedFxProvide[*TestType](pointerConstructor)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if pointerOption == nil {
		t.Error("expected non-nil pointer option")
	}
}

func TestNewTypedFxOption_WithComplexError(t *testing.T) {
	constructor := func() (TestType, error) {
		return TestType{}, errors.New("test error")
	}

	// Should not error even if constructor returns error
	option, err := NewTypedFxProvide[TestType](constructor)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if option == nil {
		t.Error("expected non-nil option")
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (len(substr) == 0 || findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
