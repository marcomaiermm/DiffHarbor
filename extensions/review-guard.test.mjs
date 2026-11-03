import assert from "node:assert/strict";
import test from "node:test";
import register from "./review-guard.js";

test("tool_call is blocked only after DIFFHARBOR_DEADLINE or past 60% context", () => {
	const handlers = {};
	register({ on: (name, fn) => (handlers[name] = fn) });
	const old = process.env.DIFFHARBOR_DEADLINE;
	try {
		delete process.env.DIFFHARBOR_DEADLINE;
		assert.equal(handlers.tool_call({ toolName: "bash" }), undefined);
		process.env.DIFFHARBOR_DEADLINE = String(Date.now() + 60_000);
		assert.equal(handlers.tool_call({ toolName: "bash" }), undefined);
		process.env.DIFFHARBOR_DEADLINE = String(Date.now() - 1);
		assert.equal(handlers.tool_call({ toolName: "bash" }).block, true);
		process.env.DIFFHARBOR_DEADLINE = String(Date.now() + 60_000);
		const usage = (percent) => ({ getContextUsage: () => ({ tokens: 1, contextWindow: 2, percent }) });
		assert.equal(handlers.tool_call({ toolName: "bash" }, usage(59)), undefined);
		assert.equal(handlers.tool_call({ toolName: "bash" }, usage(null)), undefined);
		assert.equal(handlers.tool_call({ toolName: "bash" }, usage(61)).block, true);
	} finally {
		if (old === undefined) delete process.env.DIFFHARBOR_DEADLINE;
		else process.env.DIFFHARBOR_DEADLINE = old;
	}
});

test("tool_call is blocked after DIFFHARBOR_MAX_TOOLS calls", () => {
	const handlers = {};
	register({ on: (name, fn) => (handlers[name] = fn) });
	const old = process.env.DIFFHARBOR_MAX_TOOLS;
	try {
		process.env.DIFFHARBOR_MAX_TOOLS = "2";
		assert.equal(handlers.tool_call({ toolName: "read" }), undefined);
		assert.equal(handlers.tool_call({ toolName: "bash" }), undefined);
		assert.equal(handlers.tool_call({ toolName: "bash" }).block, true);
	} finally {
		if (old === undefined) delete process.env.DIFFHARBOR_MAX_TOOLS;
		else process.env.DIFFHARBOR_MAX_TOOLS = old;
	}
});
