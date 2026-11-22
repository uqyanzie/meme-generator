"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect } from "react";
import { useForm } from "@tanstack/react-form";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useMe, useRegister } from "@/lib/auth";
import { apiErrorMessage } from "@/lib/api";

export default function RegisterPage() {
  const router = useRouter();
  const register = useRegister();
  const me = useMe();

  useEffect(() => {
    if (me.data && !register.isPending && !register.isSuccess) {
      router.replace("/");
    }
  }, [me.data, register.isPending, register.isSuccess, router]);

  const form = useForm({
    defaultValues: { name: "", email: "", password: "" },
    onSubmit: async ({ value }) => {
      await register.mutateAsync(value);
      router.push("/pricing"); // setelah daftar, ajak berlangganan dulu
    },
  });

  return (
    <main className="flex min-h-screen items-center justify-center p-8">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>Daftar</CardTitle>
        </CardHeader>
        <CardContent>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              form.handleSubmit();
            }}
            className="space-y-4"
          >
            <form.Field name="name">
              {(field) => (
                <div className="space-y-2">
                  <Label htmlFor={field.name}>Nama</Label>
                  <Input
                    id={field.name}
                    value={field.state.value}
                    onChange={(e) => field.handleChange(e.target.value)}
                    onBlur={field.handleBlur}
                  />
                </div>
              )}
            </form.Field>

            <form.Field
              name="email"
              validators={{ onChange: ({ value }) => (!value ? "Email wajib diisi" : undefined) }}
            >
              {(field) => (
                <div className="space-y-2">
                  <Label htmlFor={field.name}>Email</Label>
                  <Input
                    id={field.name}
                    type="email"
                    value={field.state.value}
                    onChange={(e) => field.handleChange(e.target.value)}
                    onBlur={field.handleBlur}
                  />
                  {field.state.meta.errors.length > 0 && (
                    <p className="text-sm text-destructive">
                      {field.state.meta.errors[0]}
                    </p>
                  )}
                </div>
              )}
            </form.Field>

            <form.Field
              name="password"
              validators={{
                onChange: ({ value }) =>
                  value.length < 8 ? "Password minimal 8 karakter" : undefined,
              }}
            >
              {(field) => (
                <div className="space-y-2">
                  <Label htmlFor={field.name}>Password</Label>
                  <Input
                    id={field.name}
                    type="password"
                    value={field.state.value}
                    onChange={(e) => field.handleChange(e.target.value)}
                    onBlur={field.handleBlur}
                  />
                  {field.state.meta.errors.length > 0 && (
                    <p className="text-sm text-destructive">
                      {field.state.meta.errors[0]}
                    </p>
                  )}
                </div>
              )}
            </form.Field>

            {register.isError && (
              <p className="text-sm text-destructive">
                {register.error
                  ? apiErrorMessage(register.error, "Pendaftaran gagal")
                  : "Pendaftaran gagal"}
              </p>
            )}

            <Button type="submit" className="w-full" disabled={register.isPending}>
              {register.isPending ? "Memproses…" : "Daftar"}
            </Button>
            <p className="text-center text-sm text-muted-foreground">
              Sudah punya akun?{" "}
              <Link href="/login" className="underline">
                Masuk
              </Link>
            </p>
          </form>
        </CardContent>
      </Card>
    </main>
  );
}
