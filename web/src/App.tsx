import type { FormEvent, ReactNode } from "react";
import { useCallback, useEffect, useState } from "react";
import { APIError, errorMessage, request } from "./client";
import { usePager, usePolling, useRoute } from "./hooks";
import type {
  AttemptPage,
  CyclePage,
  Endpoint,
  EndpointPage,
  EventDetail,
  EventPage,
  Session,
  Stats,
} from "./types";

const PAGE_SIZE = 25;
const date = (value: string | null | undefined) =>
  value ? new Date(value).toLocaleString() : "Unavailable";
const short = (id: string) => id.slice(0, 8);
const paged = (path: string, cursor: string | null) =>
  `${path}${path.includes("?") ? "&" : "?"}limit=${PAGE_SIZE}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""}`;

function Badge({ status }: { status: string }) {
  return (
    <span className={`badge ${status}`}>{status.replaceAll("_", " ")}</span>
  );
}
function Notice({
  children,
  tone = "error",
}: {
  children: ReactNode;
  tone?: string;
}) {
  return (
    <div
      className={`notice ${tone}`}
      role={tone === "error" ? "alert" : "status"}
    >
      {children}
    </div>
  );
}
function ReadStatus({
  loading,
  error,
  stale,
  updatedAt,
  refresh,
}: {
  loading: boolean;
  error?: string;
  stale: boolean;
  updatedAt?: Date;
  refresh: () => void;
}) {
  return (
    <>
      <div className="read-status">
        <span>
          {loading
            ? "Loading…"
            : stale
              ? "Stale · refresh needed"
              : updatedAt
                ? `Updated ${updatedAt.toLocaleTimeString()} · polls every 5s`
                : "Waiting for data"}
        </span>
        <button
          className="quiet"
          type="button"
          onClick={refresh}
          disabled={loading}
        >
          Refresh
        </button>
      </div>
      {error && <Notice>{error}</Notice>}
    </>
  );
}
function PageButtons({
  nextCursor,
  pager,
  loading,
}: {
  nextCursor?: string | null;
  pager: ReturnType<typeof usePager>;
  loading: boolean;
}) {
  return (
    <div className="pagination">
      <span>{PAGE_SIZE} per page · live results</span>
      <div>
        <button
          type="button"
          className="quiet"
          disabled={!pager.canGoBack || loading}
          onClick={pager.previous}
        >
          Previous
        </button>
        <button
          type="button"
          className="quiet"
          disabled={!nextCursor || loading}
          onClick={() => pager.next(nextCursor ?? null)}
        >
          Next
        </button>
      </div>
    </div>
  );
}

function Login({ ready }: { ready: (session: Session) => void }) {
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (pending) return;
    const entered = token;
    setToken("");
    setError("");
    setPending(true);
    try {
      ready(
        await request<Session>("/login", {
          method: "POST",
          body: { token: entered },
        }),
      );
    } catch (error) {
      setError(errorMessage(error));
    } finally {
      setPending(false);
    }
  };
  return (
    <main className="login">
      <div className="login-card">
        <div className="wordmark">
          <span className="brand-icon">↗</span> EventRelay
        </div>
        <p className="eyebrow">DELIVERY CONSOLE</p>
        <h1>
          See what arrived.
          <br />
          Know what happens next.
        </h1>
        <p className="muted">
          Inspect delivery history and recover an endpoint from one local
          console.
        </p>
        <form
          onSubmit={(event) => {
            void submit(event);
          }}
        >
          <label htmlFor="admin-token">Management token</label>
          <input
            id="admin-token"
            type="password"
            value={token}
            onChange={(event) => setToken(event.target.value)}
            autoComplete="off"
            spellCheck={false}
            required
            disabled={pending}
          />
          <button type="submit" disabled={pending}>
            {pending ? "Signing in…" : "Open console"}
          </button>
        </form>
        {error && <Notice>{error}</Notice>}
        <p className="fine">
          A 30-minute browser session. Credentials stay out of browser storage.
        </p>
      </div>
      <div className="login-decoration" aria-hidden="true">
        <span>EVENT → QUEUE → ATTEMPT</span>
        <div className="relay-line" />
        <span>POSTGRESQL HOLDS THE HISTORY</span>
      </div>
    </main>
  );
}

