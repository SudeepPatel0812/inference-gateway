package tools

import (
	"fmt"
	"time"
	_ "time/tzdata" // embeds timezone data: the distroless image has none, so LoadLocation would fail
)

var timeTool = Tool{
	Name:        "get_current_time",
	Description: "Returns the current date and time. Use it whenever the user asks about the current time or date. Pass the user's timezone if known.",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"timezone": map[string]any{
				"type":        "string",
				"description": "IANA timezone name, e.g. Asia/Kolkata or America/New_York. Defaults to UTC.",
			},
		},
	},
	Run: func(args map[string]any) (string, error) {
		tz, err := stringArg(args, "timezone", false)
		if err != nil {
			return "", err
		}
		if tz == "" {
			tz = "UTC"
		}
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return "", fmt.Errorf("unknown timezone %q", tz)
		}
		return time.Now().In(loc).Format(time.RFC3339), nil
	},
}
