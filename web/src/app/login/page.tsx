"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect } from "react";
import { useForm } from "@tanstack/react-form";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useLogin, useMe } from "@/lib/auth";
import { apiErrorMessage } from "@/lib/api";

export default function LoginPage() {
  const router = useRouter();
  const login = useLogin();
  const me = useMe();

  useEffect(() => {
    if (me.data && !login.isPending && !login.isSuccess) {
      router.replace("/");
    }
  }, [me.data, login.isPending, login.isSuccess, router]);

  const form = useForm({
    defaultValues: { email: "", password: "" },
    onSubmit: async ({ value }) => {
      await login.mutateAsync(value);
      router.push("/studio");
    },
  });

  return (
    <main className="flex min-h-screen items-center justify-center p-8">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>Masuk</CardTitle>
        </CardHeader>
        <CardContent>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              form.handleSubmit();
            }}
            className="space-y-4"
          >
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
              validators={{ onChange: ({ value }) => (!value ? "Password wajib diisi" : undefined) }}
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

            {login.isError && (
              <p className="text-sm text-destructive">
                {login.error ? apiErrorMessage(login.error, "Login gagal") : "Login gagal"}
              </p>
            )}

            <Button type="submit" className="w-full" disabled={login.isPending}>
              {login.isPending ? "Memproses…" : "Masuk"}
            </Button>
            <p className="text-center text-sm text-muted-foreground">
              Belum punya akun?{" "}
              <Link href="/register" className="underline">
                Daftar
              </Link>
            </p>
          </form>
        </CardContent>
      </Card>
    </main>
  );
}