export function App() {
  const [session, setSession] = useState<Session | null>(null);
  const [checking, setChecking] = useState(true);
  const [bootError, setBootError] = useState("");
  const [boot, setBoot] = useState(0);
  const unauthorized = useCallback(() => setSession(null), []);
  // biome-ignore lint/correctness/useExhaustiveDependencies: boot advances when the user retries a failed session lookup.
  useEffect(() => {
    const controller = new AbortController();
    setChecking(true);
    setBootError("");
    void request<Session>("/session", { signal: controller.signal })
      .then((value) => {
        if (!controller.signal.aborted) setSession(value);
      })
      .catch((error) => {
        if (
          !controller.signal.aborted &&
          !(error instanceof APIError && error.status === 401)
        )
          setBootError(errorMessage(error));
      })
      .finally(() => {
        if (!controller.signal.aborted) setChecking(false);
      });
    return () => controller.abort();
  }, [boot]);
  const route = useRoute();
  const logout = async () => {
    if (!session) return;
    try {
      await request("/logout", { method: "POST", csrf: session.csrf_nonce });
      setSession(null);
    } catch (error) {
      if (error instanceof APIError && error.status === 401) setSession(null);
      else setBootError(errorMessage(error));
    }
  };
  if (checking)
    return (
      <main className="boot" aria-live="polite">
        Opening EventRelay…
      </main>
    );
  if (!session)
    return (
      <>
        {bootError && (
          <Notice>
            {bootError}{" "}
            <button type="button" onClick={() => setBoot((value) => value + 1)}>
              Retry connection
            </button>
          </Notice>
        )}
        <Login ready={setSession} />
      </>
    );
  return (
    <div className="shell">
      <a href="#main" className="skip-link">
        Skip to content
      </a>
      <aside className="sidebar">
        <a className="wordmark" href="#/">
          <span className="brand-icon">↗</span> EventRelay
        </a>
        <p className="eyebrow">DELIVERY CONSOLE</p>
        <nav aria-label="Primary">
          <a className={route.kind === "overview" ? "active" : ""} href="#/">
            Overview <span>01</span>
          </a>
          <p className="nav-help">
            Open an endpoint or event to inspect its persisted history.
          </p>
        </nav>
        <div className="sidebar-footer">
          <span className="live-dot" /> PostgreSQL is authoritative
          <p>Replay keeps the event identity.</p>
        </div>
      </aside>
      <div className="workspace">
        <header className="topbar">
          <span>
            EventRelay /{" "}
            {route.kind === "overview"
              ? "Overview"
              : route.kind === "events"
                ? "Event inspection"
                : "Endpoint inspection"}
          </span>
          <div>
            <span className="session-expiry">
              Session ends {new Date(session.expires_at).toLocaleTimeString()}
            </span>
            <button
              className="quiet"
              type="button"
              onClick={() => {
                void logout();
              }}
            >
              Log out
            </button>
          </div>
        </header>
        <main id="main" tabIndex={-1}>
          {bootError && <Notice>{bootError}</Notice>}
          {route.kind === "events" ? (
            <EventView
              key={route.id}
              id={route.id}
              session={session}
              unauthorized={unauthorized}
            />
          ) : route.kind === "endpoints" ? (
            <EndpointView
              key={route.id}
              id={route.id}
              session={session}
              unauthorized={unauthorized}
            />
          ) : (
            <Overview unauthorized={unauthorized} />
          )}
        </main>
        <footer className="footer">
          Observed delivery history · response content is unavailable · polling
          pauses in hidden tabs
        </footer>
      </div>
    </div>
  );
}

