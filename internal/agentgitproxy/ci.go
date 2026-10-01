package agentgitproxy

import (
	"bytes"
	"fmt"
	"io"
	"strconv"

	"gopkg.in/yaml.v3"
)

const SuppressMR = `$CI_PIPELINE_SOURCE == "merge_request_event"`
const SuppressTask = `$CI_COMMIT_BRANCH =~ /^task\//`

// ValidateIntegrationWorkflow recognizes an explicit, project-owned contract.
// ci.skip cannot suppress MR pipelines. These first two unconditional refusal
// rules make that guarantee independent of later includes and job rules.
// Full integration-job results remain the CI owner's verification duty.
func ValidateIntegrationWorkflow(data []byte, integration string) error {
	if len(data) == 0 || len(data) > 1024*1024 {
		return fmt.Errorf("agent Git proxy CI workflow is missing or exceeds its bound")
	}
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil || len(document.Content) != 1 || decoder.Decode(&yaml.Node{}) != io.EOF {
		return fmt.Errorf("agent Git proxy cannot read the project-owned CI workflow")
	}
	field := func(node *yaml.Node, name string) *yaml.Node {
		if node == nil || node.Kind != yaml.MappingNode {
			return nil
		}
		var result *yaml.Node
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == name {
				if result != nil {
					return nil
				}
				result = node.Content[i+1]
			}
		}
		return result
	}
	rules := field(field(document.Content[0], "workflow"), "rules")
	if rules == nil || rules.Kind != yaml.SequenceNode || len(rules.Content) < 3 {
		return fmt.Errorf("agent Git proxy requires explicit task and MR pipeline suppression in workflow.rules")
	}
	seen := map[string]bool{}
	for _, rule := range rules.Content[:2] {
		condition, when := field(rule, "if"), field(rule, "when")
		if rule.Kind != yaml.MappingNode || len(rule.Content) != 4 || condition == nil || when == nil ||
			condition.Kind != yaml.ScalarNode || when.Kind != yaml.ScalarNode || when.Value != "never" ||
			(condition.Value != SuppressMR && condition.Value != SuppressTask) || seen[condition.Value] {
			return fmt.Errorf("agent Git proxy requires the first two workflow rules to suppress task and MR pipelines unconditionally")
		}
		seen[condition.Value] = true
	}
	for _, rule := range rules.Content[2:] {
		condition, when := field(rule, "if"), field(rule, "when")
		if condition == nil || (when != nil && when.Value == "never") {
			return fmt.Errorf("agent Git proxy integration admission is shadowed by a refusal rule")
		}
		if condition != nil && condition.Value == "$CI_COMMIT_BRANCH == "+strconv.Quote(integration) && (when == nil || when.Value == "always") {
			keys := make(map[string]bool)
			for i := 0; i+1 < len(rule.Content); i += 2 {
				key := rule.Content[i].Value
				if keys[key] || (key != "if" && key != "when" && key != "auto_cancel" && key != "variables") {
					return fmt.Errorf("agent Git proxy integration admission has an additional condition")
				}
				keys[key] = true
			}
			return nil
		}
	}
	return fmt.Errorf("agent Git proxy requires its integration branch to admit a CI pipeline")
}
