export class APIError extends Error {
  status: number;
  code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

export async function request<T>(
  path: string,
  options: {
    signal?: AbortSignal;
    method?: string;
    body?: unknown;
    csrf?: string;
  } = {},
): Promise<T> {
  const timeout = new AbortController();
  const timer = setTimeout(() => timeout.abort(), 4000);
  const signal = options.signal
    ? AbortSignal.any([options.signal, timeout.signal])
    : timeout.signal;
  try {
    const response = await fetch(`/dashboard/api${path}`, {
      method: options.method ?? "GET",
      signal,
      credentials: "same-origin",
      cache: "no-store",
      redirect: "error",
      headers: {
        Accept: "application/json",
        ...(options.body === undefined
          ? {}
          : { "Content-Type": "application/json" }),
        ...(options.csrf ? { "X-EventRelay-CSRF": options.csrf } : {}),
      },
      body:
        options.body === undefined ? undefined : JSON.stringify(options.body),
    });
    if (response.status === 204) return undefined as T;
    const raw = await response.text();
    if (new TextEncoder().encode(raw).length > 1 << 20)
      throw new APIError(
        503,
        "response-limit",
        "The response exceeded the inspection limit.",
      );
    let value: unknown;
    try {
      value = JSON.parse(raw);
    } catch {
      throw new APIError(
        response.status,
        "invalid-response",
        "The server returned an unreadable response.",
      );
    }
    if (!response.ok) {
      const problem = value as {
        type?: string;
        title?: string;
        detail?: string;
      };
      throw new APIError(
        response.status,
        problem.type ?? "request-failed",
        problem.detail || problem.title || "The request failed.",
      );
    }
    return value as T;
  } finally {
    clearTimeout(timer);
  }
}

export function errorMessage(error: unknown): string {
  if (error instanceof APIError) return error.message;
  if (error instanceof Error && error.name === "AbortError")
    return "The request timed out. Try refreshing.";
  return "The server could not be reached. Try refreshing.";
}
