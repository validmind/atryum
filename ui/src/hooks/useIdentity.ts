import { useMutation, useQuery, useQueryClient } from 'react-query';

import type { Me, UserUpdateInput, AgentAPIKeyCreateInput } from '../api/AtryumAPI';
import { identityApi } from '../api/AtryumAPI';
import { useAdminAuth } from '../auth/adminAuth';

export const ME_KEY = 'me';
export const USERS_KEY = 'users';
export const agentMembersKey = (agentID: string) => ['agent-members', agentID];
export const agentKeysKey = (agentID: string) => ['agent-keys', agentID];

/**
 * The current principal. With auth disabled the server answers with a
 * synthetic admin principal, so callers can rely on `role` either way.
 */
export const useMe = () => {
  const { status } = useAdminAuth();
  return useQuery<Me>([ME_KEY], () => identityApi.me(), {
    enabled: status === 'authenticated' || status === 'disabled',
    refetchOnWindowFocus: false,
    staleTime: 60_000,
  });
};

/** True when the current principal may see everything (admin, machine key, or auth disabled). */
export const useIsAdmin = (): boolean => {
  const { data } = useMe();
  return data?.role === 'admin';
};

export const useUsers = (enabled = true) =>
  useQuery([USERS_KEY], () => identityApi.listUsers(), {
    enabled,
    refetchOnWindowFocus: false,
  });

export const useUpdateUser = () => {
  const queryClient = useQueryClient();
  return useMutation(
    ({ id, input }: { id: string; input: UserUpdateInput }) => identityApi.updateUser(id, input),
    { onSuccess: () => queryClient.invalidateQueries(USERS_KEY) },
  );
};

export const useAgentMembers = (agentID: string, enabled = true) =>
  useQuery(agentMembersKey(agentID), () => identityApi.listMembers(agentID), {
    enabled,
    refetchOnWindowFocus: false,
  });

export const useAddAgentMember = (agentID: string) => {
  const queryClient = useQueryClient();
  return useMutation((userID: string) => identityApi.addMember(agentID, userID), {
    onSuccess: () => {
      queryClient.invalidateQueries(agentMembersKey(agentID));
      queryClient.invalidateQueries(agentKeysKey(agentID));
    },
  });
};

export const useRemoveAgentMember = (agentID: string) => {
  const queryClient = useQueryClient();
  return useMutation((userID: string) => identityApi.removeMember(agentID, userID), {
    onSuccess: () => {
      queryClient.invalidateQueries(agentMembersKey(agentID));
      queryClient.invalidateQueries(agentKeysKey(agentID));
    },
  });
};

export const useAgentKeys = (agentID: string, enabled = true) =>
  useQuery(agentKeysKey(agentID), () => identityApi.listKeys(agentID), {
    enabled,
    refetchOnWindowFocus: false,
  });

export const useCreateAgentKey = (agentID: string) => {
  const queryClient = useQueryClient();
  return useMutation((input: AgentAPIKeyCreateInput) => identityApi.createKey(agentID, input), {
    onSuccess: () => queryClient.invalidateQueries(agentKeysKey(agentID)),
  });
};

export const useRevokeAgentKey = (agentID: string) => {
  const queryClient = useQueryClient();
  return useMutation((keyID: string) => identityApi.revokeKey(agentID, keyID), {
    onSuccess: () => queryClient.invalidateQueries(agentKeysKey(agentID)),
  });
};
