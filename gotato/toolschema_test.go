package gotato

import (
	"testing"
)

func TestSchemaIntegerTypeRejectsFraction(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"count":{"type":"integer"}},"additionalProperties":false}`)
	if err := validateToolSchema(schema, []byte(`{"count":3}`)); err != nil {
		t.Fatalf("whole number rejected for an integer schema: %v", err)
	}
	if err := validateToolSchema(schema, []byte(`{"count":1.5}`)); err == nil {
		t.Fatal("fractional value accepted for an integer schema")
	}
	if err := validateToolSchema([]byte(`{"type":"number"}`), []byte(`1.5`)); err != nil {
		t.Fatalf("fractional value rejected for a number schema: %v", err)
	}
}
