package test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerateTestAddrs(t *testing.T) {
	var tableTest = []struct {
		description    string
		inputInt       int
		expectedOutput []string
	}{
		{
			description:    "generate 0 results",
			inputInt:       0,
			expectedOutput: []string{},
		},
		{
			description: "generate a few results",
			inputInt:    3,
			expectedOutput: []string{
				"/ip4/1.2.3.4/tcp/0",
				"/ip4/1.2.3.4/tcp/1",
				"/ip4/1.2.3.4/tcp/2",
			},
		},
	}

	for _, tt := range tableTest {
		tt := tt

		t.Run(tt.description, func(t *testing.T) {
			actualOutput := GenerateTestAddrs(tt.inputInt)

			outputStringArray := []string{}

			for index := range actualOutput {
				outputStringArray = append(outputStringArray, actualOutput[index].String())
			}

			assert.Equal(t, outputStringArray, tt.expectedOutput)
		})
	}
}
