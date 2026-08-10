package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tiktoken-go/tokenizer"
)

const (
	openAICompleteTextChunkBytes            = 8 << 10
	openAICompleteRequestBodyMaxBytes       = 8 << 20
	openAICompleteTextQuadraticBudget       = uint64(16 << 20)
	openAICompleteTextQuadraticRunThreshold = 256
	openAICompleteTextMaxNonWhitespaceBytes = 4 << 10
)

var (
	errOpenAIInputTokensUnsupportedShape = errors.New("unsupported OpenAI input shape")
	errOpenAIInputTokensInvalidUTF8      = errors.New("OpenAI input text is not valid UTF-8")
	errOpenAIInputTokensSegmentTooLong   = errors.New("OpenAI input text contains an oversized non-whitespace segment")
	errOpenAIInputTokensDeadlineExceeded = errors.New("OpenAI input token estimate exceeded its work deadline")
)

type openAICompleteWorkBudget struct {
	now      func() time.Time
	deadline time.Time
}

func (b *openAICompleteWorkBudget) check() error {
	if b == nil || b.now == nil || b.deadline.IsZero() {
		return nil
	}
	if !b.now().Before(b.deadline) {
		return errOpenAIInputTokensDeadlineExceeded
	}
	return nil
}

type openAICompleteTokenCounter struct {
	codec               tokenizer.Codec
	total               int
	quadraticBudgetUsed uint64
	budget              *openAICompleteWorkBudget
	countMemo           map[string]int
}

func (c *openAICompleteTokenCounter) add(text string) error {
	if text == "" {
		return c.budget.check()
	}
	if c.countMemo == nil {
		c.countMemo = make(map[string]int)
	}
	count, quadraticBudgetUsed, err := countOpenAICompleteText(c.codec, text, c.quadraticBudgetUsed, c.budget, c.countMemo)
	if err != nil {
		return err
	}
	c.quadraticBudgetUsed = quadraticBudgetUsed
	c.total += count
	return nil
}

func countOpenAICompleteText(codec tokenizer.Codec, text string, quadraticBudgetUsed uint64, budget *openAICompleteWorkBudget, countMemo map[string]int) (int, uint64, error) {
	if err := budget.check(); err != nil {
		return 0, quadraticBudgetUsed, err
	}
	if text == "" {
		return 0, quadraticBudgetUsed, nil
	}

	contiguousBytes := 0
	nextDeadlineCheck := openAICompleteTextChunkBytes
	for offset := 0; offset < len(text); {
		currentRune, size := utf8.DecodeRuneInString(text[offset:])
		if currentRune == utf8.RuneError && size == 1 {
			return 0, quadraticBudgetUsed, errOpenAIInputTokensInvalidUTF8
		}
		if unicode.IsSpace(currentRune) {
			var err error
			quadraticBudgetUsed, err = addOpenAICompleteTextRunCost(quadraticBudgetUsed, contiguousBytes)
			if err != nil {
				return 0, quadraticBudgetUsed, err
			}
			contiguousBytes = 0
		} else {
			contiguousBytes += size
			if contiguousBytes > openAICompleteTextMaxNonWhitespaceBytes {
				return 0, quadraticBudgetUsed, errOpenAIInputTokensSegmentTooLong
			}
		}
		offset += size
		if offset >= nextDeadlineCheck {
			if err := budget.check(); err != nil {
				return 0, quadraticBudgetUsed, err
			}
			for nextDeadlineCheck <= offset {
				nextDeadlineCheck += openAICompleteTextChunkBytes
			}
		}
	}
	var err error
	quadraticBudgetUsed, err = addOpenAICompleteTextRunCost(quadraticBudgetUsed, contiguousBytes)
	if err != nil {
		return 0, quadraticBudgetUsed, err
	}
	if err := budget.check(); err != nil {
		return 0, quadraticBudgetUsed, err
	}

	total := 0
	for start := 0; start < len(text); {
		end := start + openAICompleteTextChunkBytes
		if end >= len(text) {
			end = len(text)
		} else {
			for end > start && !utf8.RuneStart(text[end]) {
				end--
			}
			if whitespaceEnd := preferredOpenAICompleteTextWhitespaceEnd(text, start, end); whitespaceEnd > start {
				end = whitespaceEnd
			}
		}
		if err := budget.check(); err != nil {
			return 0, quadraticBudgetUsed, err
		}
		chunk := text[start:end]
		count, cached := countMemo[chunk]
		if !cached {
			count, err = codec.Count(chunk)
			if err != nil {
				return 0, quadraticBudgetUsed, err
			}
			if err := budget.check(); err != nil {
				return 0, quadraticBudgetUsed, err
			}
			if countMemo != nil {
				countMemo[chunk] = count
			}
		} else if err := budget.check(); err != nil {
			return 0, quadraticBudgetUsed, err
		}
		total += count
		start = end
	}
	return total, quadraticBudgetUsed, nil
}

