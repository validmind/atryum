import { useQuery, useMutation, useQueryClient } from 'react-query';
import { rulesApi, serversApi, type RuleInput, type ServerTool } from '../api/AtryumAPI';

const RULES_KEY = 'atryum-rules';

export const useRules = () =>
  useQuery([RULES_KEY], () => rulesApi.list(), {
    refetchOnWindowFocus: false,
  });

export const useCreateRule = () => {
  const queryClient = useQueryClient();
  return useMutation((input: RuleInput) => rulesApi.create(input), {
    onSuccess: () => queryClient.invalidateQueries(RULES_KEY),
  });
};

export const useUpdateRule = () => {
  const queryClient = useQueryClient();
  return useMutation(
    ({ id, input }: { id: string; input: RuleInput }) =>
      rulesApi.update(id, input),
    {
      onSuccess: () => queryClient.invalidateQueries(RULES_KEY),
    },
  );
};

export const useRemoveRule = () => {
  const queryClient = useQueryClient();
  return useMutation((id: string) => rulesApi.remove(id), {
    onSuccess: () => queryClient.invalidateQueries(RULES_KEY),
  });
};

export const useServerTools = (serverNames: string[]) =>
  useQuery<ServerTool[]>(
    ['atryum-server-tools', serverNames],
    async () => {
      const results = await Promise.all(
        serverNames.map((name) => serversApi.tools(name)),
      );
      const seen = new Set<string>();
      const merged: ServerTool[] = [];
      for (const result of results) {
        for (const tool of result.items) {
          if (!seen.has(tool.name)) {
            seen.add(tool.name);
            merged.push(tool);
          }
        }
      }
      return merged;
    },
    {
      enabled: serverNames.length > 0,
      refetchOnWindowFocus: false,
      keepPreviousData: true,
    },
  );

export const useMoveRule = () => {
  const queryClient = useQueryClient();
  return useMutation(
    ({ id, direction }: { id: string; direction: 'up' | 'down' }) =>
      rulesApi.move(id, direction),
    {
      onSuccess: () => queryClient.invalidateQueries(RULES_KEY),
    },
  );
};
