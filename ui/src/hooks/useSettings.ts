import { useQuery, useMutation, useQueryClient } from 'react-query';
import { settingsApi, type AgentSyncSettings } from '../api/AtryumAPI';

export const useSettings = () => {
  const query = useQuery(['settings'], settingsApi.get, {
    retry: false,
    staleTime: 30_000,
  });
  const isConnected = Boolean(
    query.data?.org_cuid && query.data?.agent_record_type_slug,
  );
  return { ...query, isConnected };
};

export const useUpdateSettings = () => {
  const queryClient = useQueryClient();
  return useMutation(
    (input: Omit<AgentSyncSettings, 'updated_at' | 'sync_error'>) =>
      settingsApi.update(input),
    {
      onSuccess: (data) => {
        queryClient.setQueryData(['settings'], data);
      },
    },
  );
};