func addOpenAICompleteTextRunCost(quadraticBudgetUsed uint64, runBytes int) (uint64, error) {
	if runBytes <= openAICompleteTextQuadraticRunThreshold {
		return quadraticBudgetUsed, nil
	}
	run := uint64(runBytes)
	if run > openAICompleteTextQuadraticBudget/run {
		return quadraticBudgetUsed, errOpenAIInputTokensSegmentTooLong
	}
	runCost := run * run
	if quadraticBudgetUsed > openAICompleteTextQuadraticBudget || runCost > openAICompleteTextQuadraticBudget-quadraticBudgetUsed {
		return quadraticBudgetUsed, errOpenAIInputTokensSegmentTooLong
	}
	return quadraticBudgetUsed + runCost, nil
}

func preferredOpenAICompleteTextWhitespaceEnd(text string, start, end int) int {
	for boundary := end; boundary > start; {
		currentRune, size := utf8.DecodeLastRuneInString(text[start:boundary])
		if unicode.IsSpace(currentRune) {
			return boundary
		}
		boundary -= size
	}
	return 0
}

func (c *openAICompleteTokenCounter) addJSON(raw json.RawMessage) error {
	compacted, err := compactOpenAIInputTokensJSON(raw)
	if err != nil {
		return err
	}
	if err := c.budget.check(); err != nil {
		return err
	}
	return c.add(compacted)
}

func (c *openAICompleteTokenCounter) unmarshalJSON(raw json.RawMessage, value any) error {
	if err := json.Unmarshal(raw, value); err != nil {
		if budgetErr := c.budget.check(); budgetErr != nil {
			return budgetErr
		}
		return errOpenAIInputTokensUnsupportedShape
	}
	return c.budget.check()
}

func (c *openAICompleteTokenCounter) decodeJSONString(raw json.RawMessage) (string, bool, error) {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", false, c.budget.check()
	}
	nextDeadlineCheck := openAICompleteTextChunkBytes
	for index := 1; index < len(raw)-1; index++ {
		if raw[index] == '\\' {
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				if budgetErr := c.budget.check(); budgetErr != nil {
					return "", false, budgetErr
				}
				return "", false, nil
			}
			if err := c.budget.check(); err != nil {
				return "", false, err
			}
			return value, true, nil
		}
		if index >= nextDeadlineCheck {
			if err := c.budget.check(); err != nil {
				return "", false, err
			}
			nextDeadlineCheck += openAICompleteTextChunkBytes
		}
	}
	if err := c.budget.check(); err != nil {
		return "", false, err
	}
	return string(raw[1 : len(raw)-1]), true, nil
}

func (c *openAICompleteTokenCounter) decodeJSONObject(raw json.RawMessage) (map[string]json.RawMessage, bool, error) {
	if len(raw) == 0 {
		return nil, false, c.budget.check()
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		if budgetErr := c.budget.check(); budgetErr != nil {
			return nil, false, budgetErr
		}
		return nil, false, nil
	}
	if err := c.budget.check(); err != nil {
		return nil, false, err
	}
	return object, true, nil
}

