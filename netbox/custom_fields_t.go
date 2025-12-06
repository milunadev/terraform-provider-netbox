package netbox

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

const customFieldsKeyT = "custom_fields"

var customFieldsSchemaT = &schema.Schema{
	Type:     schema.TypeMap,
	Optional: true,
	Default:  nil,
	Elem: &schema.Schema{
		Type: schema.TypeString,
	},
	// Note: Terraform converts all values to strings when using TypeMap with Elem: TypeString.
	// We use convertCustomFieldsFromSchemaT() to infer and convert them back to proper types (int, bool, etc.)
	// before sending to Netbox API.
}

func getCustomFieldsT(cf interface{}) map[string]interface{} {
	cfm, ok := cf.(map[string]interface{})
	if !ok || len(cfm) == 0 {
		return nil
	}
	return cfm
}

// convertCustomFieldsFromSchemaT converts custom field values from Terraform's string-based schema
// to their native types (int, bool, JSON objects, etc.) before sending to Netbox API.
// Terraform converts all values to strings when using TypeMap with Elem: TypeString,
// so we need to infer and convert them back to their proper types.
func convertCustomFieldsFromSchemaT(cf interface{}) map[string]interface{} {
	cfm, ok := cf.(map[string]interface{})
	if !ok || len(cfm) == 0 {
		return nil
	}

	result := make(map[string]interface{})
	for key, value := range cfm {
		if value == nil {
			result[key] = nil
			continue
		}

		// Terraform passes values as strings when using TypeMap with Elem: TypeString
		strValue, isString := value.(string)
		if !isString {
			// If it's already a non-string type, use it as-is
			result[key] = value
			continue
		}

		// Empty string
		if strValue == "" {
			result[key] = nil
			continue
		}

		// Try to parse as boolean
		if strValue == "true" {
			result[key] = true
			continue
		}
		if strValue == "false" {
			result[key] = false
			continue
		}

		// Try to parse as integer
		if intValue, err := strconv.ParseInt(strValue, 10, 64); err == nil {
			result[key] = intValue
			continue
		}

		// Try to parse as float
		if floatValue, err := strconv.ParseFloat(strValue, 64); err == nil {
			// Check if it's actually an integer (no decimal part)
			if floatValue == float64(int64(floatValue)) {
				result[key] = int64(floatValue)
			} else {
				result[key] = floatValue
			}
			continue
		}

		// Try to parse as JSON (for complex objects/arrays)
		var jsonValue interface{}
		if err := json.Unmarshal([]byte(strValue), &jsonValue); err == nil {
			result[key] = jsonValue
			continue
		}

		// If nothing matches, keep as string
		result[key] = strValue
	}

	return result
}

// flattenCustomFieldsT converts custom fields to a map where all values are strings.
// Complex nested objects (like IP address references) are converted to JSON strings.
func flattenCustomFieldsT(cf interface{}) map[string]interface{} {
	cfm, ok := cf.(map[string]interface{})
	if !ok || len(cfm) == 0 {
		return nil
	}

	result := make(map[string]interface{})
	for key, value := range cfm {
		if value == nil {
			result[key] = ""
			continue
		}

		// Check if the value is a simple type (string, number, bool)
		switch v := value.(type) {
		case string:
			result[key] = v
		case float64, int, int64, bool:
			result[key] = fmt.Sprintf("%v", v)
		default:
			// For complex types (maps, arrays, objects), convert to JSON string
			if jsonBytes, err := json.Marshal(value); err == nil {
				result[key] = string(jsonBytes)
			} else {
				// Fallback to string representation
				result[key] = fmt.Sprintf("%v", value)
			}
		}
	}

	return result
}
