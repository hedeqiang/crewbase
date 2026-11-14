// Relay tunnel (features/relay): when this app is opened from a relay, every
// API call goes through an end-to-end encrypted channel to the paired host.
// The handshake mirrors features/relay/crypto.go: ECDH P-256 with the
// host's static key (pinned from the pairing link) and a fresh ephemeral
// key on both sides, HKDF-SHA256 → two AES-256-GCM keys, counter nonces.

type Bytes = Uint8Array<ArrayBuffer>;

const SALT: Bytes = new TextEncoder().encode("crewbase-relay-v1");

export interface RelayHost {
  /** host id on the relay */
  host: string;
  /** the host's static public key, base64url (uncompressed P-256) */
  key: string;
}

const LS_KEY = "crewbase.relay";

export function savedRelay(): RelayHost | null {
  try {
    const v = JSON.parse(localStorage.getItem(LS_KEY) || "null");
    return v?.host && v?.key ? v : null;
  } catch {
    return null;
  }
}

export function saveRelay(r: RelayHost | null) {
  try {
    if (r) localStorage.setItem(LS_KEY, JSON.stringify(r));
    else localStorage.removeItem(LS_KEY);
  } catch {
    /* private mode */
  }
}

function b64urlDecode(s: string): Bytes {
  const b = atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4));
  return Uint8Array.from(b, (c) => c.charCodeAt(0));
}

function concat(...xs: Uint8Array[]): Bytes {
  const out = new Uint8Array(xs.reduce((n, x) => n + x.length, 0));
  let o = 0;
  for (const x of xs) {
    out.set(x, o);
    o += x.length;
  }
  return out;
}

function nonce(n: number): Bytes {
  const b = new Uint8Array(12);
  new DataView(b.buffer).setBigUint64(4, BigInt(n));
  return b;
}

function b64(bytes: Uint8Array): string {
  let s = "";
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}

function unb64(s: string | undefined): Bytes {
  if (!s) return new Uint8Array();
  return Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
}

interface TResp {
  id: number;
  s?: number;
  h?: Record<string, string>;
  b?: string;
  end?: boolean;
}

type Waiter = { onChunk: (r: TResp) => void; onClose: (err: Error) => void };

/** One encrypted connection to the host. Reconnects on demand. */
class Tunnel {
  private ws: WebSocket | null = null;
  private ready: Promise<void> | null = null;
  private sendKey!: CryptoKey;
  private recvKey!: CryptoKey;
  private sendN = 0;
  private recvN = 0;
  private next = 0;
  private waiters = new Map<number, Waiter>();
  // frames must be sealed and opened in order
  private sendChain: Promise<void> = Promise.resolve();
  private recvChain: Promise<void> = Promise.resolve();

  constructor(private relay: RelayHost) {}

  private connect(): Promise<void> {
    if (this.ready) return this.ready;
    this.ready = new Promise<void>((resolve, reject) => {
      const url = `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/v1/client?host=${encodeURIComponent(this.relay.host)}`;
      const ws = new WebSocket(url);
      ws.binaryType = "arraybuffer";
      this.ws = ws;
      let eph: CryptoKeyPair;
      let ephPub: Bytes;
      let shook = false;
      const fail = (err: Error) => {
        this.ready = null;
        this.ws = null;
        for (const w of this.waiters.values()) w.onClose(err);
        this.waiters.clear();
        if (!shook) reject(err);
      };
      ws.onerror = () => fail(new Error("relay connection failed"));
      ws.onclose = (e) => fail(new Error(e.code === 4404 ? "host offline" : "relay connection closed"));
      ws.onopen = async () => {
        eph = await crypto.subtle.generateKey({ name: "ECDH", namedCurve: "P-256" }, false, ["deriveBits"]);
        ephPub = new Uint8Array(await crypto.subtle.exportKey("raw", eph.publicKey));
        ws.send(concat(new Uint8Array([1]), ephPub));
      };
      ws.onmessage = async (m) => {
        const data: Bytes = new Uint8Array(m.data as ArrayBuffer);
        if (!shook) {
          try {
            if (data.length !== 66 || data[0] !== 2) throw new Error("bad host hello");
            const hostStatic = b64urlDecode(this.relay.key);
            const eH: Bytes = data.slice(1);
            const imp = (raw: Bytes) => crypto.subtle.importKey("raw", raw, { name: "ECDH", namedCurve: "P-256" }, false, []);
            const s1 = new Uint8Array(await crypto.subtle.deriveBits({ name: "ECDH", public: await imp(hostStatic) }, eph.privateKey, 256));
            const s2 = new Uint8Array(await crypto.subtle.deriveBits({ name: "ECDH", public: await imp(eH) }, eph.privateKey, 256));
            const ikm = await crypto.subtle.importKey("raw", concat(s1, s2), "HKDF", false, ["deriveBits"]);
            const okm = new Uint8Array(
              await crypto.subtle.deriveBits({ name: "HKDF", hash: "SHA-256", salt: SALT, info: concat(ephPub, eH, hostStatic) }, ikm, 512),
            );
            this.sendKey = await crypto.subtle.importKey("raw", okm.slice(0, 32), "AES-GCM", false, ["encrypt"]);
            this.recvKey = await crypto.subtle.importKey("raw", okm.slice(32), "AES-GCM", false, ["decrypt"]);
            this.sendN = this.recvN = 0;
            shook = true;
            resolve();
          } catch (e: any) {
            ws.close();
            fail(e);
          }
          return;
        }
        // decrypt strictly in arrival order
        this.recvChain = this.recvChain.then(async () => {
          try {
            const plain = await crypto.subtle.decrypt(
              { name: "AES-GCM", iv: nonce(this.recvN++), additionalData: new Uint8Array([104]) },
              this.recvKey,
              data,
            );
            const r: TResp = JSON.parse(new TextDecoder().decode(plain));
            this.waiters.get(r.id)?.onChunk(r);
          } catch {
            ws.close(); // tampered: drop the connection
          }
        });
      };
    });
    return this.ready;
  }

