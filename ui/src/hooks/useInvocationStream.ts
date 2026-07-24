// Direct useEffect is required here to manage the SSE connection lifecycle.
/* eslint-disable react-hooks/exhaustive-deps */
import { useEffect, useRef } from 'react';
import { useQueryClient } from 'react-query';
import type { QueryKey } from 'react-query';
import { getAdminAccessToken, refreshAdminAccessToken } from '../auth/adminAuth';
import { type Invocation, type InvocationDetail } from '../api/AtryumAPI';
import {
  INVOCATION_DETAIL_KEY,
  INVOCATION_EVENTS_KEY,
  INVOCATIONS_KEY,
} from './useInvocations';
import { RULES_KEY } from './useRules';
import type { InvocationFilters } from '../api/AtryumAPI';

interface InvocationStreamPayload {
  items?: Invocation[];
}

const buildStreamUrl = (filters: InvocationFilters): string => {
  const params = new URLSearchParams();
  if (filters.server) params.set('server', filters.server);
  if (filters.tool) params.set('tool', filters.tool);
  if (filters.status) params.set('status', filters.status);
  if (filters.client_name) params.set('client_name', filters.client_name);
  params.set('limit', String(filters.limit ?? 50));
  return `/api/v1/review/invocations/stream?${params.toString()}`;
};

const detailKey = (id: string): QueryKey => [INVOCATION_DETAIL_KEY, id];
const eventsKey = (id: string): QueryKey => [INVOCATION_EVENTS_KEY, id];

const detailNeedsRefresh = (
  cachedDetail: InvocationDetail | undefined,
  streamedInvocation: Invocation,
): boolean => {
  if (!cachedDetail) return true;
  return (
    cachedDetail.status !== streamedInvocation.status ||
    cachedDetail.completed_at !== streamedInvocation.completed_at ||
    cachedDetail.summary !== streamedInvocation.summary
  );
};

export const useInvocationStream = (
  filters: InvocationFilters,
  selectedId: string | null,
  isEnabled = true,
) => {
  const queryClient = useQueryClient();
  const selectedIdRef = useRef<string | null>(selectedId);
  selectedIdRef.current = selectedId;

  useEffect(() => {
    if (!isEnabled) return;

    let isClosed = false;
    let retryDelayMs = 1000;
    let retryTimer: ReturnType<typeof window.setTimeout> | null = null;
    let controller: AbortController | null = null;

    const handleInvocations = (data: string) => {
      const payload = JSON.parse(data) as InvocationStreamPayload;
      const items = payload.items ?? [];

      // Invalidate all invocations list queries regardless of offset/limit so
      // the currently-viewed page (which includes offset in its cache key)
      // picks up the update. Using setQueryData with the stream's own key no
      // longer works since pagination added offset to the useInvocations key.
      void queryClient.invalidateQueries([INVOCATIONS_KEY]);

      // Also refresh the rules cache: a new invocation may reference a rule
      // that was created after the rules list was last fetched, which would
      // cause the audit display to fall back to "*Unknown rule (…)*" for a
      // rule that is perfectly valid and simply not loaded yet.
      void queryClient.invalidateQueries([RULES_KEY]);

      const currentSelectedId = selectedIdRef.current;
      if (!currentSelectedId) return;

      const selectedInvocation = items.find(
        (item) => item.invocation_id === currentSelectedId,
      );

      if (!selectedInvocation) {
        void queryClient.invalidateQueries(detailKey(currentSelectedId));
        void queryClient.invalidateQueries(eventsKey(currentSelectedId));
        return;
      }

      const cachedDetail = queryClient.getQueryData<InvocationDetail>(
        detailKey(currentSelectedId),
      );

      if (detailNeedsRefresh(cachedDetail, selectedInvocation)) {
        void queryClient.invalidateQueries(detailKey(currentSelectedId));
        void queryClient.invalidateQueries(eventsKey(currentSelectedId));
      }
    };

    const dispatchEvent = (eventName: string, dataLines: string[]) => {
      if (eventName !== 'invocations' || dataLines.length === 0) return;
      handleInvocations(dataLines.join('\n'));
    };

    const scheduleReconnect = () => {
      if (isClosed) return;
      const delay = retryDelayMs;
      retryDelayMs = Math.min(retryDelayMs * 2, 15000);
      retryTimer = window.setTimeout(() => {
        retryTimer = null;
        void connect(false);
      }, delay);
    };

    const connect = async (didRefresh: boolean) => {
      if (isClosed) return;
      controller = new AbortController();
      try {
        let token = await getAdminAccessToken();
        const headers: HeadersInit = { Accept: 'text/event-stream' };
        if (token) headers.Authorization = `Bearer ${token}`;

        let response = await fetch(buildStreamUrl(filters), {
          headers,
          signal: controller.signal,
        });

        if (response.status === 401 && !didRefresh) {
          token = await refreshAdminAccessToken();
          if (token) {
            response = await fetch(buildStreamUrl(filters), {
              headers: {
                Accept: 'text/event-stream',
                Authorization: `Bearer ${token}`,
              },
              signal: controller.signal,
            });
          }
        }

        if (response.status === 401) {
          // Auth could not be recovered: the refresh attempt above either
          // signed the user out (firing the provider's userUnloaded event ->
          // sign-in screen) or returned a token the server still rejects.
          // Reconnecting would loop on the same 401, so stop here.
          return;
        }

        if (!response.ok || !response.body) {
          scheduleReconnect();
          return;
        }

        retryDelayMs = 1000;
        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = '';
        let eventName = 'message';
        let dataLines: string[] = [];

        const processLine = (rawLine: string) => {
          const line = rawLine.endsWith('\r') ? rawLine.slice(0, -1) : rawLine;
          if (line === '') {
            dispatchEvent(eventName, dataLines);
            eventName = 'message';
            dataLines = [];
            return;
          }
          if (line.startsWith(':')) return;
          const separator = line.indexOf(':');
          const field = separator === -1 ? line : line.slice(0, separator);
          const rawValue = separator === -1 ? '' : line.slice(separator + 1);
          const value = rawValue.startsWith(' ') ? rawValue.slice(1) : rawValue;
          if (field === 'event') eventName = value;
          if (field === 'data') dataLines.push(value);
        };

        while (!isClosed) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          const lines = buffer.split('\n');
          buffer = lines.pop() ?? '';
          for (const line of lines) processLine(line);
        }
        if (buffer !== '') processLine(buffer);
        scheduleReconnect();
      } catch (err) {
        if (!isClosed && !(err instanceof DOMException && err.name === 'AbortError')) {
          scheduleReconnect();
        }
      }
    };

    void connect(false);

    return () => {
      isClosed = true;
      if (retryTimer) window.clearTimeout(retryTimer);
      controller?.abort();
    };
  }, [isEnabled, filters, queryClient]);
};