func estimateOpenAIInputTokensComplete(body []byte, endpoint openAIContextPreflightEndpoint, model string) (int, bool, openAIContextPreflightSkipReason) {
	if len(body) > openAICompleteRequestBodyMaxBytes || !json.Valid(body) {
		return 0, false, openAIContextPreflightSkipCountFailed
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil || request == nil {
		return 0, false, openAIContextPreflightSkipUnsupportedShape
	}
	return estimateOpenAIInputTokensCompleteBudgeted(request, endpoint, model, nil)
}

func estimateOpenAIInputTokensCompleteBudgeted(request map[string]json.RawMessage, endpoint openAIContextPreflightEndpoint, model string, budget *openAICompleteWorkBudget) (int, bool, openAIContextPreflightSkipReason) {
	if err := budget.check(); err != nil {
		return 0, false, openAIContextPreflightSkipCountFailed
	}
	encoding, ok := strictOpenAIInputTokensEncodingForModel(model)
	if !ok {
		return 0, false, openAIContextPreflightSkipUnsupportedEncoding
	}
	codec, err := tokenizer.Get(encoding)
	if err != nil {
		return 0, false, openAIContextPreflightSkipCountFailed
	}
	if err := budget.check(); err != nil {
		return 0, false, openAIContextPreflightSkipCountFailed
	}
	counter := &openAICompleteTokenCounter{codec: codec, budget: budget, countMemo: make(map[string]int)}
	switch endpoint {
	case openAIContextPreflightEndpointResponses:
		err = countCompleteOpenAIResponsesRequest(counter, request)
	case openAIContextPreflightEndpointChatCompletions:
		err = countCompleteOpenAIChatRequest(counter, request)
	default:
		err = errOpenAIInputTokensUnsupportedShape
	}
	if err != nil {
		if errors.Is(err, errOpenAIInputTokensUnsupportedShape) {
			return 0, false, openAIContextPreflightSkipUnsupportedShape
		}
		return 0, false, openAIContextPreflightSkipCountFailed
	}
	if err := budget.check(); err != nil {
		return 0, false, openAIContextPreflightSkipCountFailed
	}
	return counter.total, true, ""
}

func strictOpenAIInputTokensEncodingForModel(model string) (tokenizer.Encoding, bool) {
	normalized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case normalized == "gpt-5",
		strings.HasPrefix(normalized, "gpt-5-"),
		strings.HasPrefix(normalized, "gpt-5."),
		normalized == "gpt-4o",
		strings.HasPrefix(normalized, "gpt-4o-"),
		normalized == "gpt-4.1",
		strings.HasPrefix(normalized, "gpt-4.1-"),
		strings.HasPrefix(normalized, "chatgpt-4o-"),
		isStrictOpenAIReasoningModel(normalized, "o1"),
		isStrictOpenAIReasoningModel(normalized, "o3"),
		isStrictOpenAIReasoningModel(normalized, "o4"):
		return tokenizer.O200kBase, true
	case normalized == "gpt-4",
		strings.HasPrefix(normalized, "gpt-4-"),
		normalized == "gpt-3.5",
		strings.HasPrefix(normalized, "gpt-3.5-"),
		normalized == "gpt-35",
		strings.HasPrefix(normalized, "gpt-35-"),
		strings.HasPrefix(normalized, "text-embedding-"):
		return tokenizer.Cl100kBase, true
	default:
		return "", false
	}
}

func warmOpenAIContextPreflightCodecs(models map[string]struct{}) {
	encodings := make(map[tokenizer.Encoding]struct{})
	for model := range models {
		if encoding, ok := strictOpenAIInputTokensEncodingForModel(model); ok {
			encodings[encoding] = struct{}{}
		}
	}
	for encoding := range encodings {
		_, _ = tokenizer.Get(encoding)
	}
}

func isStrictOpenAIReasoningModel(model, family string) bool {
	return model == family || strings.HasPrefix(model, family+"-")
}

func countCompleteOpenAIResponsesRequest(counter *openAICompleteTokenCounter, request map[string]json.RawMessage) error {
	for key := range request {
		if !isSupportedOpenAIResponsesTopLevelField(key) {
			return errOpenAIInputTokensUnsupportedShape
		}
	}
	if raw, ok := request["instructions"]; ok && !isOpenAIJSONNull(raw) {
		instructions, valid, err := counter.decodeJSONString(raw)
		if err != nil {
			return err
		}
		if !valid {
			return errOpenAIInputTokensUnsupportedShape
		}
		if err := counter.add(instructions); err != nil {
			return err
		}
	}
	if raw, ok := request["input"]; ok && !isOpenAIJSONNull(raw) {
		if err := countCompleteOpenAIResponsesInput(counter, raw); err != nil {
			return err
		}
	}
	if raw, ok := request["tools"]; ok && !isOpenAIJSONNull(raw) {
		if err := countCompleteOpenAIResponsesTools(counter, raw); err != nil {
			return err
		}
	}
	if raw, ok := request["tool_choice"]; ok && !isOpenAIJSONNull(raw) {
		if err := countCompleteOpenAIResponsesToolChoice(counter, raw); err != nil {
			return err
		}
	}
	if raw, ok := request["text"]; ok && !isOpenAIJSONNull(raw) {
		if err := validateOpenAIResponsesTextConfig(counter, raw); err != nil {
			return err
		}
	}
	return nil
}

