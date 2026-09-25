// Shared by the test files; not part of the bundle (nothing the entry
// point imports reaches it).

interface WireBatch {
  body: { attributes: Record<string, unknown>; events: Array<{ attributes?: unknown }> };
}

/**
 * What the collector stores for each event of a sent batch: the batch's
 * attributes under the event's own, a null removing the key. The SDK moves
 * values every event shares up to the batch (hoistShared), so assertions
 * about an event's data read this, not the event's wire attributes.
 */
export function storedEvents(batch: WireBatch): Record<string, unknown>[] {
  return batch.body.events.map((e) => {
    const merged: Record<string, unknown> = { ...batch.body.attributes, ...(e.attributes as Record<string, unknown>) };
    for (const key of Object.keys(merged)) if (merged[key] === null) delete merged[key];
    return merged;
  });
}
