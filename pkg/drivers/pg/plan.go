package pg

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// parsePlanJSON parses the JSON document returned by `EXPLAIN (FORMAT JSON)`
// and returns a models.Plan whose Node is the root of the parsed tree. The
// Postgres wire-format wraps the plan as a single-element array containing an
// object with a "Plan" key, e.g.:
//
//	[
//	  {
//	    "Plan": { "Node Type": "Seq Scan", "Total Cost": 1.2, "Plan Rows": 7, "Plans": [...] }
//	  }
//	]
//
// An empty array yields models.Plan{Node: nil}, nil. Malformed JSON or a
// missing/non-object "Plan" entry returns a wrapped error.
//
// parsePlanJSON is pure (no I/O, no driver dependencies) so it can be unit
// tested against golden fixtures without a live Postgres connection.
func parsePlanJSON(raw []byte) (models.Plan, error) {
	if len(raw) == 0 {
		return models.Plan{}, nil
	}
	var envelope []map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return models.Plan{}, fmt.Errorf("EXPLAIN parse failed: %w", err)
	}
	if len(envelope) == 0 {
		return models.Plan{}, nil
	}
	planAny, ok := envelope[0]["Plan"]
	if !ok {
		return models.Plan{}, fmt.Errorf("EXPLAIN parse failed: missing %q key", "Plan")
	}
	planMap, ok := planAny.(map[string]any)
	if !ok {
		return models.Plan{}, fmt.Errorf("EXPLAIN parse failed: %q is not an object", "Plan")
	}
	node := buildPlanNode(planMap)
	plan := models.Plan{
		Node:     node,
		Analyzed: nodeHasActuals(node),
		Settings: parseSettings(envelope[0]["Settings"]),
	}
	plan.ComputeDerived()
	return plan, nil
}

// parseSettings lifts the top-level "Settings" object PG emits under
// `EXPLAIN (SETTINGS)` into a flat string map. Returns nil when the key is
// absent or not an object, so the estimate-only path leaves Plan.Settings nil.
func parseSettings(v any) map[string]string {
	obj, ok := v.(map[string]any)
	if !ok || len(obj) == 0 {
		return nil
	}
	out := make(map[string]string, len(obj))
	for k, val := range obj {
		out[k] = fmt.Sprint(val)
	}
	return out
}

// nodeHasActuals reports whether n (or any descendant) carries one or more
// Actual* fields populated by `EXPLAIN ANALYZE`. The walk is depth-first;
// returns true on the first node with a non-zero actual. dbsavvy-uv0.8.
func nodeHasActuals(n *models.PlanNode) bool {
	if n == nil {
		return false
	}
	if n.ActualCost != 0 || n.ActualRows != 0 || n.Loops != 0 || n.ActualTotalTime != 0 {
		return true
	}
	return slices.ContainsFunc(n.Children, nodeHasActuals)
}

