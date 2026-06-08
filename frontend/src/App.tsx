import { useEffect, useMemo, useState } from "react";
import * as api from "./api";
import type { DashboardUpdate, EventRec } from "./api";

type Session = { token: string; userId: string; role: string } | null;

const money = (c: number) => `$${(c / 100).toFixed(2)}`;

export default function App() {
  const [session, setSession] = useState<Session>(null);
  const [events, setEvents] = useState<EventRec[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [error, setError] = useState<string>("");

  const refresh = async () => {
    try {
      setEvents(await api.listEvents(session?.token));
    } catch (e) {
      setError(String(e));
    }
  };

  useEffect(() => {
    refresh();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session]);

  return (
    <div style={styles.page}>
      <header style={styles.header}>
        <h1 style={{ margin: 0 }}>🎟️ Event Ticketing</h1>
        <Auth session={session} onSession={setSession} />
      </header>

      {error && <div style={styles.error}>{error}</div>}

      <div style={styles.columns}>
        <section style={styles.col}>
          <h2>Events</h2>
          <EventList
            events={events}
            session={session}
            onChanged={refresh}
            onSelect={setSelected}
            selected={selected}
            onError={setError}
          />
          {session?.role !== "buyer" && session && (
            <CreateEvent token={session.token} onCreated={refresh} onError={setError} />
          )}
        </section>

        <section style={styles.col}>
          <h2>Live dashboard</h2>
          {selected ? (
            <Dashboard eventId={selected} />
          ) : (
            <p style={{ color: "#888" }}>Select an event to watch live sales.</p>
          )}
        </section>
      </div>
    </div>
  );
}

function Auth({ session, onSession }: { session: Session; onSession: (s: Session) => void }) {
  const [email, setEmail] = useState("organizer@demo.com");
  const [password, setPassword] = useState("hunter2");
  const [role, setRole] = useState("organizer");
  const [err, setErr] = useState("");

  if (session) {
    return (
      <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
        <span>
          {role || session.role} · <code>{session.userId.slice(0, 8)}</code>
        </span>
        <button onClick={() => onSession(null)}>Log out</button>
      </div>
    );
  }

  const submit = async (fn: () => Promise<{ token: string; user_id: string; role: string }>) => {
    setErr("");
    try {
      const r = await fn();
      onSession({ token: r.token, userId: r.user_id, role: r.role });
    } catch (e) {
      setErr(String(e));
    }
  };

  return (
    <div style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap" }}>
      <input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="email" />
      <input
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        placeholder="password"
        type="password"
      />
      <select value={role} onChange={(e) => setRole(e.target.value)}>
        <option value="organizer">organizer</option>
        <option value="buyer">buyer</option>
      </select>
      <button onClick={() => submit(() => api.register(email, password, role))}>Register</button>
      <button onClick={() => submit(() => api.login(email, password))}>Login</button>
      {err && <span style={{ color: "crimson" }}>{err}</span>}
    </div>
  );
}

function EventList({
  events,
  session,
  onChanged,
  onSelect,
  selected,
  onError,
}: {
  events: EventRec[];
  session: Session;
  onChanged: () => void;
  onSelect: (id: string) => void;
  selected: string | null;
  onError: (e: string) => void;
}) {
  if (events.length === 0) return <p style={{ color: "#888" }}>No events yet.</p>;
  return (
    <div>
      {events.map((e) => (
        <EventCard
          key={e.id}
          ev={e}
          session={session}
          onChanged={onChanged}
          onSelect={onSelect}
          selected={selected === e.id}
          onError={onError}
        />
      ))}
    </div>
  );
}

function EventCard({
  ev,
  session,
  onChanged,
  onSelect,
  selected,
  onError,
}: {
  ev: EventRec;
  session: Session;
  onChanged: () => void;
  onSelect: (id: string) => void;
  selected: boolean;
  onError: (e: string) => void;
}) {
  const [detail, setDetail] = useState<EventRec | null>(null);
  const [busy, setBusy] = useState(false);

  const load = async () => {
    try {
      setDetail(await api.getEvent(ev.id));
    } catch (e) {
      onError(String(e));
    }
  };
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ev.id]);

  const buy = async (tierId: string) => {
    if (!session) return onError("log in to buy");
    setBusy(true);
    try {
      const co = await api.checkout(session.token, tierId, 1);
      // Test mode: simulate Stripe delivering the success webhook.
      await api.simulatePaymentSuccess(co.payment_intent_id);
      await load();
      onChanged();
    } catch (e) {
      onError(String(e));
    } finally {
      setBusy(false);
    }
  };

  const publish = async () => {
    if (!session) return;
    await api.publishEvent(session.token, ev.id);
    onChanged();
  };

  const d = detail ?? ev;
  return (
    <div style={{ ...styles.card, outline: selected ? "2px solid #4f46e5" : "none" }}>
      <div style={{ display: "flex", justifyContent: "space-between" }}>
        <strong>{d.title}</strong>
        <span style={styles.badge}>{d.state}</span>
      </div>
      <div style={{ color: "#666", fontSize: 13 }}>
        {d.category} · {d.venue || "TBA"}
      </div>
      <div style={{ marginTop: 8 }}>
        {(d.tiers ?? []).map((t) => (
          <div key={t.id} style={styles.tier}>
            <span>
              {t.name} — {money(t.price_cents)} ({t.remaining}/{t.capacity} left)
            </span>
            <button disabled={busy || t.remaining <= 0} onClick={() => buy(t.id)}>
              {t.remaining <= 0 ? "Sold out" : "Buy"}
            </button>
          </div>
        ))}
      </div>
      <div style={{ display: "flex", gap: 8, marginTop: 8 }}>
        <button onClick={() => onSelect(ev.id)}>Watch live</button>
        {session && session.role !== "buyer" && d.state === "draft" && (
          <button onClick={publish}>Publish</button>
        )}
      </div>
    </div>
  );
}

