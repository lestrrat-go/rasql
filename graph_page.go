package rasql

import (
	"context"
	"fmt"
)

func PageGraphAfter[R, G any](ctx context.Context, executor Executor, plan GraphPlan[R, G], spec PageSpec[R], policy PagePolicy, request PageRequest) (Page[G], error) {
	var result Page[G]
	_, err := prepareGraphPlan(executor, plan)
	if err != nil {
		return result, err
	}
	root, ok := plan.node.query.(graphQuery[R, G])
	if !ok {
		return result, planError("internal_plan", "graph.root", "root query type is invalid")
	}
	prepared, err := preparePageAfter(executor, root.value, spec, policy, request)
	if err != nil {
		return result, err
	}
	if prepared.impossible {
		return result, nil
	}

	callCtx, observed, completion := beginLogicalInvocation(ctx, executor, EventGraph)
	var finalErr error
	var early bool
	observedRows := int64(0)
	defer func() {
		if value := recover(); value != nil {
			finalErr = fmt.Errorf("graph page panicked: %v", value)
			completion.completeLogicalInvocation(finalErr, observedRows, early)
			panic(value)
		}
		completion.completeLogicalInvocation(finalErr, observedRows, early)
	}()

	rootRows := make([]graphRow, 0, prepared.limit)
	page, err := consumePreparedPage(callCtx, observed, prepared, func(row R, kept bool) error {
		observedRows++
		if !kept {
			early = true
			return nil
		}
		rootRows = append(rootRows, graphRow{row: row, graph: root.mapRow(row)})
		return nil
	})
	if err != nil {
		finalErr = err
		return result, err
	}
	graphs, err := expandGraphRoots[R, G](callCtx, observed, plan.node, rootRows, &observedRows)
	if err != nil {
		finalErr = err
		return result, err
	}
	result.Values = graphs
	result.Next = page.Next
	result.HasMore = page.HasMore
	return result, nil
}