  private async sendJSON(v: unknown) {
    await this.connect();
    const ws = this.ws!;
    const plain = new TextEncoder().encode(JSON.stringify(v));
    const job = this.sendChain.then(async () => {
      const sealed = await crypto.subtle.encrypt({ name: "AES-GCM", iv: nonce(this.sendN++), additionalData: new Uint8Array([100]) }, this.sendKey, plain);
      ws.send(sealed);
    });
    this.sendChain = job.catch(() => {});
    return job;
  }

  /** Streams a request; onChunk gets the head first, then body pieces. */
  async request(method: string, path: string, headers: Record<string, string>, body: Bytes | null, w: Waiter, signal?: AbortSignal) {
    const id = ++this.next;
    this.waiters.set(id, {
      onChunk: (r) => {
        w.onChunk(r);
        if (r.end) this.waiters.delete(id);
      },
      onClose: w.onClose,
    });
    signal?.addEventListener("abort", () => {
      this.waiters.delete(id);
      void this.sendJSON({ id, cancel: true }).catch(() => {});
    });
    try {
      await this.sendJSON({ id, m: method, p: path, h: headers, b: body ? b64(body) : undefined });
    } catch (e: any) {
      this.waiters.delete(id);
      w.onClose(e instanceof Error ? e : new Error(String(e)));
    }
  }
}

let tunnel: Tunnel | null = null;

/** Turns on the tunnel for every API call in this page. */
export function useRelay(r: RelayHost) {
  tunnel = new Tunnel(r);
}

export function viaRelay(): boolean {
  return tunnel !== null;
}

/** fetch() replacement: direct, or through the tunnel. */
export async function apiFetch(path: string, init: { method?: string; headers?: Record<string, string>; body?: BodyInit | null; signal?: AbortSignal } = {}): Promise<Response> {
  if (!tunnel) return fetch(path, init);
  let body: Bytes | null = null;
  if (init.body != null) body = new Uint8Array(await new Response(init.body).arrayBuffer());
  return new Promise<Response>((resolve, reject) => {
    let status = 0;
    let headers: Record<string, string> = {};
    const parts: Bytes[] = [];
    void tunnel!.request(
      init.method ?? "GET",
      path,
      init.headers ?? {},
      body,
      {
        onChunk: (r) => {
          if (r.s) {
            status = r.s;
            headers = r.h ?? {};
          }
          if (r.b) parts.push(unb64(r.b));
          if (r.end) resolve(new Response(concat(...parts), { status, headers }));
        },
        onClose: reject,
      },
      init.signal,
    );
  });
}

/** Server-sent events through the tunnel (EventSource cannot be tunnelled). */
export function tunnelEvents(path: string, token: string, on: (type: string, data: string) => void, onError: () => void): () => void {
  const ac = new AbortController();
  let buf = "";
  const dec = new TextDecoder();
  void tunnel!.request(
    "GET",
    path,
    { Authorization: `Bearer ${token}`, Accept: "text/event-stream" },
    null,
    {
      onChunk: (r) => {
        if (r.s && r.s !== 200) {
          onError();
          return;
        }
        if (r.b) {
          buf += dec.decode(unb64(r.b), { stream: true });
          let i: number;
          while ((i = buf.indexOf("\n\n")) >= 0) {
            const block = buf.slice(0, i);
            buf = buf.slice(i + 2);
            let type = "message";
            const data: string[] = [];
            for (const line of block.split("\n")) {
              if (line.startsWith("event: ")) type = line.slice(7);
              else if (line.startsWith("data: ")) data.push(line.slice(6));
            }
            if (data.length) on(type, data.join("\n"));
          }
        }
        if (r.end) onError();
      },
      onClose: () => onError(),
    },
    ac.signal,
  );
  return () => ac.abort();
}

/** Object URL for a workspace file fetched through the tunnel. */
export async function tunnelBlobURL(path: string, token: string): Promise<string> {
  const r = await apiFetch(path, { headers: { Authorization: `Bearer ${token}` } });
  if (!r.ok) throw new Error("download failed");
  return URL.createObjectURL(await r.blob());
}
