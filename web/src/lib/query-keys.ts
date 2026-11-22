export const queryKeys = {
  me: ["me"] as const,
  plans: ["plans"] as const,
  subscription: ["subscription"] as const,
  memes: (limit: number, offset: number) => ["memes", limit, offset] as const,
};
