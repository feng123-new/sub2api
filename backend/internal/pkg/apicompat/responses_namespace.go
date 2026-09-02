package apicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ResponsesNamespaceName identifies a function child in a Responses namespace.
// It aliases the chat bridge mapping so both native and bridged paths share one
// namespace identity contract.
type ResponsesNamespaceName = NamespacedToolName

// FlattenResponsesNamespaces converts Codex private namespace declarations into
// public Responses function tools and rewrites namespace-qualified request calls.
func FlattenResponsesNamespaces(req map[string]any) (map[string]ResponsesNamespaceName, bool, error) {
	return FlattenResponsesNamespacesExcept(req, nil)
}

// FlattenResponsesNamespacesExcept is FlattenResponsesNamespaces with a set of
// service-owned namespace names that must remain native in the request.
func FlattenResponsesNamespacesExcept(req map[string]any, preserved map[string]bool) (map[string]ResponsesNamespaceName, bool, error) {
	if req == nil {
		return nil, false, nil
	}
	tools, _ := req["tools"].([]any)
	declarationLists := responsesNamespaceDeclarationLists(req, tools)
	if len(declarationLists) == 0 {
		return nil, false, nil
	}

	topLevel := make(map[string]bool)
	for _, declarations := range declarationLists {
		for _, raw := range declarations {
			tool, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			typ := strings.TrimSpace(stringValue(tool["type"]))
			name := strings.TrimSpace(stringValue(tool["name"]))
			if (typ == "function" || typ == "custom") && name != "" {
				topLevel[name] = true
			}
		}
	}

	names := make(map[string]ResponsesNamespaceName)
	topLevelNames := make(map[string]ResponsesNamespaceName)
	topLevelNamespaces := make(map[string]bool)
	for declarationIndex, declarations := range declarationLists {
		fromTopLevel := len(tools) > 0 && declarationIndex == 0
		for _, raw := range declarations {
			tool, ok := raw.(map[string]any)
			if !ok || strings.TrimSpace(stringValue(tool["type"])) != "namespace" {
				continue
			}
			namespace := strings.TrimSpace(stringValue(tool["name"]))
			if namespace == "" || preserved[namespace] {
				continue
			}
			if fromTopLevel {
				topLevelNamespaces[namespace] = true
			}
			for _, rawChild := range namespaceChildren(tool) {
				child, ok := rawChild.(map[string]any)
				if !ok || strings.TrimSpace(stringValue(child["type"])) != "function" {
					continue
				}
				name := strings.TrimSpace(stringValue(child["name"]))
				if name == "" {
					continue
				}
				flat := flattenNamespaceToolName(namespace, name)
				entry := ResponsesNamespaceName{Namespace: namespace, Name: name}
				if topLevel[flat] {
					return nil, false, fmt.Errorf("namespace tool %q/%q flattens to %q which conflicts with a top-level tool of the same name; this upstream cannot disambiguate them, rename one of the tools", namespace, name, flat)
				}
				if prev, exists := names[flat]; exists && prev != entry {
					return nil, false, fmt.Errorf("namespace tools %q/%q and %q/%q both flatten to %q; this upstream cannot disambiguate them, rename one of the tools", prev.Namespace, prev.Name, namespace, name, flat)
				}
				names[flat] = entry
				if fromTopLevel {
					topLevelNames[flat] = entry
				}
			}
		}
	}
	if len(names) == 0 {
		return nil, false, nil
	}

	if len(tools) > 0 {
		flattened := make([]any, 0, len(tools)+len(names))
		seen := make(map[string]bool)
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok || strings.TrimSpace(stringValue(tool["type"])) != "namespace" {
				flattened = append(flattened, raw)
				continue
			}
			namespace := strings.TrimSpace(stringValue(tool["name"]))
			if preserved[namespace] {
				flattened = append(flattened, raw)
				continue
			}
			for _, rawChild := range namespaceChildren(tool) {
				child, ok := rawChild.(map[string]any)
				if !ok || strings.TrimSpace(stringValue(child["type"])) != "function" {
					continue
				}
				name := strings.TrimSpace(stringValue(child["name"]))
				flat := flattenNamespaceToolName(namespace, name)
				if name == "" || seen[flat] {
					continue
				}
				seen[flat] = true
				flatChild := make(map[string]any, len(child))
				for key, value := range child {
					flatChild[key] = value
				}
				flatChild["name"] = flat
				flattened = append(flattened, flatChild)
			}
		}
		req["tools"] = flattened
	}
	rewriteNamespaceQualifiedCalls(req["input"], names)
	if choice, ok := req["tool_choice"].(map[string]any); ok {
		choiceNamespace := strings.TrimSpace(stringValue(choice["name"]))
		if strings.TrimSpace(stringValue(choice["type"])) == "namespace" && topLevelNamespaces[choiceNamespace] {
			req["tool_choice"] = "auto"
		} else {
			rewriteNamespaceQualifiedCall(choice, topLevelNames)
		}
	}
	return names, true, nil
}

