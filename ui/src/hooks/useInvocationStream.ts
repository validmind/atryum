// Direct useEffect is required here to manage the SSE connection lifecycle.
/* eslint-disable react-hooks/exhaustive-deps */
import { useEffect, useMemo, useRef } from 'react';
import { useQueryClient } from 'react-query';
import type { QueryKey } from 'react-query';
import { type Invocation, type InvocationDetail } from '../api/AtryumAPI';
import {
  INVOCATION_DETAIL_KEY,
  INVOCATION_EVENTS_KEY,
  INVOCATIONS_KEY,
  normalizeInvocationFilters,
} from './useInvocations';
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
  return `/api/v1/admin/invocations/stream?${params.toString()}`;
};

const listKey = (filters: InvocationFilters): QueryKey => [
  INVOCATIONS_KEY,
  normalizeInvocationFilters(filters),
];

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

  const normalizedFilters = useMemo(
    () => normalizeInvocationFilters(filters),
    [filters],
  );

  useEffect(() => {
    if (!isEnabled) return;

    const source = new EventSource(buildStreamUrl(normalizedFilters));

    const handleInvocations = (event: MessageEvent<string>) => {
      const payload = JSON.parse(event.data) as InvocationStreamPayload;
      const items = payload.items ?? [];

      queryClient.setQueryData<{ items: Invocation[] }>(
        listKey(normalizedFilters),
        { items },
      );

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

    source.addEventListener('invocations', handleInvocations as EventListener);

    return () => {
      source.removeEventListener(
        'invocations',
        handleInvocations as EventListener,
      );
      source.close();
    };
  }, [isEnabled, normalizedFilters, queryClient]);
};
