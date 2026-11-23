"use client";

// Pricing: daftar paket dari backend; checkout lewat payment gateway mock.
// NOTE (scaffold): belum ada redirect/confirm UI.

import { useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { useCheckout, usePlans, useSubscriptionStatus } from "@/lib/queries";
import { describeError } from "@/lib/errors";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

function formatIdr(n: number) {
  return new Intl.NumberFormat("id-ID", {
    style: "currency",
    currency: "IDR",
    maximumFractionDigits: 0,
  }).format(n);
}

export default function PricingPage() {
  const plans = usePlans();
  const status = useSubscriptionStatus();
  const checkout = useCheckout();
  const router = useRouter();
  const idempotencyKeysRef = useRef<Record<string, string>>({});
  const [submittingPlan, setSubmittingPlan] = useState<string | null>(null);
  const [isNavigating, setIsNavigating] = useState(false);

  const isBusy = checkout.isPending || isNavigating;

  const handleCheckout = (planCode: string) => {
    if (isBusy) return;
    if (!idempotencyKeysRef.current[planCode]) {
      idempotencyKeysRef.current[planCode] = crypto.randomUUID();
    }
    setSubmittingPlan(planCode);
    checkout.mutate(
      { planCode, idempotencyKey: idempotencyKeysRef.current[planCode] },
      {
        onSuccess: (res) => {
          setIsNavigating(true);
          router.push(`/checkout/${res.payment.reference}`);
        },
        onError: () => {
          setSubmittingPlan(null);
        },
      },
    );
  };

  return (
    <main className="mx-auto max-w-4xl space-y-8 p-8">
      <h1 className="text-3xl font-bold">Paket Berlangganan</h1>
      {status.data && (
        <div className="space-y-1 text-sm text-muted-foreground">
          <p>
            Status:{" "}
            {status.data.subscribed
              ? `${status.data.planName} (aktif sampai ${status.data.endsAt})`
              : "belum berlangganan"}
          </p>
          {status.data.subscribed && (
            <p>
              Kuota meme: {status.data.memesUsed}
              {status.data.maxMemes > 0
                ? ` / ${status.data.maxMemes} (sisa ${status.data.maxMemes - status.data.memesUsed})`
                : " / unlimited"}
            </p>
          )}
        </div>
      )}
      {checkout.isError && (
        <p className="text-sm text-destructive">
          {describeError(checkout.error)}
        </p>
      )}
      <div className="grid gap-6 sm:grid-cols-2 lg:grid-cols-3">
        {plans.data?.map((plan) => (
          <Card key={plan.id}>
            <CardHeader>
              <CardTitle>{plan.name}</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3">
              <p className="text-2xl font-bold">{formatIdr(plan.priceIdr)}</p>
              <p className="text-sm text-muted-foreground">
                {plan.durationDays} hari ·{" "}
                {plan.maxMemes === 0 ? "unlimited meme" : `${plan.maxMemes} meme`}
              </p>
              <Button
                className="w-full"
                disabled={isBusy}
                onClick={() => handleCheckout(plan.code)}
              >
                {isBusy && submittingPlan === plan.code
                  ? "Memproses..."
                  : "Berlangganan"}
              </Button>
            </CardContent>
          </Card>
        ))}
      </div>
    </main>
  );
}