func isSupportedOpenAIResponsesTopLevelField(key string) bool {
	switch key {
	case "model", "instructions", "input", "tools", "tool_choice", "text",
		"background", "include", "max_output_tokens", "max_tool_calls", "metadata",
		"parallel_tool_calls", "reasoning", "safety_identifier", "service_tier", "store",
		"stream", "stream_options", "temperature", "top_logprobs", "top_p", "truncation",
		"user", "prompt_cache_key", "prompt_cache_retention", "prompt_cache_options",
		"type", "client_metadata", "generate":
		return true
	default:
		return false
	}
}

func countCompleteOpenAIResponsesInput(counter *openAICompleteTokenCounter, raw json.RawMessage) error {
	text, ok, err := counter.decodeJSONString(raw)
	if err != nil {
		return err
	}
	if ok {
		return counter.add(text)
	}
	var items []json.RawMessage
	if err := counter.unmarshalJSON(raw, &items); err != nil {
		return err
	}
	for _, itemRaw := range items {
		item, valid, err := counter.decodeJSONObject(itemRaw)
		if err != nil {
			return err
		}
		if !valid {
			return errOpenAIInputTokensUnsupportedShape
		}
		itemType := "message"
		if typeRaw, exists := item["type"]; exists {
			value, valid, err := counter.decodeJSONString(typeRaw)
			if err != nil {
				return err
			}
			if !valid {
				return errOpenAIInputTokensUnsupportedShape
			}
			itemType = value
		}
		counter.total += openAIResponsesInputItemTokenOverhead
		switch itemType {
		case "message":
			if err := countCompleteOpenAIResponsesMessage(counter, item); err != nil {
				return err
			}
		case "function_call":
			if err := countCompleteOpenAIResponsesFunctionCall(counter, item); err != nil {
				return err
			}
		case "function_call_output":
			if err := countCompleteOpenAIResponsesFunctionOutput(counter, item); err != nil {
				return err
			}
		case "additional_tools":
			if err := countCompleteOpenAIResponsesAdditionalTools(counter, itemRaw, item); err != nil {
				return err
			}
		default:
			return errOpenAIInputTokensUnsupportedShape
		}
	}
	return nil
}

func countCompleteOpenAIResponsesMessage(counter *openAICompleteTokenCounter, item map[string]json.RawMessage) error {
	if !openAIJSONFieldsAllowed(item, "type", "role", "content", "id", "status") {
		return errOpenAIInputTokensUnsupportedShape
	}
	role, ok, err := counter.decodeJSONString(item["role"])
	if err != nil {
		return err
	}
	if !ok || !isSupportedOpenAIMessageRole(role) {
		return errOpenAIInputTokensUnsupportedShape
	}
	if err := counter.add(role); err != nil {
		return err
	}
	if raw, exists := item["id"]; exists {
		id, valid, err := counter.decodeJSONString(raw)
		if err != nil {
			return err
		}
		if !valid {
			return errOpenAIInputTokensUnsupportedShape
		}
		if err := counter.add(id); err != nil {
			return err
		}
	}
	if raw, exists := item["status"]; exists {
		if _, valid, err := counter.decodeJSONString(raw); err != nil {
			return err
		} else if !valid {
			return errOpenAIInputTokensUnsupportedShape
		}
	}
	if raw, exists := item["content"]; exists && !isOpenAIJSONNull(raw) {
		return countCompleteOpenAITextContent(counter, raw, "input_text", "output_text", "text")
	}
	return nil
}

func countCompleteOpenAIResponsesFunctionCall(counter *openAICompleteTokenCounter, item map[string]json.RawMessage) error {
	if !openAIJSONFieldsAllowed(item, "type", "id", "call_id", "name", "arguments", "status") {
		return errOpenAIInputTokensUnsupportedShape
	}
	for _, key := range []string{"call_id", "name", "arguments"} {
		raw, exists := item[key]
		value, ok, err := counter.decodeJSONString(raw)
		if err != nil {
			return err
		}
		if !exists || !ok || (key != "arguments" && strings.TrimSpace(value) == "") {
			return errOpenAIInputTokensUnsupportedShape
		}
	}
	for _, key := range []string{"type", "id", "call_id", "name", "arguments"} {
		if raw, exists := item[key]; exists {
			value, ok, err := counter.decodeJSONString(raw)
			if err != nil {
				return err
			}
			if !ok {
				return errOpenAIInputTokensUnsupportedShape
			}
			if err := counter.add(value); err != nil {
				return err
			}
		}
	}
	if raw, exists := item["status"]; exists {
		if _, ok, err := counter.decodeJSONString(raw); err != nil {
			return err
		} else if !ok {
			return errOpenAIInputTokensUnsupportedShape
		}
	}
	return nil
}

