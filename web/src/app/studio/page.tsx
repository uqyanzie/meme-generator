"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api, unwrap } from "@/lib/api";
import { describeError } from "@/lib/errors";
import { useMe } from "@/lib/auth";
import {
  useCompleteMeme,
  useMemes,
  usePrepareUpload,
  useSubscriptionStatus,
} from "@/lib/queries";

// Alur render:
// 1. prepare upload → presigned PUT untuk gambar dasar
// 2. PUT gambar dasar langsung ke Garage (S3)
// 3. render canvas: gambar dasar + teks atas/bawah font Impact
// 4. presign via result-upload → PUT hasil PNG
// 5. complete → simpan URL hasil ke database

const FONT_STACK = "Impact, 'Arial Narrow Bold', sans-serif";

// Family Anton (fallback Impact di Linux) di-resolve dari CSS var next/font.
function canvasFont(size: number): string {
  let anton = "";
  try {
    anton = getComputedStyle(document.documentElement)
      .getPropertyValue("--font-anton")
      .trim();
  } catch {
    /* SSR / non-browser: lewati */
  }
  const stack = anton ? `${FONT_STACK}, ${anton}` : FONT_STACK;
  return `bold ${size}px ${stack}`;
}

function drawMeme(
  canvas: HTMLCanvasElement,
  img: HTMLImageElement,
  top: string,
  bottom: string,
) {
  const maxW = 800;
  const scale = Math.min(1, maxW / img.width);
  canvas.width = Math.round(img.width * scale);
  canvas.height = Math.round(img.height * scale);

  const ctx = canvas.getContext("2d");
  if (!ctx) return;

  ctx.drawImage(img, 0, 0, canvas.width, canvas.height);

  const fontSize = Math.max(24, Math.round(canvas.width / 12));
  ctx.font = canvasFont(fontSize);
  ctx.textAlign = "center";
  // Gaya meme klasik (seperti contoh Impact meme): teks putih kapital,
  // outline hitam tebal.
  ctx.fillStyle = "#fefefe";
  ctx.strokeStyle = "#000";
  ctx.lineWidth = fontSize / 8;
  ctx.lineJoin = "round";

  const margin = fontSize / 3;
  const wrap = (text: string, maxWidth: number): string[] => {
    const lines: string[] = [];
    let line = "";
    for (const word of text.toUpperCase().split(/\s+/)) {
      const candidate = line ? `${line} ${word}` : word;
      if (ctx.measureText(candidate).width > maxWidth && line) {
        lines.push(line);
        line = word;
      } else {
        line = candidate;
      }
    }
    if (line) lines.push(line);
    return lines;
  };

  const maxWidth = canvas.width - margin * 2;
  const topLines = wrap(top, maxWidth);
  const bottomLines = wrap(bottom, maxWidth);

  let y = margin + fontSize * 0.85;
  for (const line of topLines) {
    ctx.strokeText(line, canvas.width / 2, y);
    ctx.fillText(line, canvas.width / 2, y);
    y += fontSize * 1.1;
  }
  y = canvas.height - margin - (bottomLines.length - 1) * fontSize * 1.1;
  for (const line of bottomLines) {
    ctx.strokeText(line, canvas.width / 2, y);
    ctx.fillText(line, canvas.width / 2, y);
    y += fontSize * 1.1;
  }
}

const MAX_UPLOAD_BYTES = 5 * 1024 * 1024;
const EXT_TO_MIME: Record<string, string> = {
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".png": "image/png",
  ".gif": "image/gif",
  ".webp": "image/webp",
};

function validateSelectedFile(
  file: File,
): { ok: true; contentType: string } | { ok: false; error: string } {
  const dotIdx = file.name.lastIndexOf(".");
  const ext = dotIdx >= 0 ? file.name.slice(dotIdx).toLowerCase() : "";
  const expectedMime = EXT_TO_MIME[ext];
  if (!expectedMime) {
    return {
      ok: false,
      error: "Format gambar harus JPG, PNG, GIF, atau WebP.",
    };
  }
  if (file.type) {
    const normalizedType = file.type.split(";")[0].trim().toLowerCase();
    if (normalizedType !== expectedMime) {
      return {
        ok: false,
        error: "Tipe file tidak cocok dengan ekstensi gambar.",
      };
    }
  }
  if (file.size <= 0) {
    return { ok: false, error: "File gambar tidak valid." };
  }
  if (file.size > MAX_UPLOAD_BYTES) {
    return { ok: false, error: "Ukuran file melebihi batas maksimum 5 MB." };
  }
  return { ok: true, contentType: expectedMime };
}

