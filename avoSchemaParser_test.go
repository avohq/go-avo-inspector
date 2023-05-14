package avoinspector

import (
	"testing"
)

func TestExtractSchema(t *testing.T) {
	testCases := []struct {
		name           string
		eventProps     map[string]interface{}
		expectedResult []Property
	}{
		{
			name: "simple string property",
			eventProps: map[string]interface{}{
				"name": "John",
			},
			expectedResult: []Property{
				{
					PropertyName: "name",
					PropertyType: "string",
					Children:     nil,
				},
			},
		},
		{
			name: "nested object",
			eventProps: map[string]interface{}{
				"name": "John",
				"address": map[string]interface{}{
					"street": "123 Main St",
					"city":   "Anytown",
					"zip":    12345,
				},
			},
			expectedResult: []Property{
				{
					PropertyName: "name",
					PropertyType: "string",
					Children:     nil,
				},
				{
					PropertyName: "address",
					PropertyType: "object",
					Children: []Property{
						{
							PropertyName: "street",
							PropertyType: "string",
							Children:     nil,
						},
						{
							PropertyName: "city",
							PropertyType: "string",
							Children:     nil,
						},
						{
							PropertyName: "zip",
							PropertyType: "int",
							Children:     nil,
						},
					},
				},
			},
		},
		{
			name: "list of objects",
			eventProps: map[string]interface{}{
				"people": []interface{}{
					map[string]interface{}{
						"name": "John",
						"age":  30,
					},
					map[string]interface{}{
						"name": "Jane",
						"age":  25,
					},
				},
			},
			expectedResult: []Property{
				{
					PropertyName: "people",
					PropertyType: "list",
					Children: []Property{
						{
							PropertyName: "0",
							PropertyType: "object",
							Children: []Property{
								{
									PropertyName: "name",
									PropertyType: "string",
									Children:     nil,
								},
								{
									PropertyName: "age",
									PropertyType: "int",
									Children:     nil,
								},
							},
						},
						{
							PropertyName: "1",
							PropertyType: "object",
							Children: []Property{
								{
									PropertyName: "name",
									PropertyType: "string",
									Children:     nil,
								},
								{
									PropertyName: "age",
									PropertyType: "int",
									Children:     nil,
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := extractSchema(tc.eventProps)
			if !compareProperties(tc.expectedResult, result) {
				t.Errorf("Expected %v, but got %v", tc.expectedResult, result)
			}
		})
	}
}

func compareProperties(expected, actual []Property) bool {
	if len(expected) != len(actual) {
		return false
	}

	found := false

	for i, exp := range expected {
		act := actual[i]

		if exp.PropertyName == act.PropertyName && exp.PropertyType == act.PropertyType {
			if len(exp.Children) > 0 {
				if compareProperties(exp.Children, act.Children) {
					found = true
				}
			} else {
				found = true
			}
		}

	}

	return found
}
