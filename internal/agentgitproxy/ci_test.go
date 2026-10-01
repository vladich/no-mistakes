package agentgitproxy

import (
	"fmt"
	"strings"
	"testing"
)

func TestIntegrationWorkflowAdmission(t *testing.T) {
	valid := fmt.Sprintf("workflow:\n  rules:\n    - if: '%s'\n      when: never\n    - if: '%s'\n      when: never\n    - if: '$CI_COMMIT_BRANCH == \"dev\"'\n", SuppressMR, SuppressTask)
	if err := ValidateIntegrationWorkflow([]byte(valid), "dev"); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"MR allowed":               strings.Replace(valid, "      when: never", "      when: always", 1),
		"conditional suppression":  strings.Replace(valid, "      when: never", "      when: never\n      changes: [optional]", 1),
		"missing task suppression": strings.Replace(valid, SuppressTask, "$CI_COMMIT_BRANCH == \"other\"", 1),
		"wrong integration":        strings.Replace(valid, "\"dev\"", "\"master\"", 1),
		"late refusals":            strings.Replace(valid, "  rules:\n", "  rules:\n    - when: always\n", 1),
		"missing":                  "", "invalid YAML": "workflow: [",
		"duplicate workflow":            valid + "workflow:\n  rules:\n    - when: always\n",
		"second document":               valid + "---\nworkflow:\n  rules:\n    - when: always\n",
		"shadowed integration":          strings.Replace(valid, "    - if: '$CI_COMMIT_BRANCH == \"dev\"'", "    - when: never\n    - if: '$CI_COMMIT_BRANCH == \"dev\"'", 1),
		"conditional integration":       valid + "      changes: [optional]\n",
		"duplicate integration verdict": valid + "      when: always\n      when: never\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateIntegrationWorkflow([]byte(data), "dev"); err == nil {
				t.Fatal("unsafe workflow admitted")
			}
		})
	}
}
