import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { QueryClient } from "@tanstack/react-query";
import { invalidateAuthQueries } from "@/lib/auth";
import {
  invalidateMemeQueries,
  invalidateSubscriptionQuery,
  queryKeys,
  subscriptionStatusQueryOptions,
} from "@/lib/queries";

describe("TKT-104 subscription cache & invalidation", () => {
  it("subscriptionStatusQueryOptions(false) disables unauthenticated polling and uses staleTime: 0", () => {
    const opts = subscriptionStatusQueryOptions(false);
    assert.deepEqual(opts.queryKey, queryKeys.subscription);
    assert.equal(opts.enabled, false);
    assert.equal(opts.staleTime, 0);
    assert.equal(opts.refetchOnWindowFocus, true);
    assert.equal(opts.retry, false);
  });

  it("subscriptionStatusQueryOptions(true) enables authenticated polling with staleTime: 0", () => {
    const opts = subscriptionStatusQueryOptions(true);
    assert.deepEqual(opts.queryKey, queryKeys.subscription);
    assert.equal(opts.enabled, true);
    assert.equal(opts.staleTime, 0);
    assert.equal(opts.refetchOnWindowFocus, true);
    assert.equal(opts.retry, false);
  });

  it("invalidateAuthQueries invalidates me, subscription, and memes", async () => {
    const qc = new QueryClient();
    qc.setQueryData(queryKeys.me, { id: "u1", email: "a@b.com", name: "A" });
    qc.setQueryData(queryKeys.subscription, { subscribed: false });
    qc.setQueryData(queryKeys.memes(20, 0), { items: [], total: 0 });

    await invalidateAuthQueries(qc);

    assert.equal(qc.getQueryState(queryKeys.me)?.isInvalidated, true);
    assert.equal(qc.getQueryState(queryKeys.subscription)?.isInvalidated, true);
    assert.equal(qc.getQueryState(queryKeys.memes(20, 0))?.isInvalidated, true);
  });

  it("invalidateSubscriptionQuery invalidates subscription", async () => {
    const qc = new QueryClient();
    qc.setQueryData(queryKeys.subscription, { subscribed: false });

    await invalidateSubscriptionQuery(qc);

    assert.equal(qc.getQueryState(queryKeys.subscription)?.isInvalidated, true);
  });

  it("invalidateMemeQueries invalidates both memes and subscription", async () => {
    const qc = new QueryClient();
    qc.setQueryData(queryKeys.memes(20, 0), { items: [], total: 0 });
    qc.setQueryData(queryKeys.subscription, { subscribed: true, memesUsed: 0 });

    await invalidateMemeQueries(qc);

    assert.equal(qc.getQueryState(queryKeys.memes(20, 0))?.isInvalidated, true);
    assert.equal(qc.getQueryState(queryKeys.subscription)?.isInvalidated, true);
  });
});