function Overview({ unauthorized }: { unauthorized: () => void }) {
  const pager = usePager("endpoints");
  const endpoints = usePolling<EndpointPage>(
    paged("/endpoints", pager.cursor),
    unauthorized,
  );
  return (
    <>
      <div className="page-heading">
        <div>
          <p className="eyebrow">YOUR DELIVERY OPERATIONS</p>
          <h1>
            Relay overview<span className="heading-dot">.</span>
          </h1>
          <p className="muted">
            Destinations, accepted events and the next scheduled attempt.
          </p>
        </div>
        <span className="environment">M4 · LOCAL CONSOLE</span>
      </div>
      <section className="panel" aria-labelledby="endpoints-title">
        <div className="panel-heading">
          <div>
            <p className="eyebrow">DESTINATIONS</p>
            <h2 id="endpoints-title">Endpoints</h2>
          </div>
          <span className="count">
            {endpoints.data?.endpoints.length ?? "—"} on this page
          </span>
        </div>
        <ReadStatus {...endpoints} />
        {endpoints.data &&
          (endpoints.data.endpoints.length ? (
            <div className="endpoint-grid">
              {endpoints.data.endpoints.map((endpoint) => (
                <a
                  className="endpoint-card"
                  href={`#/endpoints/${encodeURIComponent(endpoint.id)}`}
                  key={endpoint.id}
                >
                  <div className="endpoint-card-top">
                    <span className="endpoint-icon">↗</span>
                    <Badge status={endpoint.enabled ? "enabled" : "disabled"} />
                  </div>
                  <h3>
                    {endpoint.description || `Endpoint ${short(endpoint.id)}`}
                  </h3>
                  <p className="url">{endpoint.url}</p>
                  <div className="card-meta">
                    <span>{endpoint.max_attempts} attempts / cycle</span>
                    <span>Inspect →</span>
                  </div>
                </a>
              ))}
            </div>
          ) : (
            <p className="empty">
              No endpoints yet. Create one through the management API.
            </p>
          ))}
        <PageButtons
          pager={pager}
          nextCursor={endpoints.data?.next_cursor}
          loading={endpoints.loading}
        />
      </section>
      <EventList unauthorized={unauthorized} />
    </>
  );
}

