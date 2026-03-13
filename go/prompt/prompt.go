package prompt

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/openai/symphony/go/config"
	"github.com/openai/symphony/go/workflow"
)

// WorkflowUnavailableError reports workflow-loading failures separately from template parse errors.
type WorkflowUnavailableError struct {
	Reason error
}

func (e *WorkflowUnavailableError) Error() string {
	return fmt.Sprintf("workflow_unavailable: %v", e.Reason)
}

// ParseError reports invalid template syntax while preserving the offending template text.
type ParseError struct {
	Template string
	Reason   string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("template_parse_error: %s template=%q", e.Reason, e.Template)
}

// RenderError reports strict missing-variable failures during rendering.
type RenderError struct {
	Expression string
}

func (e *RenderError) Error() string {
	return fmt.Sprintf("template_render_error: undefined variable %q", e.Expression)
}

type node interface {
	render(map[string]any) (string, error)
}

type textNode struct {
	text string
}

func (n textNode) render(_ map[string]any) (string, error) {
	return n.text, nil
}

type variableNode struct {
	expression string
}

func (n variableNode) render(ctx map[string]any) (string, error) {
	value, err := resolveExpression(n.expression, ctx)
	if err != nil {
		return "", err
	}

	return stringifyValue(value), nil
}

type ifNode struct {
	condition string
	thenNodes []node
	elseNodes []node
}

func (n ifNode) render(ctx map[string]any) (string, error) {
	value, err := resolveExpression(n.condition, ctx)
	if err != nil {
		return "", err
	}

	nodes := n.elseNodes
	if truthy(value) {
		nodes = n.thenNodes
	}

	return renderNodes(nodes, ctx)
}

type parser struct {
	template string
	offset   int
}

// Build renders the current workflow prompt for one issue and optional retry attempt.
func Build(issue any, attempt *int) (string, error) {
	loaded, err := loadCurrentWorkflow()
	if err != nil {
		return "", &WorkflowUnavailableError{Reason: err}
	}

	templateText := loaded.PromptTemplate
	if strings.TrimSpace(templateText) == "" {
		templateText = config.Current().WorkflowPrompt
	}

	nodes, err := parse(templateText)
	if err != nil {
		return "", err
	}

	ctx := map[string]any{
		"attempt": attemptValue(attempt),
		"issue":   normalizeValue(issue),
	}

	return renderNodes(nodes, ctx)
}

func loadCurrentWorkflow() (workflow.Loaded, error) {
	return workflow.Current()
}

func parse(template string) ([]node, error) {
	p := &parser{template: template}
	nodes, terminator, err := p.parseSection(nil)
	if err != nil {
		return nil, err
	}

	if terminator != "" {
		return nil, &ParseError{Template: template, Reason: fmt.Sprintf("unexpected tag %q", terminator)}
	}

	return nodes, nil
}

func (p *parser) parseSection(stop map[string]bool) ([]node, string, error) {
	var nodes []node

	for p.offset < len(p.template) {
		nextTag := strings.Index(p.template[p.offset:], "{")
		if nextTag == -1 {
			nodes = append(nodes, textNode{text: p.template[p.offset:]})
			p.offset = len(p.template)
			break
		}

		nextTag += p.offset
		if nextTag > p.offset {
			nodes = append(nodes, textNode{text: p.template[p.offset:nextTag]})
			p.offset = nextTag
		}

		switch {
		case strings.HasPrefix(p.template[p.offset:], "{{"):
			tagEnd := strings.Index(p.template[p.offset+2:], "}}")
			if tagEnd == -1 {
				return nil, "", &ParseError{Template: p.template, Reason: "unterminated variable expression"}
			}

			expression := strings.TrimSpace(p.template[p.offset+2 : p.offset+2+tagEnd])
			if expression == "" {
				return nil, "", &ParseError{Template: p.template, Reason: "empty variable expression"}
			}

			nodes = append(nodes, variableNode{expression: expression})
			p.offset += 2 + tagEnd + 2

		case strings.HasPrefix(p.template[p.offset:], "{%"):
			tagEnd := strings.Index(p.template[p.offset+2:], "%}")
			if tagEnd == -1 {
				return nil, "", &ParseError{Template: p.template, Reason: "unterminated control tag"}
			}

			tag := strings.TrimSpace(p.template[p.offset+2 : p.offset+2+tagEnd])
			p.offset += 2 + tagEnd + 2

			if stop != nil && stop[tag] {
				return nodes, tag, nil
			}

			switch {
			case strings.HasPrefix(tag, "if "):
				condition := strings.TrimSpace(strings.TrimPrefix(tag, "if "))
				if condition == "" {
					return nil, "", &ParseError{Template: p.template, Reason: "empty if condition"}
				}

				thenNodes, terminator, err := p.parseSection(map[string]bool{"else": true, "endif": true})
				if err != nil {
					return nil, "", err
				}
				if terminator == "" {
					return nil, "", &ParseError{Template: p.template, Reason: "missing endif"}
				}

				var elseNodes []node
				if terminator == "else" {
					elseNodes, terminator, err = p.parseSection(map[string]bool{"endif": true})
					if err != nil {
						return nil, "", err
					}
					if terminator != "endif" {
						return nil, "", &ParseError{Template: p.template, Reason: "missing endif after else"}
					}
				}

				nodes = append(nodes, ifNode{
					condition: condition,
					thenNodes: thenNodes,
					elseNodes: elseNodes,
				})

			case tag == "else", tag == "endif":
				return nil, "", &ParseError{Template: p.template, Reason: fmt.Sprintf("unexpected tag %q", tag)}

			default:
				return nil, "", &ParseError{Template: p.template, Reason: fmt.Sprintf("unsupported tag %q", tag)}
			}

		default:
			nodes = append(nodes, textNode{text: p.template[p.offset : p.offset+1]})
			p.offset++
		}
	}

	return nodes, "", nil
}

