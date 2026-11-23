// Ubah error apa pun (axios / network / Error biasa) menjadi pesan yang
// jelas untuk ditampilkan di UI, termasuk kode status HTTP.
export function describeError(err: unknown): string {
  const e = err as {
    response?: { status?: number; data?: { message?: string; error?: string } };
    message?: string;
    code?: string;
  };

  // 1) Server merespons dengan status non-2xx → tampilkan status + pesan.
  if (e?.response) {
    const status = e.response.status ?? 0;
    const msg = e.response.data?.message ?? e.response.data?.error;
    const label: Record<number, string> = {
      400: "Permintaan tidak valid",
      401: "Sesi habis / belum login",
      402: "Pembayaran tidak dapat diproses",
      403: "Tidak diizinkan (subscription tidak aktif?)",
      404: "Resource tidak ditemukan",
      405: "Endpoint/method tidak didukung server",
      409: "Konflik data",
      413: "File terlalu besar",
      422: "Data tidak valid",
      429: "Terlalu banyak permintaan",
      500: "Kesalahan server internal",
      502: "Server upstream bermasalah",
      503: "Server sedang tidak tersedia",
    };
    const base = label[status] ?? `HTTP ${status}`;
    return msg ? `${base}: ${msg}` : base;
  }

  // 2) Request terkirim tapi tidak ada respons (timeout/CORS/refused).
  if (e?.code === "ECONNREFUSED" || e?.message === "Network Error") {
    return "Tidak bisa terhubung ke server API — pastikan backend berjalan";
  }

  return e?.message || "Terjadi kesalahan";
}