func countCompleteOpenAIResponsesFunctionOutput(counter *openAICompleteTokenCounter, item map[string]json.RawMessage) error {
	if !openAIJSONFieldsAllowed(item, "type", "id", "call_id", "name", "output", "status") {
		return errOpenAIInputTokensUnsupportedShape
	}
	callID, callIDExists := item["call_id"]
	callIDValue, callIDValid, err := counter.decodeJSONString(callID)
	if err != nil {
		return err
	}
	if !callIDExists || !callIDValid || strings.TrimSpace(callIDValue) == "" {
		return errOpenAIInputTokensUnsupportedShape
	}
	if _, outputExists := item["output"]; !outputExists {
		return errOpenAIInputTokensUnsupportedShape
	}
	for _, key := range []string{"type", "id", "call_id", "name"} {
		if raw, exists := item[key]; exists {
			value, ok, err := counter.decodeJSONString(raw)
			if err != nil {
				return err
			}
			if !ok {
				return errOpenAIInputTokensUnsupportedShape
			}
			if err := counter.add(value); err != nil {
				return err
			}
		}
	}
	if output, exists := item["output"]; exists {
		text, ok, err := counter.decodeJSONString(output)
		if err != nil {
			return err
		}
		if ok {
			if err := counter.add(text); err != nil {
				return err
			}
		} else if err := countCompleteOpenAITextContent(counter, output, "input_text", "output_text", "text"); err != nil {
			return err
		}
	}
	if raw, exists := item["status"]; exists {
		if _, ok, err := counter.decodeJSONString(raw); err != nil {
			return err
		} else if !ok {
			return errOpenAIInputTokensUnsupportedShape
		}
	}
	return nil
}

// Responses Lite carries tool catalogs inside input.additional_tools. Once the
// carrier shape is validated, count its compact canonical JSON so catalog size
// contributes deterministically while all surrounding history is counted by the
// normal input-item paths.
func countCompleteOpenAIResponsesAdditionalTools(counter *openAICompleteTokenCounter, raw json.RawMessage, item map[string]json.RawMessage) error {
	if !openAIJSONFieldsAllowed(item, "type", "role", "tools") {
		return errOpenAIInputTokensUnsupportedShape
	}
	if roleRaw, exists := item["role"]; exists {
		role, ok, err := counter.decodeJSONString(roleRaw)
		if err != nil {
			return err
		}
		if !ok || role != "developer" {
			return errOpenAIInputTokensUnsupportedShape
		}
	}
	toolsRaw, exists := item["tools"]
	if !exists || isOpenAIJSONNull(toolsRaw) {
		return errOpenAIInputTokensUnsupportedShape
	}
	var tools []json.RawMessage
	if err := counter.unmarshalJSON(toolsRaw, &tools); err != nil {
		return err
	}
	for _, toolRaw := range tools {
		if err := validateOpenAIResponsesLiteAdditionalTool(counter, toolRaw); err != nil {
			return err
		}
	}
	return counter.addJSON(raw)
}

func validateOpenAIResponsesLiteAdditionalTool(counter *openAICompleteTokenCounter, raw json.RawMessage) error {
	if shorthand, ok, err := counter.decodeJSONString(raw); err != nil {
		return err
	} else if ok {
		if strings.TrimSpace(shorthand) == "" {
			return errOpenAIInputTokensUnsupportedShape
		}
		return nil
	}
	tool, ok, err := counter.decodeJSONObject(raw)
	if err != nil {
		return err
	}
	if !ok {
		return errOpenAIInputTokensUnsupportedShape
	}
	toolType, ok, err := counter.decodeJSONString(tool["type"])
	if err != nil {
		return err
	}
	if !ok {
		return errOpenAIInputTokensUnsupportedShape
	}
	switch strings.TrimSpace(toolType) {
	case "function", "custom", "tool_search", "namespace":
		return nil
	default:
		return errOpenAIInputTokensUnsupportedShape
	}
}

