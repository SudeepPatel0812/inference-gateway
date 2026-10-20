package tools

import (
	"fmt"
	"time"

	"inference-gateway/internal/metrics"
)

type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Run         func(args map[string]any) (string, error)
}

// Registry is the allowlist: the model can only run tools listed here.
// Each tool lives in its own file (time.go, calculator.go, fetch.go).
var Registry = map[string]Tool{
	timeTool.Name:       timeTool,
	calculatorTool.Name: calculatorTool,
	fetchTool.Name:      fetchTool,
}

func Definitions() []map[string]any {
	defs := []map[string]any{}

	for _, t := range Registry {
		defs = append(defs, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Parameters,
			},
		})
	}

	return defs
}

// Run is the single path every tool call takes, so it's where tool metrics are recorded.
func Run(name string, args map[string]any) (string, error) {
	start := time.Now()
	tool, ok := Registry[name]
	if !ok {
		err := fmt.Errorf("unknown tool %q", name)
		// label "unknown", never the model's made-up name: each new label value is a new time series
		metrics.ObserveTool("unknown", time.Since(start), err)
		return "", err
	}
	out, err := tool.Run(args)
	metrics.ObserveTool(name, time.Since(start), err)
	return out, err
}

// Arguments come from the model, so every tool validates them like user input.
// JSON numbers always decode to float64 in a map[string]any, never int.

func numberArg(args map[string]any, key string) (float64, error) {
	v, ok := args[key].(float64)
	if !ok {
		return 0, fmt.Errorf("argument %q must be a number", key)
	}
	return v, nil
}

// stringArg returns "" without an error when an optional argument is missing.
func stringArg(args map[string]any, key string, required bool) (string, error) {
	raw, present := args[key]
	if !present || raw == nil {
		if required {
			return "", fmt.Errorf("argument %q is required", key)
		}
		return "", nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string", key)
	}
	return s, nil
}
