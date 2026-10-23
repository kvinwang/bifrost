import type { LogEntry } from "@/lib/types/logs";

// Output tokens per second over the generation phase (first chunk to last chunk).
export function outputTokensPerSecond(log: Pick<LogEntry, "latency" | "time_to_first_token" | "token_usage">): number | undefined {
	const completion = log.token_usage?.completion_tokens;
	if (log.latency == null || log.time_to_first_token == null || !completion) return undefined;
	const generationMs = log.latency - log.time_to_first_token;
	return generationMs > 0 ? (completion * 1000) / generationMs : undefined;
}