func countCompleteOpenAITextContent(counter *openAICompleteTokenCounter, raw json.RawMessage, supportedTypes ...string) error {
	text, ok, err := counter.decodeJSONString(raw)
	if err != nil {
		return err
	}
	if ok {
		return counter.add(text)
	}
	var parts []json.RawMessage
	if err := counter.unmarshalJSON(raw, &parts); err != nil {
		return err
	}
	supported := make(map[string]struct{}, len(supportedTypes))
	for _, partType := range supportedTypes {
		supported[partType] = struct{}{}
	}
	for _, partRaw := range parts {
		part, ok, err := counter.decodeJSONObject(partRaw)
		if err != nil {
			return err
		}
		if !ok || !openAIJSONFieldsAllowed(part, "type", "text") {
			return errOpenAIInputTokensUnsupportedShape
		}
		partType, ok, err := counter.decodeJSONString(part["type"])
		if err != nil {
			return err
		}
		if !ok {
			return errOpenAIInputTokensUnsupportedShape
		}
		if _, ok := supported[partType]; !ok {
			return errOpenAIInputTokensUnsupportedShape
		}
		text, ok, err := counter.decodeJSONString(part["text"])
		if err != nil {
			return err
		}
		if !ok {
			return errOpenAIInputTokensUnsupportedShape
		}
		counter.total += openAIResponsesContentPartOverhead
		if err := counter.add(text); err != nil {
			return err
		}
	}
	return nil
}

func countCompleteOpenAIResponsesTools(counter *openAICompleteTokenCounter, raw json.RawMessage) error {
	var tools []json.RawMessage
	if err := counter.unmarshalJSON(raw, &tools); err != nil {
		return err
	}
	for _, toolRaw := range tools {
		tool, ok, err := counter.decodeJSONObject(toolRaw)
		if err != nil {
			return err
		}
		if !ok || !openAIJSONFieldsAllowed(tool, "type", "name", "description", "parameters", "strict") {
			return errOpenAIInputTokensUnsupportedShape
		}
		toolType, ok, err := counter.decodeJSONString(tool["type"])
		if err != nil {
			return err
		}
		if !ok || toolType != "function" {
			return errOpenAIInputTokensUnsupportedShape
		}
		if err := validateOpenAIFunctionDefinition(counter, tool); err != nil {
			return err
		}
		if err := counter.addJSON(toolRaw); err != nil {
			return err
		}
	}
	return nil
}

func countCompleteOpenAIResponsesToolChoice(counter *openAICompleteTokenCounter, raw json.RawMessage) error {
	value, ok, err := counter.decodeJSONString(raw)
	if err != nil {
		return err
	}
	if ok {
		switch value {
		case "auto", "none", "required":
			return counter.add(value)
		default:
			return errOpenAIInputTokensUnsupportedShape
		}
	}
	choice, ok, err := counter.decodeJSONObject(raw)
	if err != nil {
		return err
	}
	if !ok || !openAIJSONFieldsAllowed(choice, "type", "name") {
		return errOpenAIInputTokensUnsupportedShape
	}
	choiceType, ok, err := counter.decodeJSONString(choice["type"])
	if err != nil {
		return err
	}
	if !ok || choiceType != "function" {
		return errOpenAIInputTokensUnsupportedShape
	}
	if _, ok, err = counter.decodeJSONString(choice["name"]); err != nil {
		return err
	} else if !ok {
		return errOpenAIInputTokensUnsupportedShape
	}
	return counter.addJSON(raw)
}

func validateOpenAIResponsesTextConfig(counter *openAICompleteTokenCounter, raw json.RawMessage) error {
	textConfig, ok, err := counter.decodeJSONObject(raw)
	if err != nil {
		return err
	}
	if !ok || !openAIJSONFieldsAllowed(textConfig, "format", "verbosity") {
		return errOpenAIInputTokensUnsupportedShape
	}
	if verbosity, exists := textConfig["verbosity"]; exists {
		if _, ok, err := counter.decodeJSONString(verbosity); err != nil {
			return err
		} else if !ok {
			return errOpenAIInputTokensUnsupportedShape
		}
	}
	formatRaw, exists := textConfig["format"]
	if !exists || isOpenAIJSONNull(formatRaw) {
		return nil
	}
	format, ok, err := counter.decodeJSONObject(formatRaw)
	if err != nil {
		return err
	}
	if !ok || !openAIJSONFieldsAllowed(format, "type") {
		return errOpenAIInputTokensUnsupportedShape
	}
	formatType, ok, err := counter.decodeJSONString(format["type"])
	if err != nil {
		return err
	}
	if !ok || formatType != "text" {
		return errOpenAIInputTokensUnsupportedShape
	}
	return nil
}