func responsesNamespaceDeclarationLists(req map[string]any, topLevel []any) [][]any {
	lists := make([][]any, 0, 1)
	if len(topLevel) > 0 {
		lists = append(lists, topLevel)
	}
	input, _ := req["input"].([]any)
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok || strings.TrimSpace(stringValue(item["type"])) != "additional_tools" {
			continue
		}
		tools, _ := item["tools"].([]any)
		if len(tools) > 0 {
			lists = append(lists, tools)
		}
	}
	return lists
}

// RestoreResponsesNamespaceCalls restores flattened function calls in a JSON
// Responses payload to the namespace/name identity expected by Codex.
func RestoreResponsesNamespaceCalls(payload []byte, names map[string]ResponsesNamespaceName) ([]byte, bool, error) {
	if len(payload) == 0 || len(names) == 0 {
		return payload, false, nil
	}
	value, err := decodeResponsesNamespaceJSONValue(payload)
	if err != nil {
		return payload, false, err
	}
	changed := restoreResponsesNamespaceValue(value, names)
	if !changed {
		return payload, false, nil
	}
	var rebuilt bytes.Buffer
	encoder := json.NewEncoder(&rebuilt)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return payload, false, err
	}
	return bytes.TrimSuffix(rebuilt.Bytes(), []byte("\n")), true, nil
}

func decodeResponsesNamespaceJSONValue(payload []byte) (any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("unexpected trailing JSON value")
	}
	return value, nil
}

func namespaceChildren(tool map[string]any) []any {
	if children, ok := tool["tools"].([]any); ok && len(children) > 0 {
		return children
	}
	children, _ := tool["children"].([]any)
	return children
}

func rewriteNamespaceQualifiedCalls(value any, names map[string]ResponsesNamespaceName) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			rewriteNamespaceQualifiedCalls(item, names)
		}
	case map[string]any:
		itemType := strings.TrimSpace(stringValue(typed["type"]))
		if itemType == "function_call" || itemType == "function_call_output" {
			rewriteNamespaceQualifiedCall(typed, names)
		}
		for _, child := range typed {
			rewriteNamespaceQualifiedCalls(child, names)
		}
	}
}

func rewriteNamespaceQualifiedCall(item map[string]any, names map[string]ResponsesNamespaceName) bool {
	namespace := strings.TrimSpace(stringValue(item["namespace"]))
	name := strings.TrimSpace(stringValue(item["name"]))
	if namespace == "" || name == "" {
		return false
	}
	flat := flattenNamespaceToolName(namespace, name)
	entry, ok := names[flat]
	if !ok || entry.Namespace != namespace || entry.Name != name {
		return false
	}
	item["name"] = flat
	delete(item, "namespace")
	return true
}

func restoreResponsesNamespaceValue(value any, names map[string]ResponsesNamespaceName) bool {
	changed := false
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			changed = restoreResponsesNamespaceValue(item, names) || changed
		}
	case map[string]any:
		if strings.TrimSpace(stringValue(typed["type"])) == "function_call" {
			if entry, ok := names[strings.TrimSpace(stringValue(typed["name"]))]; ok {
				typed["name"] = entry.Name
				typed["namespace"] = entry.Namespace
				changed = true
			}
		}
		for _, child := range typed {
			changed = restoreResponsesNamespaceValue(child, names) || changed
		}
	}
	return changed
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
