// Pi extension for diffharbor: once a pass runs out of time, context or tool calls, tool calls are refused,
// so the model answers with what it checked instead of being killed (or compacted) with nothing.
export default function (pi) {
	let calls = 0; // one Pi process per pass
	pi.on("tool_call", (_event, ctx) => {
		// DIFFHARBOR_MAX_TOOLS: every call re-reads the context and adds thinking, so calls are most of the cost.
		const max = Number(process.env.DIFFHARBOR_MAX_TOOLS);
		if (max > 0 && ++calls > max) {
			return { block: true, reason: "Tool budget used: call no more tools. Output the required JSON now from what you have checked." };
		}
		// DIFFHARBOR_DEADLINE: unix ms, set by diffharbor before its hard timeout.
		const deadline = Number(process.env.DIFFHARBOR_DEADLINE);
		if (deadline > 0 && Date.now() > deadline) {
			return { block: true, reason: "Time is up: call no more tools. Output the required JSON now from what you have checked." };
		}
		// Past 60%: Pi's auto-compaction (at window - 16k) would summarize away the diff, and the answer
		// plus this turn's tool results need the rest.
		if ((ctx?.getContextUsage?.()?.percent ?? 0) > 60) {
			return { block: true, reason: "Context is nearly full: call no more tools. Output the required JSON now from what you have checked." };
		}
	});
}