async function putToS3(uploadUrl: string, blob: Blob, contentType: string) {
  const res = await fetch(uploadUrl, {
    method: "PUT",
    body: blob,
    headers: { "Content-Type": contentType },
  });
  if (!res.ok) throw new Error(`upload ke S3 gagal: ${res.status}`);
}

interface ResultUploadData {
  uploadUrl: string;
  baseUrl: string;
}

export default function StudioPage() {
  const me = useMe();
  const status = useSubscriptionStatus();
  const memes = useMemes();
  const prepare = usePrepareUpload();
  const complete = useCompleteMeme();

  const [title, setTitle] = useState("");
  const [top, setTop] = useState("");
  const [bottom, setBottom] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const fileRef = useRef<HTMLInputElement>(null);
  const imgRef = useRef<HTMLImageElement | null>(null);

  // Render ulang preview setiap teks berubah — jangan pakai rAF dari handler
  // onChange, karena closure-nya masih memegang state LAMA (inilah bug teks
  // yang sudah dihapus tapi masih tampil di canvas).
  useEffect(() => {
    renderPreview();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [top, bottom]);

  const renderPreview = () => {
    const canvas = document.getElementById("meme-canvas") as HTMLCanvasElement | null;
    if (!canvas || !imgRef.current) return;
    drawMeme(canvas, imgRef.current, top, bottom);
  };

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError("");
    const file = fileRef.current?.files?.[0];
    if (!file) {
      setError("Pilih gambar dasar dulu.");
      return;
    }
    const validated = validateSelectedFile(file);
    if (!validated.ok) {
      setError(validated.error);
      return;
    }
    setBusy(true);
    try {
      // Pastikan fallback font (Anton) sudah termuat sebelum dirender ke canvas.
      await document.fonts.ready;
      const img = new Image();
      img.src = URL.createObjectURL(file);
      await new Promise<void>((resolve, reject) => {
        img.onload = () => resolve();
        img.onerror = () => reject(new Error("gambar tidak bisa dibaca"));
      });
      imgRef.current = img;

      // 1) presign gambar dasar
      const prepared = await prepare.mutateAsync({
        title,
        filename: file.name,
        contentType: validated.contentType,
        fileSize: file.size,
      });
      // 2) unggah gambar dasar ke Garage
      await putToS3(prepared.uploadUrl, file, validated.contentType);

      // 3) render hasil ke PNG
      const resultCanvas = document.createElement("canvas");
      drawMeme(resultCanvas, img, top, bottom);
      const blob = await new Promise<Blob>((resolve, reject) =>
        resultCanvas.toBlob(
          (b) => (b ? resolve(b) : reject(new Error("render gagal"))),
          "image/png",
        ),
      );
      if (blob.size > MAX_UPLOAD_BYTES) {
        throw new Error("Ukuran hasil render melebihi batas maksimum 5 MB.");
      }

      // 4) presign + unggah hasil
      const result = unwrap<ResultUploadData>(
        await api.post(`/api/v1/memes/${prepared.id}/result-upload`, {
          fileSize: blob.size,
        }),
      );
      await putToS3(result.uploadUrl, blob, "image/png");

      // 5) complete
      await complete.mutateAsync({ id: prepared.id, resultUrl: result.baseUrl });
      memes.refetch();
    } catch (err) {
      setError(describeError(err));
    } finally {
      setBusy(false);
    }
  };

  const subscribed = status.data?.subscribed ?? false;
  const quotaLeft =
    status.data && status.data.maxMemes > 0
      ? status.data.maxMemes - status.data.memesUsed
      : null;

  // Gate: halaman studio hanya untuk user yang sudah berlangganan.
  // Belum selesai dicek → tampilkan kosong dulu agar tidak berkedip.
  if (me.isLoading || (me.data && status.isPending)) {
    return (
      <main className="mx-auto max-w-3xl p-8">
        <p className="text-muted-foreground">Memuat…</p>
      </main>
    );
  }
  if (!me.data) {
    return (
      <main className="mx-auto max-w-3xl space-y-4 p-8">
        <h1 className="text-4xl font-bold" style={{ fontFamily: "var(--font-impact)" }}>
          MEME STUDIO
        </h1>
        <Card>
          <CardContent className="flex flex-col items-center gap-3 p-8 text-center">
            <p>Anda harus masuk untuk mengakses studio.</p>
            <div className="flex gap-2">
              <Button asChild>
                <Link href="/login">Masuk</Link>
              </Button>
              <Button asChild variant="outline">
                <Link href="/register">Daftar</Link>
              </Button>
            </div>
          </CardContent>
        </Card>
      </main>
    );
  }
  if (!subscribed) {
    return (
      <main className="mx-auto max-w-3xl space-y-4 p-8">
        <h1 className="text-4xl font-bold" style={{ fontFamily: "var(--font-impact)" }}>
          MEME STUDIO
        </h1>
        <Card className="border-destructive">
          <CardContent className="flex flex-col items-center gap-3 p-8 text-center">
            <p>
              Studio hanya untuk pelanggan. Berlangganan dulu untuk membuat
              meme.
            </p>
            <Button asChild>
              <Link href="/pricing">Pilih Paket</Link>
            </Button>
          </CardContent>
        </Card>
      </main>
    );
  }

  return (
    <main className="mx-auto max-w-3xl space-y-8 p-8">
      <h1
        className="text-4xl font-bold"
        style={{ fontFamily: "var(--font-impact)" }}
      >
        MEME STUDIO
      </h1>

      {quotaLeft !== null && (
        <p className="text-sm text-muted-foreground">
          Sisa kuota: {quotaLeft} meme
        </p>
      )}

      <Card>
        <CardHeader>
          <CardTitle>Buat Meme</CardTitle>
        </CardHeader>
        <CardContent>
          <form onSubmit={onSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="title">Judul</Label>
              <Input
                id="title"
                value={title}
                onChange={(e) => setTitle(e.target.value)}
                placeholder="Meme hari ini"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="image">Gambar dasar</Label>
              <Input
                id="image"
                type="file"
                accept=".jpg,.jpeg,.png,.gif,.webp,image/jpeg,image/png,image/gif,image/webp"
                ref={fileRef}
                onChange={() => {
                  const file = fileRef.current?.files?.[0];
                  if (!file) return;
                  const validated = validateSelectedFile(file);
                  if (!validated.ok) {
                    setError(validated.error);
                    if (fileRef.current) fileRef.current.value = "";
                    imgRef.current = null;
                    return;
                  }
                  setError("");
                  const img = new Image();
                  img.onload = () => {
                    imgRef.current = img;
                    renderPreview();
                  };
                  img.src = URL.createObjectURL(file);
                }}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="top">Teks atas</Label>
              <Input
                id="top"
                value={top}
                onChange={(e) => setTop(e.target.value)}
                placeholder="WHEN THE SCAFFOLD"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="bottom">Teks bawah</Label>
              <Input
                id="bottom"
                value={bottom}
                onChange={(e) => setBottom(e.target.value)}
                placeholder="BUILDS ON THE FIRST TRY"
              />
            </div>
            <canvas
              id="meme-canvas"
              className="mx-auto max-w-full rounded-md border bg-muted"
            />
            {error && <p className="text-sm text-destructive">{error}</p>}
            <Button type="submit" disabled={busy || prepare.isPending}>
              {busy ? "Memproses…" : "Render & Simpan"}
            </Button>
          </form>
        </CardContent>
      </Card>

      <section className="space-y-2">
        <h2 className="text-lg font-semibold">Meme Saya</h2>
        {memes.data?.items?.length ? (
          <ul className="grid gap-3 sm:grid-cols-2">
            {memes.data.items.map((m) => (
              <li key={m.id} className="space-y-1 rounded-md border p-3 text-sm">
                <p className="font-medium">{m.title || "(tanpa judul)"}</p>
                {m.resultUrl && (
                  // eslint-disable-next-line @next/next/no-img-element
                  <img
                    src={m.resultUrl}
                    alt={m.title}
                    className="max-h-40 w-auto rounded"
                  />
                )}
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-sm text-muted-foreground">Belum ada meme.</p>
        )}
      </section>
    </main>
  );
}
