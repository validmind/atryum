import { useQuery, useMutation, useQueryClient } from 'react-query';
import { serversApi, type ServerInput, type OAuthConnectStatusResponse } from '../api/AtryumAPI';

const SERVERS_KEY = 'atryum-servers';
const SERVER_DETAIL_KEY = 'atryum-server-detail';

export const useServers = (showDisabled = true) =>
  useQuery([SERVERS_KEY, showDisabled], () => serversApi.list(showDisabled), {
    refetchOnWindowFocus: false,
  });

export const useServerDetail = (name: string | null) =>
  useQuery([SERVER_DETAIL_KEY, name], () => serversApi.detail(name!), {
    enabled: !!name,
    refetchOnWindowFocus: false,
  });

export const useCreateServer = () => {
  const queryClient = useQueryClient();
  return useMutation((input: ServerInput) => serversApi.create(input), {
    onSuccess: () => queryClient.invalidateQueries(SERVERS_KEY),
  });
};

export const useUpdateServer = () => {
  const queryClient = useQueryClient();
  return useMutation(
    ({ name, input }: { name: string; input: ServerInput }) =>
      serversApi.update(name, input),
    {
      onSuccess: (_data, { name }) => {
        queryClient.invalidateQueries(SERVERS_KEY);
        queryClient.invalidateQueries([SERVER_DETAIL_KEY, name]);
      },
    },
  );
};

export const useRemoveServer = () => {
  const queryClient = useQueryClient();
  return useMutation(
    ({ name, disable }: { name: string; disable?: boolean }) =>
      serversApi.remove(name, disable),
    {
      onSuccess: () => queryClient.invalidateQueries(SERVERS_KEY),
    },
  );
};

export const useTestServer = () => {
  const queryClient = useQueryClient();
  return useMutation((name: string) => serversApi.test(name), {
    onSuccess: (_data, name) => {
      queryClient.invalidateQueries(SERVERS_KEY);
      queryClient.invalidateQueries([SERVER_DETAIL_KEY, name]);
    },
  });
};

export const useStartConnect = () =>
  useMutation((name: string) => serversApi.connect(name));

export const useConnectStatus = (
  name: string | null,
  enabled: boolean,
  onDone: (result: OAuthConnectStatusResponse) => void,
) => {
  const queryClient = useQueryClient();
  return useQuery(
    ['atryum-connect-status', name],
    () => serversApi.connectStatus(name!),
    {
      enabled: !!name && enabled,
      refetchInterval: 2000,
      onSuccess: (data) => {
        if (data.status === 'succeeded' || data.status === 'failed') {
          queryClient.removeQueries(['atryum-connect-status', name]);
          queryClient.invalidateQueries(SERVERS_KEY);
          queryClient.invalidateQueries([SERVER_DETAIL_KEY, name]);
          onDone(data);
        }
      },
    },
  );
};
