import { useMutation, useQuery, useQueryClient } from 'react-query';
import {
  agentsApi,
  type AgentCreateInput,
  type AgentUpdateInput,
} from '../api/AtryumAPI';

const AGENTS_KEY = 'atryum-agents';

export const useAgents = () =>
  useQuery([AGENTS_KEY], () => agentsApi.list(), {
    refetchOnWindowFocus: false,
  });

export const useCreateAgent = () => {
  const queryClient = useQueryClient();
  return useMutation((input: AgentCreateInput) => agentsApi.create(input), {
    onSuccess: () => queryClient.invalidateQueries(AGENTS_KEY),
  });
};

export const useUpdateAgent = () => {
  const queryClient = useQueryClient();
  return useMutation(
    ({ cuid, input }: { cuid: string; input: AgentUpdateInput }) =>
      agentsApi.update(cuid, input),
    {
      onSuccess: () => queryClient.invalidateQueries(AGENTS_KEY),
    },
  );
};

export const useDeleteAgent = () => {
  const queryClient = useQueryClient();
  return useMutation((cuid: string) => agentsApi.remove(cuid), {
    onSuccess: () => queryClient.invalidateQueries(AGENTS_KEY),
  });
};
