// Thin API client for the Go backend. In dev, Vite proxies these paths to :8080.

export interface Tier {
  id: string;
  name: string;
  price_cents: number;
  capacity: number;
  sold: number;
  remaining: number;
}

export interface EventRec {
  id: string;
  title: string;
  description: string;
  category: string;
  venue: string;
  state: string;
  tiers?: Tier[];
}

export interface DashboardUpdate {
  event_id: string;
  total_revenue_cents: number;
  tickets_sold: number;
  sales_velocity_per_min: number;
  tiers: { tier_id: string; name: string; sold: number; remaining: number; capacity: number }[];
  at: string;
}

async function json<T>(res: Response): Promise<T> {
  if (!res.ok) throw new Error((await res.text()) || res.statusText);
  return res.json() as Promise<T>;
}

export async function register(email: string, password: string, role: string) {
  return json<{ token: string; user_id: string; role: string }>(
    await fetch("/auth/register", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email, password, role }),
    }),
  );
}

export async function login(email: string, password: string) {
  return json<{ token: string; user_id: string; role: string }>(
    await fetch("/auth/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email, password }),
    }),
  );
}

export async function listEvents(token?: string) {
  const headers: Record<string, string> = {};
  if (token) headers.Authorization = `Bearer ${token}`;
  return json<EventRec[]>(await fetch("/events", { headers }));
}

export async function getEvent(id: string) {
  return json<EventRec>(await fetch(`/events/${id}`));
}

export async function createEvent(token: string, body: Record<string, unknown>) {
  return json<EventRec>(
    await fetch("/events", {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
      body: JSON.stringify(body),
    }),
  );
}

export async function createTier(token: string, eventId: string, body: Record<string, unknown>) {
  return json<Tier>(
    await fetch(`/events/${eventId}/tiers`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
      body: JSON.stringify(body),
    }),
  );
}

export async function publishEvent(token: string, eventId: string) {
  return fetch(`/events/${eventId}/publish`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}` },
  });
}

export async function checkout(token: string, tierId: string, quantity = 1) {
  return json<{ payment_intent_id: string; amount_cents: number; provider: string }>(
    await fetch("/checkout", {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
      body: JSON.stringify({ tier_id: tierId, quantity }),
    }),
  );
}

// Simulates Stripe delivering a payment_intent.succeeded webhook. In production
// Stripe calls this; the button exists so the demo can complete a purchase in
// test mode without real Stripe.
export async function simulatePaymentSuccess(intentId: string) {
  return fetch("/webhooks/stripe", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      type: "payment_intent.succeeded",
      data: { object: { id: intentId } },
    }),
  });
}

export function openDashboard(eventId: string, onUpdate: (u: DashboardUpdate) => void): WebSocket {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  const ws = new WebSocket(`${proto}://${location.host}/ws/events/${eventId}`);
  ws.onmessage = (ev) => {
    try {
      onUpdate(JSON.parse(ev.data) as DashboardUpdate);
    } catch {
      /* ignore non-JSON frames */
    }
  };
  return ws;
}
