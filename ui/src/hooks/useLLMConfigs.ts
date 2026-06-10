import { useMutation, useQuery, useQueryClient } from 'react-query';
import { type LLMConfigInput, llmConfigsApi } from '../api/AtryumAPI';

const QUERY_KEY = 'llm-configs';

export function useLLMConfigs() {
  return useQuery(QUERY_KEY, () => llmConfigsApi.list(), { staleTime: 30_000 });
}

export function useCreateLLMConfig() {
  const queryClient = useQueryClient();
  return useMutation((input: LLMConfigInput) => llmConfigsApi.create(input), {
    onSuccess: () => void queryClient.invalidateQueries(QUERY_KEY),
  });
}

export function useUpdateLLMConfig() {
  const queryClient = useQueryClient();
  return useMutation(
    ({ id, input }: { id: string; input: Partial<LLMConfigInput> }) =>
      llmConfigsApi.update(id, input),
    { onSuccess: () => void queryClient.invalidateQueries(QUERY_KEY) },
  );
}

export function useDeleteLLMConfig() {
  const queryClient = useQueryClient();
  return useMutation((id: string) => llmConfigsApi.remove(id), {
    onSuccess: () => void queryClient.invalidateQueries(QUERY_KEY),
  });
}