// buildPlanNode recursively transforms a decoded EXPLAIN-JSON object into a
// *models.PlanNode. Recognized keys (cost/row estimates, ANALYZE actuals,
// BUFFERS accounting, VERBOSE "Output", relation identity, sort/parallel
// diagnostics, "Plans") are lifted into typed fields; every other scalar key
// is stringified into Detail via fmt.Sprint. Nested arrays/objects other than
// the recognized "Plans" / "Output" lists are skipped (not recorded in Detail)
// to keep the map flat — UI rendering only consumes scalar metadata.
func buildPlanNode(m map[string]any) *models.PlanNode {
	if m == nil {
		return nil
	}
	n := &models.PlanNode{Detail: map[string]string{}}
	for k, v := range m {
		switch k {
		case "Node Type":
			if s, ok := v.(string); ok {
				n.Op = s
			}
		case "Total Cost":
			n.Cost = jsonNumberToFloat(v)
		case "Plan Rows":
			n.EstRows = jsonNumberToInt64(v)
		case "Actual Total Cost":
			n.ActualCost = jsonNumberToFloat(v)
		case "Actual Rows":
			n.ActualRows = jsonNumberToInt64(v)
		case "Actual Loops":
			n.Loops = jsonNumberToInt64(v)
		case "Actual Total Time":
			n.ActualTotalTime = jsonNumberToFloat(v)
		case "Actual Startup Time":
			n.ActualStartupTime = jsonNumberToFloat(v)
		case "Plan Width":
			n.PlanWidth = int(jsonNumberToInt64(v))
		case "Rows Removed by Filter":
			n.RowsRemovedByFilter = jsonNumberToInt64(v)
		case "Shared Hit Blocks":
			n.SharedHitBlocks = jsonNumberToInt64(v)
		case "Shared Read Blocks":
			n.SharedReadBlocks = jsonNumberToInt64(v)
		case "Shared Written Blocks":
			n.SharedWrittenBlocks = jsonNumberToInt64(v)
		case "Local Hit Blocks":
			n.LocalHitBlocks = jsonNumberToInt64(v)
		case "Local Read Blocks":
			n.LocalReadBlocks = jsonNumberToInt64(v)
		case "Local Written Blocks":
			n.LocalWrittenBlocks = jsonNumberToInt64(v)
		case "Temp Read Blocks":
			n.TempReadBlocks = jsonNumberToInt64(v)
		case "Temp Written Blocks":
			n.TempWrittenBlocks = jsonNumberToInt64(v)
		case "Workers Launched":
			n.WorkersLaunched = int(jsonNumberToInt64(v))
		case "Parallel Aware":
			if b, ok := v.(bool); ok {
				n.ParallelAware = b
			}
		case "Sort Method":
			if s, ok := v.(string); ok {
				n.SortMethod = s
			}
		case "Sort Space Used":
			n.SortSpaceUsed = jsonNumberToInt64(v)
		case "Heap Fetches":
			n.HeapFetches = jsonNumberToInt64(v)
		case "Relation Name":
			if s, ok := v.(string); ok {
				n.RelationName = s
			}
		case "Alias":
			if s, ok := v.(string); ok {
				n.Alias = s
			}
		case "Index Name":
			if s, ok := v.(string); ok {
				n.IndexName = s
			}
		case "Output":
			if arr, ok := v.([]any); ok {
				for _, col := range arr {
					if s, ok := col.(string); ok {
						n.OutputColumns = append(n.OutputColumns, s)
					}
				}
			}
		case "Plans":
			if arr, ok := v.([]any); ok {
				for _, child := range arr {
					childMap, ok := child.(map[string]any)
					if !ok {
						continue
					}
					n.Children = append(n.Children, buildPlanNode(childMap))
				}
			}
		default:
			// Only scalars belong in Detail; arrays/objects are skipped
			// (we already special-case "Plans" above).
			switch v.(type) {
			case map[string]any, []any:
				continue
			default:
				n.Detail[k] = fmt.Sprint(v)
			}
		}
	}
	if len(n.Detail) == 0 {
		n.Detail = nil
	}
	return n
}

// jsonNumberToFloat coerces a json-decoded value into a float64. encoding/json
// decodes numbers into float64 by default; we also accept json.Number for
// callers that may have set UseNumber on a Decoder. Returns 0 for any other
// type.
func jsonNumberToFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return 0
		}
		return f
	default:
		return 0
	}
}

// jsonNumberToInt64 coerces a json-decoded value into an int64. Float inputs
// are rounded to the nearest integer (Postgres reports Plan Rows as a JSON
// number which encoding/json materializes as float64).
func jsonNumberToInt64(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(math.Round(x))
	case float32:
		return int64(math.Round(float64(x)))
	case int:
		return int64(x)
	case int64:
		return x
	case json.Number:
		i, err := x.Int64()
		if err == nil {
			return i
		}
		f, err := x.Float64()
		if err != nil {
			return 0
		}
		return int64(math.Round(f))
	default:
		return 0
	}
}