function CreateEvent({
  token,
  onCreated,
  onError,
}: {
  token: string;
  onCreated: () => void;
  onError: (e: string) => void;
}) {
  const [title, setTitle] = useState("");
  const [price, setPrice] = useState(2500);
  const [capacity, setCapacity] = useState(100);

  const create = async () => {
    try {
      const ev = await api.createEvent(token, { title, category: "music", venue: "Demo Hall" });
      await api.createTier(token, ev.id, { name: "GA", price_cents: price, capacity });
      await api.publishEvent(token, ev.id);
      setTitle("");
      onCreated();
    } catch (e) {
      onError(String(e));
    }
  };

  return (
    <div style={{ ...styles.card, marginTop: 12 }}>
      <strong>Create event</strong>
      <div style={{ display: "flex", gap: 6, flexWrap: "wrap", marginTop: 6 }}>
        <input placeholder="title" value={title} onChange={(e) => setTitle(e.target.value)} />
        <input
          type="number"
          value={price}
          onChange={(e) => setPrice(Number(e.target.value))}
          style={{ width: 90 }}
        />
        <input
          type="number"
          value={capacity}
          onChange={(e) => setCapacity(Number(e.target.value))}
          style={{ width: 90 }}
        />
        <button disabled={!title} onClick={create}>
          Create + publish
        </button>
      </div>
    </div>
  );
}

function Dashboard({ eventId }: { eventId: string }) {
  const [update, setUpdate] = useState<DashboardUpdate | null>(null);
  const [connected, setConnected] = useState(false);

  useEffect(() => {
    const ws = api.openDashboard(eventId, setUpdate);
    ws.onopen = () => setConnected(true);
    ws.onclose = () => setConnected(false);
    return () => ws.close();
  }, [eventId]);

  const tiers = useMemo(() => update?.tiers ?? [], [update]);

  return (
    <div style={styles.card}>
      <div style={{ display: "flex", justifyContent: "space-between" }}>
        <strong>Event {eventId.slice(0, 8)}</strong>
        <span style={{ color: connected ? "green" : "#aaa" }}>
          {connected ? "● live" : "○ connecting"}
        </span>
      </div>
      {update ? (
        <div style={{ marginTop: 8 }}>
          <div style={styles.metrics}>
            <Metric label="Revenue" value={money(update.total_revenue_cents)} />
            <Metric label="Tickets sold" value={String(update.tickets_sold)} />
            <Metric label="Velocity" value={`${update.sales_velocity_per_min.toFixed(1)}/min`} />
          </div>
          <table style={{ width: "100%", marginTop: 10, borderCollapse: "collapse" }}>
            <thead>
              <tr>
                <th style={styles.th}>Tier</th>
                <th style={styles.th}>Sold</th>
                <th style={styles.th}>Remaining</th>
              </tr>
            </thead>
            <tbody>
              {tiers.map((t) => (
                <tr key={t.tier_id}>
                  <td style={styles.td}>{t.name}</td>
                  <td style={styles.td}>{t.sold}</td>
                  <td style={styles.td}>{t.remaining}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <p style={{ color: "#888" }}>Waiting for the first sale…</p>
      )}
    </div>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div style={styles.metric}>
      <div style={{ fontSize: 12, color: "#666" }}>{label}</div>
      <div style={{ fontSize: 22, fontWeight: 700 }}>{value}</div>
    </div>
  );
}

const styles: Record<string, React.CSSProperties> = {
  page: { fontFamily: "system-ui, sans-serif", maxWidth: 960, margin: "0 auto", padding: 16 },
  header: {
    display: "flex",
    justifyContent: "space-between",
    alignItems: "center",
    flexWrap: "wrap",
    gap: 8,
  },
  columns: { display: "flex", gap: 16, marginTop: 16, flexWrap: "wrap" },
  col: { flex: "1 1 380px", minWidth: 320 },
  card: { border: "1px solid #e5e7eb", borderRadius: 10, padding: 12, marginBottom: 10 },
  badge: { fontSize: 12, background: "#eef2ff", color: "#4f46e5", padding: "2px 8px", borderRadius: 999 },
  tier: { display: "flex", justifyContent: "space-between", alignItems: "center", padding: "4px 0" },
  error: { background: "#fef2f2", color: "#991b1b", padding: 8, borderRadius: 8, marginTop: 8 },
  metrics: { display: "flex", gap: 12 },
  metric: { flex: 1, background: "#f9fafb", borderRadius: 8, padding: 10 },
  th: { textAlign: "left", borderBottom: "1px solid #e5e7eb", padding: 4, fontSize: 13 },
  td: { borderBottom: "1px solid #f3f4f6", padding: 4, fontSize: 14 },
};