func renderNodes(nodes []node, ctx map[string]any) (string, error) {
	var b strings.Builder
	for _, node := range nodes {
		rendered, err := node.render(ctx)
		if err != nil {
			return "", err
		}
		b.WriteString(rendered)
	}
	return b.String(), nil
}

func resolveExpression(expression string, ctx map[string]any) (any, error) {
	segments := strings.Split(strings.TrimSpace(expression), ".")
	if len(segments) == 0 || segments[0] == "" {
		return nil, &RenderError{Expression: expression}
	}

	var current any = ctx
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			return nil, &RenderError{Expression: expression}
		}

		next, ok := resolveSegment(current, segment)
		if !ok {
			return nil, &RenderError{Expression: expression}
		}
		current = next
	}

	return current, nil
}

func resolveSegment(current any, segment string) (any, bool) {
	switch typed := current.(type) {
	case map[string]any:
		value, ok := typed[segment]
		return value, ok
	default:
		return nil, false
	}
}

func attemptValue(attempt *int) any {
	if attempt == nil {
		return nil
	}
	return *attempt
}

func normalizeValue(value any) any {
	if value == nil {
		return nil
	}

	return normalizeReflectValue(reflect.ValueOf(value))
}

func normalizeReflectValue(value reflect.Value) any {
	if !value.IsValid() {
		return nil
	}

	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}

	if value.Type() == reflect.TypeOf(time.Time{}) {
		return value.Interface().(time.Time).Format(time.RFC3339)
	}

	switch value.Kind() {
	case reflect.Struct:
		result := make(map[string]any, value.NumField())
		valueType := value.Type()
		for index := 0; index < value.NumField(); index++ {
			field := valueType.Field(index)
			if !field.IsExported() {
				continue
			}

			key := fieldName(field)
			if key == "" {
				continue
			}

			result[key] = normalizeReflectValue(value.Field(index))
		}
		return result

	case reflect.Map:
		result := make(map[string]any, value.Len())
		iter := value.MapRange()
		for iter.Next() {
			result[fmt.Sprint(iter.Key().Interface())] = normalizeReflectValue(iter.Value())
		}
		return result

	case reflect.Slice, reflect.Array:
		result := make([]any, value.Len())
		for index := 0; index < value.Len(); index++ {
			result[index] = normalizeReflectValue(value.Index(index))
		}
		return result

	case reflect.String:
		return value.String()
	case reflect.Bool:
		return value.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(value.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return int(value.Uint())
	case reflect.Float32, reflect.Float64:
		return value.Float()
	default:
		return value.Interface()
	}
}

func fieldName(field reflect.StructField) string {
	if tag := field.Tag.Get("json"); tag != "" {
		name := strings.Split(tag, ",")[0]
		if name == "-" {
			return ""
		}
		if name != "" {
			return name
		}
	}

	return toSnakeCase(field.Name)
}

func toSnakeCase(value string) string {
	if value == "" {
		return ""
	}

	var b strings.Builder
	runes := []rune(value)
	for index, r := range runes {
		if index > 0 && isUpper(r) && (index+1 < len(runes) && !isUpper(runes[index+1]) || !isUpper(runes[index-1])) {
			b.WriteByte('_')
		}
		b.WriteRune(toLower(r))
	}

	return b.String()
}

func isUpper(r rune) bool {
	return r >= 'A' && r <= 'Z'
}

func toLower(r rune) rune {
	if isUpper(r) {
		return r + ('a' - 'A')
	}
	return r
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return strings.TrimSpace(typed) != ""
	case int:
		return typed != 0
	case float64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func stringifyValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, entry := range typed {
			rendered := stringifyValue(entry)
			if rendered != "" {
				parts = append(parts, rendered)
			}
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(typed)
	}
}
