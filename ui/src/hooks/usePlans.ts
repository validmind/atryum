import { useMutation, useQuery, useQueryClient } from 'react-query';
import { plansApi, type PlanFilters } from '../api/AtryumAPI';

export const PLANS_KEY = 'atryum-plans';
export const PLAN_DETAIL_KEY = 'atryum-plan-detail';
export const PLAN_EVENTS_KEY = 'atryum-plan-events';

// Plans have no SSE stream — poll every 5s so pending submissions show up.
const PLAN_POLL_INTERVAL_MS = 5000;

export const usePlans = (filters: PlanFilters = {}) =>
  useQuery([PLANS_KEY, filters], () => plansApi.list(filters), {
    keepPreviousData: true,
    refetchOnWindowFocus: false,
    refetchInterval: PLAN_POLL_INTERVAL_MS,
  });

export const usePlanDetail = (id: string | null) =>
  useQuery([PLAN_DETAIL_KEY, id], () => plansApi.detail(id!), {
    enabled: !!id,
    refetchOnWindowFocus: false,
  });

export const usePlanEvents = (id: string | null) =>
  useQuery([PLAN_EVENTS_KEY, id], () => plansApi.events(id!), {
    enabled: !!id,
    refetchOnWindowFocus: false,
  });

const usePlanMutation = <TVars>(fn: (vars: TVars) => Promise<unknown>, getId: (vars: TVars) => string) => {
  const queryClient = useQueryClient();
  return useMutation(fn, {
    onSuccess: async (_data, vars) => {
      const id = getId(vars);
      await Promise.all([
        queryClient.invalidateQueries(PLANS_KEY),
        queryClient.invalidateQueries([PLAN_DETAIL_KEY, id]),
        queryClient.invalidateQueries([PLAN_EVENTS_KEY, id]),
      ]);
    },
  });
};

export const useApprovePlan = () =>
  usePlanMutation(
    ({ id, ttlSeconds }: { id: string; ttlSeconds?: number }) =>
      plansApi.approve(id, ttlSeconds),
    (v) => v.id,
  );

export const useDenyPlan = () =>
  usePlanMutation(
    ({ id, message }: { id: string; message?: string }) => plansApi.deny(id, message),
    (v) => v.id,
  );

export const useRevisePlan = () =>
  usePlanMutation(
    ({ id, feedback }: { id: string; feedback: string }) => plansApi.revise(id, feedback),
    (v) => v.id,
  );

export const useExpirePlan = () =>
  usePlanMutation(
    ({ id }: { id: string }) => plansApi.expire(id),
    (v) => v.id,
  );
