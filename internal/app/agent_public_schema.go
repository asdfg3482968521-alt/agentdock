package app

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed d2contract/*.schema.json
var d2AgentContractSchemas embed.FS

var d2AgentSchemaFiles = map[string]string{
	"agent_start":  "d2contract/agent-start.schema.json",
	"agent_status": "d2contract/agent-status.schema.json",
	"agent_output": "d2contract/agent-output.schema.json",
	"agent_cancel": "d2contract/agent-cancel.schema.json",
	"agent_result": "d2contract/agent-result.schema.json",
	"agent_list":   "d2contract/agent-list.schema.json",
}

var d2AgentAllSchemaFiles = []string{
	"d2contract/agent-start.schema.json",
	"d2contract/agent-status.schema.json",
	"d2contract/agent-output.schema.json",
	"d2contract/agent-cancel.schema.json",
	"d2contract/agent-result.schema.json",
	"d2contract/agent-list.schema.json",
	"d2contract/agent.schema.json",
	"d2contract/agent-budget.schema.json",
	"d2contract/agent-permission.schema.json",
	"d2contract/credential-ref.schema.json",
	"d2contract/agent-error.schema.json",
}

func frozenAgentInputSchema(name string) map[string]any {
	file, ok := d2AgentSchemaFiles[name]
	if !ok {
		panic(fmt.Sprintf("unknown D2 agent schema %q", name))
	}
	data, err := d2AgentContractSchemas.ReadFile(file)
	if err != nil {
		panic(fmt.Sprintf("read D2 agent schema %s: %v", file, err))
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		panic(fmt.Sprintf("parse D2 agent schema %s: %v", file, err))
	}
	return schema
}

func frozenAgentOutputSchema(name string) map[string]any {
	schema := frozenAgentInputSchema(name)
	output, ok := schema["x-success-output"].(map[string]any)
	if !ok {
		panic(fmt.Sprintf("D2 agent schema %q has no x-success-output", name))
	}
	return output
}

type d2AgentSchemaValidatorSet struct {
	byTool       map[string]*jsonschema.Schema
	outputByTool map[string]*jsonschema.Schema
	err          error
}

var d2AgentValidators = sync.OnceValue(func() d2AgentSchemaValidatorSet {
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	ids := make(map[string]string, len(d2AgentAllSchemaFiles))
	for _, file := range d2AgentAllSchemaFiles {
		data, err := d2AgentContractSchemas.ReadFile(file)
		if err != nil {
			return d2AgentSchemaValidatorSet{err: fmt.Errorf("read frozen schema %s: %w", file, err)}
		}
		var schema map[string]any
		if err := json.Unmarshal(data, &schema); err != nil {
			return d2AgentSchemaValidatorSet{err: fmt.Errorf("parse frozen schema %s: %w", file, err)}
		}
		id, _ := schema["$id"].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			return d2AgentSchemaValidatorSet{err: fmt.Errorf("frozen schema %s has no $id", file)}
		}
		if err := compiler.AddResource(id, schema); err != nil {
			return d2AgentSchemaValidatorSet{err: fmt.Errorf("load frozen schema %s: %w", file, err)}
		}
		ids[file] = id
	}
	validators := make(map[string]*jsonschema.Schema, len(d2AgentSchemaFiles))
	outputValidators := make(map[string]*jsonschema.Schema, len(d2AgentSchemaFiles))
	for tool, file := range d2AgentSchemaFiles {
		compiled, err := compiler.Compile(ids[file])
		if err != nil {
			return d2AgentSchemaValidatorSet{err: fmt.Errorf("compile frozen schema %s: %w", file, err)}
		}
		outputCompiled, err := compiler.Compile(ids[file] + "#/x-success-output")
		if err != nil {
			return d2AgentSchemaValidatorSet{err: fmt.Errorf("compile frozen output schema %s: %w", file, err)}
		}
		validators[tool] = compiled
		outputValidators[tool] = outputCompiled
	}
	return d2AgentSchemaValidatorSet{byTool: validators, outputByTool: outputValidators}
})

func validateFrozenAgentInput(name string, args map[string]any) error {
	set := d2AgentValidators()
	if set.err != nil {
		return toolErrorCause("INVALID_ARGUMENTS", "D2 Agent schema validation is unavailable", "validation", nil, set.err)
	}
	validator := set.byTool[name]
	if validator == nil {
		return toolErrorDetails("INVALID_ARGUMENTS", "unknown D2 Agent tool schema", "validation", map[string]any{"tool": name})
	}
	if args == nil {
		args = map[string]any{}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return toolErrorCause("INVALID_ARGUMENTS", "Agent tool arguments are not valid JSON", "validation", nil, err)
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return toolErrorCause("INVALID_ARGUMENTS", "Agent tool arguments are not valid JSON", "validation", nil, err)
	}
	if err := validator.Validate(normalized); err != nil {
		// Do not surface validator diagnostics here: some implementations include
		// rejected instance values. The stable public contract needs only the code
		// and tool name, never caller-supplied secret-bearing values.
		return toolErrorDetails("INVALID_ARGUMENTS", "Agent tool arguments do not match the frozen D2 contract", "validation", map[string]any{"tool": name})
	}
	return nil
}

func validateFrozenAgentOutput(name string, value any) error {
	set := d2AgentValidators()
	if set.err != nil {
		return set.err
	}
	validator := set.outputByTool[name]
	if validator == nil {
		return fmt.Errorf("unknown D2 Agent output schema %q", name)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return err
	}
	return validator.Validate(normalized)
}

func compactAgentSchemaError(err error) string {
	message := strings.Join(strings.Fields(err.Error()), " ")
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}

func sortedD2AgentToolNames() []string {
	names := make([]string, 0, len(d2AgentSchemaFiles))
	for name := range d2AgentSchemaFiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