func countCompleteOpenAIChatRequest(counter *openAICompleteTokenCounter, request map[string]json.RawMessage) error {
	for key := range request {
		if !isSupportedOpenAIChatTopLevelField(key) {
			return errOpenAIInputTokensUnsupportedShape
		}
	}
	if raw, ok := request["messages"]; ok && !isOpenAIJSONNull(raw) {
		if err := countCompleteOpenAIChatMessages(counter, raw); err != nil {
			return err
		}
	}
	if raw, ok := request["tools"]; ok && !isOpenAIJSONNull(raw) {
		if err := countCompleteOpenAIChatTools(counter, raw); err != nil {
			return err
		}
	}
	if raw, ok := request["tool_choice"]; ok && !isOpenAIJSONNull(raw) {
		if err := countCompleteOpenAIChatToolChoice(counter, raw); err != nil {
			return err
		}
	}
	return nil
}

func isSupportedOpenAIChatTopLevelField(key string) bool {
	switch key {
	case "model", "messages", "tools", "tool_choice", "frequency_penalty", "logit_bias",
		"logprobs", "max_completion_tokens", "max_tokens", "metadata", "n", "parallel_tool_calls",
		"presence_penalty", "prompt_cache_key", "prompt_cache_retention", "reasoning_effort",
		"safety_identifier", "seed", "service_tier", "stop", "store", "stream", "stream_options",
		"temperature", "top_logprobs", "top_p", "user", "verbosity":
		return true
	default:
		return false
	}
}

func countCompleteOpenAIChatMessages(counter *openAICompleteTokenCounter, raw json.RawMessage) error {
	var messages []json.RawMessage
	if err := counter.unmarshalJSON(raw, &messages); err != nil {
		return err
	}
	for _, messageRaw := range messages {
		message, ok, err := counter.decodeJSONObject(messageRaw)
		if err != nil {
			return err
		}
		if !ok || !openAIJSONFieldsAllowed(message, "role", "content", "name", "tool_calls", "tool_call_id") {
			return errOpenAIInputTokensUnsupportedShape
		}
		role, ok, err := counter.decodeJSONString(message["role"])
		if err != nil {
			return err
		}
		if !ok || !isSupportedOpenAIMessageRole(role) {
			return errOpenAIInputTokensUnsupportedShape
		}
		counter.total += openAIResponsesInputItemTokenOverhead
		if err := counter.add(role); err != nil {
			return err
		}
		if nameRaw, exists := message["name"]; exists {
			name, ok, err := counter.decodeJSONString(nameRaw)
			if err != nil {
				return err
			}
			if !ok {
				return errOpenAIInputTokensUnsupportedShape
			}
			if err := counter.add(name); err != nil {
				return err
			}
		}
		if contentRaw, exists := message["content"]; exists && !isOpenAIJSONNull(contentRaw) {
			if err := countCompleteOpenAITextContent(counter, contentRaw, "text"); err != nil {
				return err
			}
		}
		if toolCallsRaw, exists := message["tool_calls"]; exists {
			if role != "assistant" || isOpenAIJSONNull(toolCallsRaw) {
				return errOpenAIInputTokensUnsupportedShape
			}
			if err := countCompleteOpenAIChatToolCalls(counter, toolCallsRaw); err != nil {
				return err
			}
		}
		if toolCallIDRaw, exists := message["tool_call_id"]; exists {
			if role != "tool" {
				return errOpenAIInputTokensUnsupportedShape
			}
			toolCallID, ok, err := counter.decodeJSONString(toolCallIDRaw)
			if err != nil {
				return err
			}
			if !ok {
				return errOpenAIInputTokensUnsupportedShape
			}
			if err := counter.add(toolCallID); err != nil {
				return err
			}
		}
	}
	return nil
}

