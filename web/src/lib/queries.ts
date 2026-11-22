import {
  type QueryClient,
  queryOptions,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { api, unwrap } from "@/lib/api";
import { useMe } from "@/lib/auth";
import { queryKeys } from "@/lib/query-keys";
import type {
  CheckoutResponse,
  Meme,
  Plan,
  SubscriptionStatus,
  UploadResponse,
} from "@/lib/types";

export { queryKeys } from "@/lib/query-keys";

export function usePlans() {
  return useQuery({
    queryKey: queryKeys.plans,
    queryFn: async () =>
      unwrap<Plan[]>(await api.get("/api/v1/subscription/plans")) ?? [],
  });
}

export function subscriptionStatusQueryOptions(enabled: boolean) {
  return queryOptions({
    queryKey: queryKeys.subscription,
    queryFn: async () =>
      unwrap<SubscriptionStatus>(await api.get("/api/v1/subscription/status")),
    enabled,
    staleTime: 0,
    refetchOnWindowFocus: true,
    retry: false,
  });
}

export function useSubscriptionStatus() {
  const { data: user } = useMe();
  return useQuery(subscriptionStatusQueryOptions(Boolean(user)));
}

export function invalidateSubscriptionQuery(qc: QueryClient) {
  return qc.invalidateQueries({ queryKey: queryKeys.subscription });
}

export function invalidateMemeQueries(qc: QueryClient) {
  return Promise.all([
    qc.invalidateQueries({ queryKey: ["memes"] }),
    qc.invalidateQueries({ queryKey: queryKeys.subscription }),
  ]);
}

export function useCheckout() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (planCode: string) =>
      unwrap<CheckoutResponse>(
        await api.post("/api/v1/subscription/checkout", { planCode }, {
          headers: {
            // BUG: key dibuat BARU di setiap panggilan mutationFn — artinya
            // setiap klik (termasuk double-click) dianggap maksud berbeda.
            // Key yang benar dibuat sekali per maksud (mis. per plan yang
            // dipilih) lalu dipakai ulang saat retry.
            "Idempotency-Key": crypto.randomUUID(),
          },
        }),
      ),
    onSuccess: () => invalidateSubscriptionQuery(qc),
  });
}

export function useConfirmPayment() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (reference: string) =>
      unwrap(
        await api.post(
          `/api/v1/subscription/payments/${reference}/confirm`,
        ),
      ),
    onSuccess: () => invalidateSubscriptionQuery(qc),
  });
}

export function useMemes(limit = 20, offset = 0) {
  return useQuery({
    queryKey: queryKeys.memes(limit, offset),
    queryFn: async () =>
      unwrap<{ items: Meme[]; total: number }>(
        await api.get("/api/v1/memes", { params: { limit, offset } }),
      ),
  });
}

export function usePrepareUpload() {
  return useMutation({
    mutationFn: async (input: { title: string; filename: string }) =>
      unwrap<UploadResponse>(
        await api.post("/api/v1/memes/upload", input),
      ),
  });
}

export function useCompleteMeme() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (input: { id: string; resultUrl: string }) =>
      unwrap<Meme>(
        await api.post(`/api/v1/memes/${input.id}/complete`, {
          resultUrl: input.resultUrl,
        }),
      ),
    onSuccess: () => invalidateMemeQueries(qc),
  });
}

export function useDeleteMeme() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => api.delete(`/api/v1/memes/${id}`),
    onSuccess: () => invalidateMemeQueries(qc),
  });
}
