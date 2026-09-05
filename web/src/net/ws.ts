import { ReqEnvelope, RespEnvelope } from "../proto/messages";

type RespListener = (env: RespEnvelope) => void;

export class WSClient {
  private ws?: WebSocket;
  private nextId = 1;
  private waiters = new Map<number, (env: RespEnvelope) => void>();
  private listeners: Set<RespListener> = new Set();
  public connected = false;
  // Two components mounting at once (e.g. the app-level fresh-device check
  // alongside a component's own first request) both used to call connect()
  // concurrently. The old readyState-based guard only stopped the second
  // caller from opening a *second* socket — it didn't make that caller
  // actually wait for the first one to finish, so it returned as if
  // already connected while the real socket was still CONNECTING, and its
  // immediately-following request() failed with "WS not connected". Every
  // caller now awaits this same in-flight promise instead.
  private connecting?: Promise<void>;

  onMessage(fn: RespListener) {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  connect(url: string): Promise<void> {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) return Promise.resolve();
    if (this.connecting) return this.connecting;

    this.connecting = new Promise<void>((resolve, reject) => {
      console.log('WS to endpoint:', url);
      const ws = new WebSocket(url);
      ws.binaryType = "arraybuffer";

      ws.onopen = () => { this.connected = true; this.connecting = undefined; resolve(); };
      ws.onerror = (e) => { this.connecting = undefined; reject(e); };
      ws.onclose = () => { this.connected = false; this.connecting = undefined; };
      ws.onmessage = (ev) => {
        try {
          const env = RespEnvelope.decode(new Uint8Array(ev.data as ArrayBuffer));
          const cont = this.waiters.get(env.id);
          if (cont) { this.waiters.delete(env.id); cont(env); }
          this.listeners.forEach(fn => fn(env));
        } catch (err) {
          console.error("WS decode error", err);
        }
      };

      this.ws = ws;
    });
    return this.connecting;
  }

  close() { this.ws?.close(); }

  request(build: (e: Partial<ReqEnvelope>) => void): Promise<RespEnvelope> {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      return Promise.reject(new Error("WS not connected"));
    }
    const id = this.nextId++;
    const draft: Partial<ReqEnvelope> = { id };
    build(draft);
    const req = ReqEnvelope.fromPartial(draft);
    const bytes = ReqEnvelope.encode(req).finish();
    this.ws.send(bytes);
    return new Promise<RespEnvelope>((resolve) => this.waiters.set(id, resolve));
  }
}

export const wsClient = new WSClient();