func countCompleteOpenAIChatToolCalls(counter *openAICompleteTokenCounter, raw json.RawMessage) error {
	var calls []json.RawMessage
	if err := counter.unmarshalJSON(raw, &calls); err != nil {
		return err
	}
	for _, callRaw := range calls {
		call, ok, err := counter.decodeJSONObject(callRaw)
		if err != nil {
			return err
		}
		if !ok || !openAIJSONFieldsAllowed(call, "id", "type", "function") {
			return errOpenAIInputTokensUnsupportedShape
		}
		callType, ok, err := counter.decodeJSONString(call["type"])
		if err != nil {
			return err
		}
		if !ok || callType != "function" {
			return errOpenAIInputTokensUnsupportedShape
		}
		id, ok, err := counter.decodeJSONString(call["id"])
		if err != nil {
			return err
		}
		if !ok {
			return errOpenAIInputTokensUnsupportedShape
		}
		function, ok, err := counter.decodeJSONObject(call["function"])
		if err != nil {
			return err
		}
		if !ok || !openAIJSONFieldsAllowed(function, "name", "arguments") {
			return errOpenAIInputTokensUnsupportedShape
		}
		name, nameOK, err := counter.decodeJSONString(function["name"])
		if err != nil {
			return err
		}
		arguments, argumentsOK, err := counter.decodeJSONString(function["arguments"])
		if err != nil {
			return err
		}
		if !nameOK || !argumentsOK {
			return errOpenAIInputTokensUnsupportedShape
		}
		for _, value := range []string{id, callType, name, arguments} {
			if err := counter.add(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func countCompleteOpenAIChatTools(counter *openAICompleteTokenCounter, raw json.RawMessage) error {
	var tools []json.RawMessage
	if err := counter.unmarshalJSON(raw, &tools); err != nil {
		return err
	}
	for _, toolRaw := range tools {
		tool, ok, err := counter.decodeJSONObject(toolRaw)
		if err != nil {
			return err
		}
		if !ok || !openAIJSONFieldsAllowed(tool, "type", "function") {
			return errOpenAIInputTokensUnsupportedShape
		}
		toolType, ok, err := counter.decodeJSONString(tool["type"])
		if err != nil {
			return err
		}
		if !ok || toolType != "function" {
			return errOpenAIInputTokensUnsupportedShape
		}
		function, ok, err := counter.decodeJSONObject(tool["function"])
		if err != nil {
			return err
		}
		if !ok || !openAIJSONFieldsAllowed(function, "name", "description", "parameters", "strict") {
			return errOpenAIInputTokensUnsupportedShape
		}
		if err := validateOpenAIFunctionDefinition(counter, function); err != nil {
			return err
		}
		if err := counter.addJSON(toolRaw); err != nil {
			return err
		}
	}
	return nil
}

func countCompleteOpenAIChatToolChoice(counter *openAICompleteTokenCounter, raw json.RawMessage) error {
	value, ok, err := counter.decodeJSONString(raw)
	if err != nil {
		return err
	}
	if ok {
		switch value {
		case "auto", "none", "required":
			return counter.add(value)
		default:
			return errOpenAIInputTokensUnsupportedShape
		}
	}
	choice, ok, err := counter.decodeJSONObject(raw)
	if err != nil {
		return err
	}
	if !ok || !openAIJSONFieldsAllowed(choice, "type", "function") {
		return errOpenAIInputTokensUnsupportedShape
	}
	choiceType, ok, err := counter.decodeJSONString(choice["type"])
	if err != nil {
		return err
	}
	if !ok || choiceType != "function" {
		return errOpenAIInputTokensUnsupportedShape
	}
	function, ok, err := counter.decodeJSONObject(choice["function"])
	if err != nil {
		return err
	}
	if !ok || !openAIJSONFieldsAllowed(function, "name") {
		return errOpenAIInputTokensUnsupportedShape
	}
	if _, ok, err = counter.decodeJSONString(function["name"]); err != nil {
		return err
	} else if !ok {
		return errOpenAIInputTokensUnsupportedShape
	}
	return counter.addJSON(raw)
}

func validateOpenAIFunctionDefinition(counter *openAICompleteTokenCounter, function map[string]json.RawMessage) error {
	name, ok, err := counter.decodeJSONString(function["name"])
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(name) == "" {
		return errOpenAIInputTokensUnsupportedShape
	}
	if description, exists := function["description"]; exists {
		if _, ok, err := counter.decodeJSONString(description); err != nil {
			return err
		} else if !ok {
			return errOpenAIInputTokensUnsupportedShape
		}
	}
	if parameters, exists := function["parameters"]; exists && !isOpenAIJSONNull(parameters) {
		if _, ok, err := counter.decodeJSONObject(parameters); err != nil {
			return err
		} else if !ok {
			return errOpenAIInputTokensUnsupportedShape
		}
	}
	if strict, exists := function["strict"]; exists {
		var value bool
		if err := counter.unmarshalJSON(strict, &value); err != nil {
			return err
		}
	}
	return nil
}

func isSupportedOpenAIMessageRole(role string) bool {
	switch role {
	case "system", "developer", "user", "assistant", "tool":
		return true
	default:
		return false
	}
}

func openAIJSONFieldsAllowed(object map[string]json.RawMessage, allowed ...string) bool {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	for key := range object {
		if _, ok := allowedSet[key]; !ok {
			return false
		}
	}
	return true
}

func isOpenAIJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
