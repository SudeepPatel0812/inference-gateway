package tools

import (
	"errors"
	"fmt"
	"strconv"
)

// Typed arguments + an allowlisted op, never an expression string to eval:
// evaluating model-written expressions would let a prompt run arbitrary code.
var calculatorTool = Tool{
	Name:        "calculator",
	Description: "Does exact arithmetic on two numbers. Use it for any calculation instead of computing in your head.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"a":  map[string]any{"type": "number", "description": "first number"},
			"b":  map[string]any{"type": "number", "description": "second number"},
			"op": map[string]any{"type": "string", "enum": []string{"add", "subtract", "multiply", "divide"}},
		},
		"required": []string{"a", "b", "op"},
	},
	Run: func(args map[string]any) (string, error) {
		a, err := numberArg(args, "a")
		if err != nil {
			return "", err
		}
		b, err := numberArg(args, "b")
		if err != nil {
			return "", err
		}
		op, err := stringArg(args, "op", true)
		if err != nil {
			return "", err
		}

		var result float64
		switch op {
		case "add":
			result = a + b
		case "subtract":
			result = a - b
		case "multiply":
			result = a * b
		case "divide":
			if b == 0 {
				return "", errors.New("division by zero")
			}
			result = a / b
		default:
			return "", fmt.Errorf("unsupported op %q: use add, subtract, multiply or divide", op)
		}
		return strconv.FormatFloat(result, 'f', -1, 64), nil // 391, not 3.91e+02
	},
}
