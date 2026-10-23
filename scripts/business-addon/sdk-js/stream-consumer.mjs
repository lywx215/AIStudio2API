// Business emits STOP only after successful upstream completion and delivery.
// A completed SDK iterator alone is not a completion signal: SDK 2.22.0 drops
// the error property of ordinary data: SSE error envelopes during conversion.
export class BusinessStreamCompletionError extends Error {
  constructor(code) {
    super('Business generation stream did not complete successfully');
    this.name = 'BusinessStreamCompletionError';
    this.code = code;
  }
}

// Preserve each original SDK chunk for the caller and do not replay requests.
// onChunk may consume output incrementally; no response buffer is accumulated.
export async function consumeBusinessStream(source, onChunk = () => {}) {
  let completed = false;
  let events = 0;
  for await (const chunk of await source) {
    events += 1;
    if (chunk?.error) throw new BusinessStreamCompletionError('UPSTREAM_STREAM_ERROR');
    for (const candidate of chunk?.candidates ?? []) {
      if (!candidate.finishReason) continue;
      if (candidate.finishReason !== 'STOP') throw new BusinessStreamCompletionError('UNSUCCESSFUL_FINISH');
      completed = true;
    }
    await onChunk(chunk);
  }
  if (!completed) throw new BusinessStreamCompletionError('MISSING_STOP');
  return { events, completed };
}
