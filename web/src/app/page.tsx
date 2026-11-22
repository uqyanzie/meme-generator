import Link from "next/link";
import { Button } from "@/components/ui/button";

export default function Home() {
  return (
    <main className="flex min-h-screen flex-col items-center justify-center gap-8 p-8">
      <h1
        className="text-6xl font-bold tracking-tight text-center"
        style={{ fontFamily: "var(--font-impact)" }}
      >
        MEME PORTAL
      </h1>
      <p className="text-muted-foreground text-center max-w-md">
        Meme generator berlangganan. Upload gambar dasar, tulis teks dengan
        font Impact, dan bagikan hasilnya.
      </p>
      <div className="flex gap-4">
        <Button asChild>
          <Link href="/studio">Buat Meme</Link>
        </Button>
        <Button asChild variant="outline">
          <Link href="/pricing">Lihat Paket</Link>
        </Button>
      </div>
    </main>
  );
}
