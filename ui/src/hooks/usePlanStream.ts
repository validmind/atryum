// Direct useEffect is required here to manage the SSE connection lifecycle.
/* eslint-disable react-hooks/exhaustive-deps */
import { useEffect, useRef } from 'react';
import { useQueryClient } from 'react-query';
import { getAdminAccessToken, refreshAdminAccessToken } from '../auth/adminAuth';
import type { Plan, PlanFilters } from '../api/AtryumAPI';
import { PLAN_DETAIL_KEY, PLAN_EVENTS_KEY, PLANS_KEY } from './usePlans';

interface PlanStreamPayload {
  items: Plan[];
  total: number;
  offset: number;
  limit: number;
}

const buildStreamUrl = (filters: PlanFilters): string => {
  const params = new URLSearchParams();
  if (filters.status) params.set('status', filters.status);
  if (filters.agent_id) params.set('agent_id', filters.agent_id);
  if (filters.offset != null) params.set('offset', String(filters.offset));
  params.set('limit', String(filters.limit ?? 50));
  return `/api/v1/plans/stream?${params.toString()}`;
};

export const usePlanStream = (
  filters: PlanFilters,
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

    const handlePlans = (data: string) => {
      const payload = JSON.parse(data) as PlanStreamPayload;
      queryClient.setQueryData([PLANS_KEY, filters], payload);

      const currentSelectedId = selectedIdRef.current;
      if (!currentSelectedId) return;
      void queryClient.invalidateQueries([PLAN_DETAIL_KEY, currentSelectedId]);
      void queryClient.invalidateQueries([PLAN_EVENTS_KEY, currentSelectedId]);
    };

    const dispatchEvent = (eventName: string, dataLines: string[]) => {
      if (eventName !== 'plans' || dataLines.length === 0) return;
      handlePlans(dataLines.join('\n'));
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

        if (response.status === 401) return;
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
