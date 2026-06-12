import { useMutation, useQuery, useQueryClient } from 'react-query';
import {
  invocationsApi,
  type InvocationFilters,
  type RuleInput,
} from '../api/AtryumAPI';

export const INVOCATIONS_KEY = 'atryum-invocations';
export const INVOCATION_DETAIL_KEY = 'atryum-invocation-detail';
export const INVOCATION_EVENTS_KEY = 'atryum-invocation-events';

export const normalizeInvocationFilters = (
  filters: InvocationFilters = {},
): InvocationFilters => {
  const normalized: InvocationFilters = { limit: filters.limit ?? 50 };
  if (filters.server) normalized.server = filters.server;
  if (filters.tool) normalized.tool = filters.tool;
  if (filters.status) normalized.status = filters.status;
  if (filters.client_name) normalized.client_name = filters.client_name;
  if (filters.offset != null) normalized.offset = filters.offset;
  return normalized;
};

export const useInvocations = (filters: InvocationFilters = {}) => {
  const normalizedFilters = normalizeInvocationFilters(filters);
  return useQuery(
    [INVOCATIONS_KEY, normalizedFilters],
    () => invocationsApi.list(normalizedFilters),
    { keepPreviousData: true, refetchOnWindowFocus: false },
  );
};

export const useInvocationDetail = (id: string | null) =>
  useQuery([INVOCATION_DETAIL_KEY, id], () => invocationsApi.detail(id!), {
    enabled: !!id,
    refetchOnWindowFocus: false,
  });

export const useInvocationEvents = (id: string | null) =>
  useQuery([INVOCATION_EVENTS_KEY, id], () => invocationsApi.events(id!), {
    enabled: !!id,
    refetchOnWindowFocus: false,
  });

export const useApproveInvocation = () => {
  const queryClient = useQueryClient();
  return useMutation(
    ({ id, createRule }: { id: string; createRule?: RuleInput }) =>
      invocationsApi.approve(
        id,
        createRule ? { create_rule: createRule } : undefined,
      ),
    {
      onSuccess: async (_data, { id }) => {
        await Promise.all([
          queryClient.invalidateQueries(INVOCATIONS_KEY),
          queryClient.invalidateQueries([INVOCATION_DETAIL_KEY, id]),
          queryClient.invalidateQueries([INVOCATION_EVENTS_KEY, id]),
        ]);
      },
    },
  );
};

export const useDenyInvocation = () => {
  const queryClient = useQueryClient();
  return useMutation(
    ({
      id,
      message,
      createRule,
    }: {
      id: string;
      message?: string;
      createRule?: RuleInput;
    }) => invocationsApi.deny(id, message, createRule),
    {
      onSuccess: async (_data, { id }) => {
        await Promise.all([
          queryClient.invalidateQueries(INVOCATIONS_KEY),
          queryClient.invalidateQueries([INVOCATION_DETAIL_KEY, id]),
          queryClient.invalidateQueries([INVOCATION_EVENTS_KEY, id]),
        ]);
      },
    },
  );
};

export const useSummarizeInvocation = () => {
  const queryClient = useQueryClient();
  return useMutation(
    ({ id, modelConfigCuid }: { id: string; modelConfigCuid?: string }) =>
      invocationsApi.summarize(id, modelConfigCuid),
    {
      onSuccess: async (_data, { id }) => {
        await Promise.all([
          queryClient.invalidateQueries(INVOCATIONS_KEY),
          queryClient.invalidateQueries([INVOCATION_DETAIL_KEY, id]),
          queryClient.invalidateQueries([INVOCATION_EVENTS_KEY, id]),
        ]);
      },
    },
  );
};
