import type { Session } from './types';

export class ApiError extends Error {
  readonly status: number;
  constructor(message: string, status: number) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

function isErrorBody(value: unknown): value is { error: { message: string } } {
  return typeof value === 'object' && value !== null && 'error' in value
    && typeof value.error === 'object' && value.error !== null && 'message' in value.error
    && typeof value.error.message === 'string';
}

export async function api<T>(
  path: string, session: Session | null = null,
  options: { method?: string; body?: object; signal?: AbortSignal } = {},
): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (session) headers.Authorization = `Bearer ${session.token}`;
  if (options.body) headers['Content-Type'] = 'application/json';
  const response = await fetch(path, {
    method: options.method ?? 'GET', headers,
    body: options.body ? JSON.stringify(options.body) : undefined,
    signal: options.signal
      ? AbortSignal.any([options.signal, AbortSignal.timeout(12000)])
      : AbortSignal.timeout(12000),
  });
  if (!response.ok) {
    const text = await response.text();
    let message = `Request failed (${response.status}).`;
    if (text) {
      try {
        const data: unknown = JSON.parse(text);
        if (isErrorBody(data)) message = data.error.message;
      } catch {
        message = `The service returned an unexpected response (${response.status}).`;
      }
    }
    throw new ApiError(message, response.status);
  }
  return response.json();
}

export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : 'An unexpected error occurred. Please try again.';
}
