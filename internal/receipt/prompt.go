package receipt

import (
	"encoding/json"
	"sync"
)

// The extraction contract shared by every backend: what the model is asked for,
// and the schema its answer must satisfy. Kept apart from any one client so the
// two cannot drift.

// extractPrompt is tuned against real receipts. Each rule exists because its
// absence produced a specific wrong answer during Phase 0.
const extractPrompt = `Extract this receipt into JSON. The image may be rotated.

RULES
1. Copy text and numbers exactly. Perform NO arithmetic.
2. Every purchased item goes in items, in printed order. Repeated items are normal --
   three "Baked Potato" lines are three items, not one.
3. A number left of the description is the quantity; the price is the rightmost number.
   Never put a quantity or product code in amount.
4. A sub-line with no price, or a price of [0.00], modifies the item above it. It is NOT an
   item and NOT an adjustment. Omit it. Same for "Regular Price $39.99" lines.
5. adjustments is ONLY for a discount printed as its own line and subtracted from the
   total. A summary of money saved or a tip suggestion is NOT an adjustment: leave
   adjustments empty for "YOUR TOTAL SAVINGS THIS TRIP: $20.00", "18% 75.78" and the like.
6. SUBTOTAL, TAX, TOTAL, payment, auth code, gratuity notes, survey text and department
   headers such as GROCERY or KITCHEN are NOT items.
7. Record any taxability marker (T, TF, N, F) verbatim in marker and set taxable to match.
8. If a tax line prints its own base, as in "6.00000 on $70.66", put 70.66 in base and the
   rate as a decimal (0.06) in rate.
9. Copy the printed date into purchased_at. Use null for anything not visible.`

// extractSchema constrains generation.
//
// A "transcription" field was tried here, declared first so the model would read
// the whole receipt before structuring it. On one encoding of a dim 14-item
// restaurant check it turned 6 items into 14, but across other encodings of the
// same photo it was no better than without, while costing 40-60% more wall clock
// on every scan. The apparent win did not survive measurement, so it is gone.
//
// Note the absence of a confidence field: it measured 1.0 on a 50%-wrong
// extraction and is worse than useless.
const extractSchema = `{
  "type": "object",
  "properties": {
    "merchant":     { "type": ["string", "null"] },
    "purchased_at": { "type": ["string", "null"] },
    "currency":     { "type": ["string", "null"] },
    "items": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "position":    { "type": "integer" },
          "line_text":   { "type": "string" },
          "description": { "type": "string" },
          "quantity":    { "type": ["number", "null"] },
          "unit_price":  { "type": ["number", "null"] },
          "amount":      { "type": "number" },
          "taxable":     { "type": ["boolean", "null"] },
          "marker":      { "type": ["string", "null"] }
        },
        "required": ["position", "line_text", "description", "amount", "taxable"]
      }
    },
    "adjustments": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "label":               { "type": "string" },
          "amount":              { "type": "number" },
          "applies_to_position": { "type": ["integer", "null"] }
        },
        "required": ["label", "amount"]
      }
    },
    "tax_lines": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "label":  { "type": "string" },
          "rate":   { "type": ["number", "null"] },
          "base":   { "type": ["number", "null"] },
          "amount": { "type": "number" }
        },
        "required": ["label", "amount"]
      }
    },
    "subtotal":     { "type": ["number", "null"] },
    "total":        { "type": ["number", "null"] },
    "tax_evidence": { "type": "string", "enum": ["per_line_flags", "single_rate", "multi_rate", "unknown"] }
  },
  "required": ["merchant", "items", "tax_lines", "subtotal", "total", "tax_evidence"]
}`

// ExtractionRules returns the extraction contract as prose, for a caller that
// reads the receipt itself instead of handing this app the photo.
//
// It is the same text the built-in backends send. Sharing it is the point: the
// rules here were each paid for by a specific wrong answer, and a second copy
// written for MCP clients would drift away from them one edit at a time.
func ExtractionRules() string { return extractPrompt }

// ExtractionSchema returns the JSON Schema an extraction must satisfy, parsed
// so it can be embedded in a tool definition.
//
// Parsed once: the schema is a compile-time constant, and re-parsing it per
// tools/list call would be work done to reach the same answer.
func ExtractionSchema() map[string]any {
	schemaOnce.Do(func() {
		if err := json.Unmarshal([]byte(extractSchema), &parsedSchema); err != nil {
			// Unreachable unless extractSchema is edited into invalid JSON, which
			// the test in this package catches first.
			panic("receipt: extractSchema is not valid JSON: " + err.Error())
		}
	})
	return parsedSchema
}

var (
	schemaOnce   sync.Once
	parsedSchema map[string]any
)
