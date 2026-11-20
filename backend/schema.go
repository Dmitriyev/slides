package main

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
)

// Check wire types and required fields before decoding, so omitted numbers and
// explicit nulls cannot silently become Go zero values. Optional defaults are
// supplied by the typed decoders. Length and numeric limits stay in models.go.
func validateWire(raw []byte, kind string) error {
	var schema map[string]any
	if err := json.Unmarshal(schemas[kind], &schema); err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	return checkWire(value, schema, schema)
}
func checkWire(value any, schema, root map[string]any) error {
	if ref, ok := schema["$ref"].(string); ok {
		node := any(root)
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			m, ok := node.(map[string]any)
			if !ok {
				return fmt.Errorf("Invalid schema reference")
			}
			node = m[part]
		}
		target, ok := node.(map[string]any)
		if !ok {
			return fmt.Errorf("Invalid schema reference")
		}
		return checkWire(value, target, root)
	}
	if options, ok := schema["anyOf"].([]any); ok {
		for _, option := range options {
			if checkWire(value, option.(map[string]any), root) == nil {
				return nil
			}
		}
		return fmt.Errorf("Invalid field type")
	}
	if options, ok := schema["enum"].([]any); ok {
		valid := false
		for _, option := range options {
			if reflect.DeepEqual(value, option) {
				valid = true
			}
		}
		if !valid {
			return fmt.Errorf("Invalid field value")
		}
	}
	switch schema["type"] {
	case "null":
		if value != nil {
			return fmt.Errorf("Expected null")
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("Expected string")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("Expected boolean")
		}
	case "number", "integer":
		n, ok := value.(float64)
		if !ok || (schema["type"] == "integer" && n != math.Trunc(n)) {
			return fmt.Errorf("Expected number")
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("Expected array")
		}
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for _, item := range items {
				if err := checkWire(item, itemSchema, root); err != nil {
					return err
				}
			}
		}
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("Expected object")
		}
		if required, ok := schema["required"].([]any); ok {
			for _, name := range required {
				if _, ok := obj[name.(string)]; !ok {
					return fmt.Errorf("Missing required field: %s", name)
				}
			}
		}
		if props, ok := schema["properties"].(map[string]any); ok {
			for name, field := range props {
				if v, exists := obj[name]; exists {
					if err := checkWire(v, field.(map[string]any), root); err != nil {
						return fmt.Errorf("%s: %w", name, err)
					}
				}
			}
		}
	}
	return nil
}
