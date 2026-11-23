"use client";

// Halaman simulasi pembayaran (mock payment gateway).
// Di produksi halaman ini digantikan halaman payment gateway sungguhan
// yang memanggil webhook /confirm dengan verifikasi signature.

import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useEffect, useRef } from "react";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { describeError } from "@/lib/errors";
import { useConfirmPayment } from "@/lib/queries";

function formatIdr(n: number) {
  return new Intl.NumberFormat("id-ID", {
    style: "currency",
    currency: "IDR",
    maximumFractionDigits: 0,
  }).format(n);
}

export default function CheckoutPage() {
  const { reference } = useParams<{ reference: string }>();
  const router = useRouter();
  const confirm = useConfirmPayment();
  const redirectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (redirectTimerRef.current) {
        clearTimeout(redirectTimerRef.current);
      }
    };
  }, []);

  const onPay = () => {
    if (confirm.isPending || confirm.data) return;
    confirm.mutate(reference, {
      onSuccess: () => {
        redirectTimerRef.current = setTimeout(() => {
          router.push("/pricing");
        }, 1200);
      },
    });
  };

  return (
    <main className="mx-auto flex min-h-[70vh] max-w-md items-center p-8">
      <Card className="w-full">
        <CardHeader>
          <CardTitle>Simulasi Pembayaran</CardTitle>
          <CardDescription>
            Payment gateway mock — tidak ada uang sungguhan.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex justify-between text-sm">
            <span className="text-muted-foreground">Referensi</span>
            <span className="font-mono">{reference}</span>
          </div>
          {confirm.data ? (
            <p className="text-center text-sm font-medium text-green-600">
              ✅ Pembayaran berhasil! Langganan Anda aktif. Mengalihkan…
            </p>
          ) : null}
          {confirm.isError ? (
            <p
              role="alert"
              className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
            >
              {describeError(confirm.error)}
            </p>
          ) : null}
        </CardContent>
        <CardFooter className="flex gap-2">
          <Button
            className="flex-1"
            disabled={confirm.isPending || Boolean(confirm.data)}
            aria-busy={confirm.isPending}
            onClick={onPay}
          >
            {confirm.data
              ? "Lunas"
              : confirm.isPending
                ? "Memproses..."
                : "Bayar Sekarang (Simulasi)"}
          </Button>
          <Button asChild variant="ghost">
            <Link href="/pricing">Batal</Link>
          </Button>
        </CardFooter>
      </Card>
    </main>
  );
}
