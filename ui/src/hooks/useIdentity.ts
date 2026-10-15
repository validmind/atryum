import { useMutation, useQuery, useQueryClient } from 'react-query';

import type { Me, UserUpdateInput, AgentAPIKeyCreateInput } from '../api/AtryumAPI';
import { identityApi } from '../api/AtryumAPI';
import { useAdminAuth } from '../auth/adminAuth';

export const ME_KEY = 'me';
export const USERS_KEY = 'users';
export const userKey = (userID: string) => ['user', userID];
export const userAgentsKey = (userID: string) => ['user-agents', userID];
export const userKeysKey = (userID: string) => ['user-keys', userID];
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

export const useUser = (userID: string, enabled = true) =>
  useQuery(userKey(userID), () => identityApi.getUser(userID), {
    enabled: enabled && userID !== '',
    refetchOnWindowFocus: false,
  });

export const useUpdateUser = () => {
  const queryClient = useQueryClient();
  return useMutation(
    ({ id, input }: { id: string; input: UserUpdateInput }) => identityApi.updateUser(id, input),
    {
      onSuccess: (_data, { id }) => {
        queryClient.invalidateQueries(USERS_KEY);
        queryClient.invalidateQueries(userKey(id));
        // Disabling cascades to memberships and keys.
        queryClient.invalidateQueries(userAgentsKey(id));
        queryClient.invalidateQueries(userKeysKey(id));
      },
    },
  );
};

/** Agents a user is a member of (admin view, /users/{id}/agents). */
export const useUserAgents = (userID: string, enabled = true) =>
  useQuery(userAgentsKey(userID), () => identityApi.listUserAgents(userID), {
    enabled: enabled && userID !== '',
    refetchOnWindowFocus: false,
  });

/** Every API key a user issued, across agents (admin view, /users/{id}/keys). */
export const useUserKeys = (userID: string, enabled = true) =>
  useQuery(userKeysKey(userID), () => identityApi.listUserKeys(userID), {
    enabled: enabled && userID !== '',
    refetchOnWindowFocus: false,
  });

/**
 * Remove a user from an agent, from the user's side. Keys the user issued
 * for that agent are revoked by the server, so both per-user views refresh.
 */
export const useRemoveUserFromAgent = (userID: string) => {
  const queryClient = useQueryClient();
  return useMutation((agentID: string) => identityApi.removeMember(agentID, userID), {
    onSuccess: (_data, agentID) => {
      queryClient.invalidateQueries(userAgentsKey(userID));
      queryClient.invalidateQueries(userKeysKey(userID));
      queryClient.invalidateQueries(agentMembersKey(agentID));
      queryClient.invalidateQueries(agentKeysKey(agentID));
    },
  });
};

/** Revoke one of a user's keys, from the user's side. */
export const useRevokeUserKey = (userID: string) => {
  const queryClient = useQueryClient();
  return useMutation(
    ({ agentID, keyID }: { agentID: string; keyID: string }) => identityApi.revokeKey(agentID, keyID),
    {
      onSuccess: (_data, { agentID }) => {
        queryClient.invalidateQueries(userKeysKey(userID));
        queryClient.invalidateQueries(agentKeysKey(agentID));
      },
    },
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
