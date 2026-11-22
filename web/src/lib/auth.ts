import { type QueryClient, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import type { ApiEnvelope } from "@/lib/types";

export interface AuthUser {
  id: string;
  email: string;
  name: string;
}

export interface AuthResponse {
  user: AuthUser;
}

interface AuthInput {
  email: string;
  password: string;
  name?: string;
}

export function invalidateAuthQueries(qc: QueryClient) {
  return Promise.all([
    qc.invalidateQueries({ queryKey: queryKeys.me }),
    qc.invalidateQueries({ queryKey: queryKeys.subscription }),
    qc.invalidateQueries({ queryKey: ["memes"] }),
  ]);
}

export function useMe() {
  return useQuery({
    queryKey: queryKeys.me,
    queryFn: async () => unwrap<AuthUser>(await api.get("/api/v1/auth/me")),
    retry: false,
  });
}

export function useRegister() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (input: AuthInput) => {
      const res = await api.post<ApiEnvelope<AuthResponse>>(
        "/api/v1/auth/register",
        { name: input.name, email: input.email, password: input.password },
      );
      return res.data;
    },
    onSuccess: () => invalidateAuthQueries(qc),
  });
}

export function useLogin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (input: Omit<AuthInput, "name">) => {
      const res = await api.post<ApiEnvelope<AuthResponse>>(
        "/api/v1/auth/login",
        input,
      );
      return res.data;
    },
    onSuccess: () => invalidateAuthQueries(qc),
  });
}

export function useLogout() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => api.post("/api/v1/auth/logout"),
    onSettled: () => {
      qc.clear();
      window.location.href = "/";
    },
  });
}