function EventList({
  unauthorized,
  endpointID,
}: {
  unauthorized: () => void;
  endpointID?: string;
}) {
  const [draft, setDraft] = useState({
    status: "",
    endpoint: endpointID ?? "",
    from: "",
    to: "",
  });
  const [filters, setFilters] = useState("");
  const base = `/events?view=summary${endpointID ? `&endpoint_id=${encodeURIComponent(endpointID)}` : ""}${filters}`;
  const pager = usePager(base);
  const events = usePolling<EventPage>(paged(base, pager.cursor), unauthorized);
  const apply = (event: FormEvent) => {
    event.preventDefault();
    const query = new URLSearchParams();
    if (draft.status) query.set("status", draft.status);
    if (!endpointID && draft.endpoint)
      query.set("endpoint_id", draft.endpoint.trim());
    if (draft.from) query.set("from", new Date(draft.from).toISOString());
    if (draft.to) query.set("to", new Date(draft.to).toISOString());
    setFilters(query.size ? `&${query}` : "");
  };
  return (
    <section className="panel" aria-labelledby="events-title">
      <div className="panel-heading">
        <div>
          <p className="eyebrow">ACCEPTED & PERSISTED</p>
          <h2 id="events-title">Recent events</h2>
        </div>
        <span className="count">
          {events.data?.events.length ?? "—"} on this page
        </span>
      </div>
      <form className="filters" onSubmit={apply}>
        <label>
          Status
          <select
            value={draft.status}
            onChange={(event) =>
              setDraft({ ...draft, status: event.target.value })
            }
          >
            <option value="">All statuses</option>
            {[
              "pending",
              "in_flight",
              "delivered",
              "dead_lettered",
              "canceled",
            ].map((status) => (
              <option key={status} value={status}>
                {status.replaceAll("_", " ")}
              </option>
            ))}
          </select>
        </label>
        {!endpointID && (
          <label>
            Endpoint ID
            <input
              value={draft.endpoint}
              onChange={(event) =>
                setDraft({ ...draft, endpoint: event.target.value })
              }
              placeholder="All endpoints"
            />
          </label>
        )}
        <label>
          From (local time)
          <input
            type="datetime-local"
            value={draft.from}
            onChange={(event) =>
              setDraft({ ...draft, from: event.target.value })
            }
          />
        </label>
        <label>
          To (local time)
          <input
            type="datetime-local"
            value={draft.to}
            onChange={(event) => setDraft({ ...draft, to: event.target.value })}
          />
        </label>
        <button className="quiet" type="submit">
          Apply filters
        </button>
      </form>
      <ReadStatus {...events} />
      {events.data &&
        (events.data.events.length ? (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th scope="col">Event</th>
                  <th scope="col">Status</th>
                  <th scope="col">Cycle attempts</th>
                  <th scope="col">Received</th>
                  <th scope="col">Next attempt</th>
                </tr>
              </thead>
              <tbody>
                {events.data.events.map((event) => (
                  <tr key={event.id}>
                    <td>
                      <a
                        className="event-link"
                        href={`#/events/${encodeURIComponent(event.id)}`}
                      >
                        {event.event_type}
                        <span>
                          {short(event.id)} · {event.attempt_count} lifetime
                          attempts
                        </span>
                      </a>
                    </td>
                    <td>
                      <Badge status={event.status} />
                    </td>
                    <td>
                      {event.cycle_attempt_count} / {event.cycle_max_attempts}
                      <span className="cell-note">
                        Cycle {event.replay_count}
                      </span>
                    </td>
                    <td>{date(event.received_at)}</td>
                    <td>
                      {event.next_attempt_at
                        ? date(event.next_attempt_at)
                        : "—"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <p className="empty">No events match these filters.</p>
        ))}
      <PageButtons
        pager={pager}
        nextCursor={events.data?.next_cursor}
        loading={events.loading}
      />
      <p className="fine panel-footnote">
        Newest received first. Status changes affect live membership; pages are
        not a frozen snapshot.
      </p>
    </section>
  );
}

function EndpointView({
  id,
  session,
  unauthorized,
}: {
  id: string;
  session: Session;
  unauthorized: () => void;
}) {
  const endpoint = usePolling<Endpoint>(
    `/endpoints/${encodeURIComponent(id)}`,
    unauthorized,
  );
  const stats = usePolling<Stats>(
    `/endpoints/${encodeURIComponent(id)}/stats`,
    unauthorized,
  );
  const [editing, setEditing] = useState(false);
  const [url, setURL] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [revision, setRevision] = useState(0);
  const [pending, setPending] = useState(false);
  const [message, setMessage] = useState("");
  const [failed, setFailed] = useState(false);
  const save = async (event: FormEvent) => {
    event.preventDefault();
    if (pending) return;
    setPending(true);
    setMessage("");
    try {
      await request(`/endpoints/${encodeURIComponent(id)}`, {
        method: "PATCH",
        csrf: session.csrf_nonce,
        body: { expected_revision: revision, url, enabled },
      });
      setEditing(false);
      setFailed(false);
      setMessage(
        "Endpoint updated. Pending delivery can resume when it is enabled.",
      );
      endpoint.refresh();
    } catch (error) {
      setFailed(true);
      setMessage(errorMessage(error));
      if (error instanceof APIError && error.status === 401) unauthorized();
      if (error instanceof APIError && error.status === 409) {
        setEditing(false);
        endpoint.refresh();
      }
    } finally {
      setPending(false);
    }
  };
  return (
    <>
      <a className="back-link" href="#/">
        ← Overview
      </a>
      <div className="page-heading">
        <div>
          <p className="eyebrow">DESTINATION INSPECTION</p>
          <h1>{endpoint.data?.description || `Endpoint ${short(id)}`}</h1>
          <p className="muted id-text">{id}</p>
        </div>
        {endpoint.data && (
          <Badge status={endpoint.data.enabled ? "enabled" : "disabled"} />
        )}
      </div>
      <section className="panel">
        <div className="panel-heading">
          <h2>Endpoint settings</h2>
        </div>
        <ReadStatus {...endpoint} />
        {message && (
          <Notice tone={failed ? "error" : "success"}>{message}</Notice>
        )}
        {endpoint.data && (
          <>
            <dl className="facts">
              <div>
                <dt>Destination URL</dt>
                <dd>{endpoint.data.url}</dd>
              </div>
              <div>
                <dt>Frozen budget for new cycles</dt>
                <dd>{endpoint.data.max_attempts} attempts</dd>
              </div>
              <div>
                <dt>Management revision</dt>
                <dd>{endpoint.data.revision}</dd>
              </div>
              <div>
                <dt>Created</dt>
                <dd>{date(endpoint.data.created_at)}</dd>
              </div>
            </dl>
            {editing ? (
              <form
                className="edit-form"
                onSubmit={(event) => {
                  void save(event);
                }}
              >
                <label htmlFor="destination-url">Destination URL</label>
                <input
                  id="destination-url"
                  type="url"
                  value={url}
                  onChange={(event) => setURL(event.target.value)}
                  required
                  maxLength={2048}
                />
                <label className="checkbox">
                  <input
                    type="checkbox"
                    checked={enabled}
                    onChange={(event) => setEnabled(event.target.checked)}
                  />{" "}
                  Enabled
                </label>
                <div className="actions">
                  <button type="submit" disabled={pending}>
                    Save endpoint
                  </button>
                  <button
                    className="quiet"
                    type="button"
                    disabled={pending}
                    onClick={() => setEditing(false)}
                  >
                    Cancel
                  </button>
                </div>
              </form>
            ) : (
              <button
                className="quiet"
                type="button"
                onClick={() => {
                  setURL(endpoint.data?.url ?? "");
                  setEnabled(endpoint.data?.enabled ?? true);
                  setRevision(endpoint.data?.revision ?? 0);
                  setEditing(true);
                }}
              >
                Edit destination
              </button>
            )}
            <p className="fine">
              Inspection sends no probe. URL validation and destination
              protection run again during delivery. A 410 from an earlier
              revision cannot disable a corrected destination.
            </p>
          </>
        )}
      </section>
      <section className="panel" aria-labelledby="stats-title">
        <div className="panel-heading">
          <div>
            <p className="eyebrow">FINISHED HTTP ATTEMPTS</p>
            <h2 id="stats-title">Observed success · last 24 hours</h2>
          </div>
        </div>
        <ReadStatus {...stats} />
        {stats.data &&
          (stats.data.available ? (
            <>
              <div className="stats-grid">
                <div className="stat">
                  <span>HTTP attempt success rate</span>
                  <strong>
                    {stats.data.success_rate === null
                      ? "—"
                      : `${(stats.data.success_rate * 100).toFixed(1)}%`}
                  </strong>
                  <small>
                    {stats.data.success_http_attempts} successes /{" "}
                    {stats.data.http_response_attempts} HTTP responses
                  </small>
                </div>
                <div className="stat">
                  <span>No response</span>
                  <strong>{stats.data.no_response_attempts}</strong>
                  <small>Includes network, timeout and expired leases</small>
                </div>
                <div className="stat">
                  <span>Network / timeout</span>
                  <strong>
                    {stats.data.network_attempts} /{" "}
                    {stats.data.timeout_attempts}
                  </strong>
                  <small>
                    Policy: {stats.data.policy_attempts} · expired leases:{" "}
                    {stats.data.lease_expired_attempts}
                  </small>
                </div>
              </div>
              {stats.data.success_rate === null && (
                <Notice tone="info">
                  No finished HTTP-response attempts in this window. The success
                  rate is unavailable.
                </Notice>
              )}
              <p className="fine">
                UTC window: {stats.data.window_from} ≤ finish &lt;{" "}
                {stats.data.window_to}. Includes replay cycles and broken-body
                HTTP responses. Counts describe attempts, with overlapping
                failure categories; they do not measure unique-event success or
                future endpoint availability.
              </p>
            </>
          ) : (
            <Notice tone="info">
              Statistics unavailable: more than{" "}
              {stats.data.attempt_limit.toLocaleString()} attempts in this
              window.
            </Notice>
          ))}
      </section>
      <EventList endpointID={id} unauthorized={unauthorized} />
    </>
  );
}

function EventView({
  id,
  session,
  unauthorized,
}: {
  id: string;
  session: Session;
  unauthorized: () => void;
}) {
  const detail = usePolling<EventDetail>(
    `/events/${encodeURIComponent(id)}`,
    unauthorized,
  );
  const attemptPager = usePager(`attempts:${id}`);
  const cyclePager = usePager(`cycles:${id}`);
  const attempts = usePolling<AttemptPage>(
    paged(`/events/${encodeURIComponent(id)}/attempts`, attemptPager.cursor),
    unauthorized,
  );
  const cycles = usePolling<CyclePage>(
    paged(`/events/${encodeURIComponent(id)}/cycles`, cyclePager.cursor),
    unauthorized,
  );
  const [showPayload, setShowPayload] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [version, setVersion] = useState(0);
  const [pending, setPending] = useState(false);
  const [message, setMessage] = useState("");
  const [failed, setFailed] = useState(false);
  const replay = async () => {
    if (pending) return;
    setPending(true);
    setMessage("");
    try {
      await request(`/events/${encodeURIComponent(id)}/replay`, {
        method: "POST",
        csrf: session.csrf_nonce,
        body: { expected_replay_count: version },
      });
      setFailed(false);
      setMessage(
        "Replay accepted. The event is durably pending; the worker creates the next HTTP attempt.",
      );
    } catch (error) {
      setFailed(true);
      setMessage(errorMessage(error));
      if (error instanceof APIError && error.status === 401) unauthorized();
    } finally {
      setConfirming(false);
      setPending(false);
      detail.refresh();
      attempts.refresh();
      cycles.refresh();
    }
  };
  const eligible =
    detail.data?.status === "delivered" ||
    detail.data?.status === "dead_lettered";
  return (
    <>
      <a className="back-link" href="#/">
        ← Overview
      </a>
      <div className="page-heading">
        <div>
          <p className="eyebrow">EVENT INSPECTION</p>
          <h1>{detail.data?.event_type || `Event ${short(id)}`}</h1>
          <p className="muted id-text">{id}</p>
        </div>
        {detail.data && <Badge status={detail.data.status} />}
      </div>
      <section className="panel">
        <div className="panel-heading">
          <h2>Current delivery cycle</h2>
        </div>
        <ReadStatus {...detail} />
        {message && (
          <Notice tone={failed ? "error" : "success"}>{message}</Notice>
        )}
        {detail.data && (
          <>
            <dl className="facts">
              <div>
                <dt>Endpoint</dt>
                <dd>
                  <a
                    href={`#/endpoints/${encodeURIComponent(detail.data.endpoint_id)}`}
                  >
                    {short(detail.data.endpoint_id)} →
                  </a>
                </dd>
              </div>
              <div>
                <dt>Cycle / replay count</dt>
                <dd>{detail.data.replay_count}</dd>
              </div>
              <div>
                <dt>Cycle attempts / budget</dt>
                <dd>
                  {detail.data.cycle_attempt_count} /{" "}
                  {detail.data.cycle_max_attempts}
                </dd>
              </div>
              <div>
                <dt>Lifetime attempts</dt>
                <dd>{detail.data.attempt_count}</dd>
              </div>
              <div>
                <dt>Next scheduled attempt</dt>
                <dd>
                  {detail.data.next_attempt_at
                    ? date(detail.data.next_attempt_at)
                    : "None"}
                </dd>
              </div>
              <div>
                <dt>Terminal reason</dt>
                <dd>{detail.data.terminal_reason || "None"}</dd>
              </div>
            </dl>
            {detail.data.last_error && (
              <div className="diagnostic">
                <span>
                  Last diagnostic
                  {detail.data.last_error_truncated
                    ? " · truncated to 2,048 characters"
                    : ""}
                </span>
                <pre>{detail.data.last_error}</pre>
              </div>
            )}
            <div className="actions">
              <button
                className="quiet"
                type="button"
                onClick={() => setShowPayload((value) => !value)}
              >
                {showPayload ? "Hide payload" : "Show payload"}
              </button>
              <button
                type="button"
                disabled={!eligible || detail.stale || pending}
                onClick={() => {
                  setVersion(detail.data?.replay_count ?? 0);
                  setConfirming(true);
                }}
              >
                Replay event
              </button>
            </div>
            {showPayload &&
              (detail.data.payload_available ? (
                <section aria-label="Stored event payload">
                  <pre className="payload">
                    {JSON.stringify(detail.data.payload, null, 2)}
                  </pre>
                </section>
              ) : (
                <Notice tone="info">
                  Payload unavailable for inspection:{" "}
                  {detail.data.payload_bytes.toLocaleString()} bytes exceeds the
                  512 KiB limit. The stored payload is unchanged.
                </Notice>
              ))}
            {confirming && (
              <fieldset className="confirmation" aria-label="Confirm replay">
                <h3>Start another delivery cycle?</h3>
                <p>
                  Replay retains the event ID and payload. A previously
                  delivered event can repeat an effect at the destination. The
                  endpoint must be enabled. This action is submitted once and
                  conflicts are shown.
                </p>
                <div className="actions">
                  <button
                    type="button"
                    disabled={pending}
                    onClick={() => {
                      void replay();
                    }}
                  >
                    {pending ? "Submitting…" : "Confirm replay"}
                  </button>
                  <button
                    className="quiet"
                    type="button"
                    disabled={pending}
                    onClick={() => setConfirming(false)}
                  >
                    Cancel replay
                  </button>
                </div>
              </fieldset>
            )}
          </>
        )}
      </section>
      <section className="panel" aria-labelledby="attempts-title">
        <div className="panel-heading">
          <div>
            <p className="eyebrow">PERSISTED EVIDENCE</p>
            <h2 id="attempts-title">Attempt timeline</h2>
          </div>
        </div>
        <ReadStatus {...attempts} />
        {attempts.data &&
          (attempts.data.attempts.length ? (
            <ol className="timeline">
              {attempts.data.attempts.map((attempt) => (
                <li key={attempt.attempt_no}>
                  <div className="timeline-number">{attempt.attempt_no}</div>
                  <div className="timeline-body">
                    <div className="timeline-top">
                      <h3>
                        Attempt {attempt.attempt_no}{" "}
                        <span className="timeline-cycle">
                          Cycle {attempt.cycle_no} · attempt{" "}
                          {attempt.cycle_attempt_no}
                        </span>
                      </h3>
                      <Badge status={attempt.outcome} />
                    </div>
                    <p className="muted">
                      Started {date(attempt.started_at)} ·{" "}
                      {attempt.finished_at
                        ? `finished ${date(attempt.finished_at)}`
                        : "in flight; finish unavailable"}
                    </p>
                    <p>
                      HTTP {attempt.response_status ?? "unavailable"} ·{" "}
                      {attempt.duration_ms === null
                        ? "duration unavailable"
                        : `${attempt.duration_ms} ms`}{" "}
                      · endpoint revision{" "}
                      {attempt.endpoint_revision ?? "unknown (M3 history)"}
                    </p>
                    {attempt.error && (
                      <pre className="diagnostic-text">
                        {attempt.error}
                        {attempt.error_truncated
                          ? "\n[Diagnostic truncated to 2,048 characters]"
                          : ""}
                      </pre>
                    )}
                    <p className="fine">
                      Response body and headers: unavailable; never captured.
                    </p>
                  </div>
                </li>
              ))}
            </ol>
          ) : (
            <p className="empty">No HTTP attempt has been claimed yet.</p>
          ))}
        <PageButtons
          pager={attemptPager}
          nextCursor={attempts.data?.next_cursor}
          loading={attempts.loading}
        />
      </section>
      <section className="panel" aria-labelledby="cycles-title">
        <div className="panel-heading">
          <h2 id="cycles-title">Previous delivery cycles</h2>
        </div>
        <ReadStatus {...cycles} />
        {cycles.data &&
          (cycles.data.cycles.length ? (
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th scope="col">Cycle</th>
                    <th scope="col">Final status</th>
                    <th scope="col">Reason</th>
                    <th scope="col">Attempt range</th>
                    <th scope="col">Replayed</th>
                  </tr>
                </thead>
                <tbody>
                  {cycles.data.cycles.map((cycle) => (
                    <tr key={cycle.cycle_no}>
                      <td>{cycle.cycle_no}</td>
                      <td>
                        <Badge status={cycle.final_status} />
                      </td>
                      <td>{cycle.terminal_reason ?? "None"}</td>
                      <td>
                        {cycle.first_attempt_no === null
                          ? "No attempts"
                          : `${cycle.first_attempt_no}–${cycle.last_attempt_no}`}
                        <span className="cell-note">
                          {cycle.attempt_count} / {cycle.max_attempts} budget
                        </span>
                      </td>
                      <td>{date(cycle.replayed_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <p className="empty">
              This event has not been replayed. Its current cycle appears above.
            </p>
          ))}
        <PageButtons
          pager={cyclePager}
          nextCursor={cycles.data?.next_cursor}
          loading={cycles.loading}
        />
      </section>
    </>
  );
}
