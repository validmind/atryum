import React, { useMemo, useState } from 'react';
import {
  Alert,
  AlertDescription,
  AlertIcon,
  Badge,
  Box,
  Button,
  Code,
  Divider,
  FormControl,
  FormHelperText,
  FormLabel,
  HStack,
  Input,
  Select as NativeSelect,
  Spinner,
  Table,
  Tbody,
  Td,
  Text,
  Th,
  Thead,
  Tr,
  VStack,
  useClipboard,
} from '@chakra-ui/react';

import type { Agent, AgentAPIKey, AtryumUser } from '../api/AtryumAPI';
import { apiErrorMessage } from '../api/AtryumAPI';
import {
  useAddAgentMember,
  useAgentKeys,
  useAgentMembers,
  useCreateAgentKey,
  useIsAdmin,
  useMe,
  useRemoveAgentMember,
  useRevokeAgentKey,
  useUsers,
} from '../hooks/useIdentity';

const formatDate = (iso?: string | null): string => {
  if (!iso) return '—';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleString();
};

const EXPIRY_OPTIONS: { label: string; value: string }[] = [
  { label: 'Never', value: '' },
  { label: '7 days', value: '7d' },
  { label: '30 days', value: '30d' },
  { label: '90 days', value: '90d' },
  { label: '1 year', value: '365d' },
];

// ─── Newly issued key banner ─────────────────────────────────────────────────

const NewKeyBanner: React.FC<{ token: string; onDismiss: () => void }> = ({ token, onDismiss }) => {
  const { hasCopied, onCopy } = useClipboard(token);
  return (
    <Alert status="success" borderRadius="md" alignItems="flex-start" data-testid="new-key-banner">
      <AlertIcon mt={1} />
      <Box flex={1}>
        <AlertDescription fontSize="sm">
          <Text fontWeight="semibold">Copy this key now. It will not be shown again.</Text>
          <HStack mt={2} align="center">
            <Code
              fontSize="xs"
              px={2}
              py={1}
              wordBreak="break-all"
              flex={1}
              data-testid="new-key-token"
            >
              {token}
            </Code>
            <Button size="xs" onClick={onCopy}>
              {hasCopied ? 'Copied' : 'Copy'}
            </Button>
          </HStack>
          <Text mt={2} fontSize="xs" color="text.subtle">
            Use it as <Code fontSize="xs">Authorization: Bearer {'<key>'}</Code> from MCP clients and
            hooks, or run <Code fontSize="xs">atryum setup claude</Code> and paste it when prompted.
          </Text>
        </AlertDescription>
        <Button size="xs" variant="ghost" mt={2} onClick={onDismiss}>
          Done
        </Button>
      </Box>
    </Alert>
  );
};

// ─── API keys ────────────────────────────────────────────────────────────────

export const keyStatus = (key: AgentAPIKey): { label: string; color: string } => {
  if (key.revoked_at) return { label: 'revoked', color: 'gray' };
  if (key.expires_at && new Date(key.expires_at).getTime() <= Date.now()) {
    return { label: 'expired', color: 'orange' };
  }
  return { label: 'active', color: 'green' };
};

export const AgentKeysPanel: React.FC<{ agent: Agent; isOpen: boolean }> = ({ agent, isOpen }) => {
  const keysQuery = useAgentKeys(agent.cuid, isOpen);
  const createKey = useCreateAgentKey(agent.cuid);
  const revokeKey = useRevokeAgentKey(agent.cuid);
  const [name, setName] = useState('');
  const [expiresIn, setExpiresIn] = useState('');
  const [newToken, setNewToken] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const keys = keysQuery.data?.items ?? [];
  const activeCount = keys.filter((k) => k.active).length;

  const handleCreate = async () => {
    setError(null);
    try {
      const created = await createKey.mutateAsync({
        name: name.trim(),
        expires_in: expiresIn || undefined,
      });
      setNewToken(created.token ?? null);
      setName('');
    } catch (err: unknown) {
      setError(apiErrorMessage(err, 'Failed to create key.'));
    }
  };

  const handleRevoke = async (key: AgentAPIKey) => {
    const label = key.name ? `"${key.name}" (${key.key_prefix}…)` : `${key.key_prefix}…`;
    if (!window.confirm(`Revoke key ${label}? Anything using it will stop authenticating immediately.`)) {
      return;
    }
    setError(null);
    try {
      await revokeKey.mutateAsync(key.id);
    } catch (err: unknown) {
      setError(apiErrorMessage(err, 'Failed to revoke key.'));
    }
  };

  return (
    <VStack align="stretch" gap={4}>
      <Text fontSize="sm" color="text.subtle">
        API keys let an agent authenticate to Atryum as <strong>{agent.name}</strong>. Every request
        made with a key is attributed to this agent and to the user who issued the key.
      </Text>

      {error && (
        <Alert status="error" borderRadius="md" py={2}>
          <AlertIcon />
          <AlertDescription fontSize="sm">{error}</AlertDescription>
        </Alert>
      )}

      {newToken && <NewKeyBanner token={newToken} onDismiss={() => setNewToken(null)} />}

      <HStack align="flex-end" gap={3}>
        <FormControl flex={2}>
          <FormLabel fontSize="sm">Key name</FormLabel>
          <Input
            size="sm"
            placeholder="e.g. laptop, ci-runner"
            value={name}
            onChange={(e) => setName(e.target.value)}
            data-testid="new-key-name"
          />
        </FormControl>
        <FormControl flex={1}>
          <FormLabel fontSize="sm">Expires</FormLabel>
          <NativeSelect size="sm" value={expiresIn} onChange={(e) => setExpiresIn(e.target.value)}>
            {EXPIRY_OPTIONS.map((opt) => (
              <option key={opt.value} value={opt.value}>
                {opt.label}
              </option>
            ))}
          </NativeSelect>
        </FormControl>
        <Button
          size="sm"
          variant="primary"
          isLoading={createKey.isLoading}
          onClick={handleCreate}
          data-testid="generate-key"
        >
          Generate key
        </Button>
      </HStack>

      <Divider />

      <HStack justify="space-between">
        <Text fontSize="sm" fontWeight="semibold">
          Keys
        </Text>
        <Badge colorScheme={activeCount > 0 ? 'green' : 'gray'} fontSize="2xs">
          {activeCount} active
        </Badge>
      </HStack>

      {keysQuery.isLoading ? (
        <HStack justify="center" py={4}>
          <Spinner size="sm" />
        </HStack>
      ) : keysQuery.isError ? (
        <Alert status="error" borderRadius="md" py={2}>
          <AlertIcon />
          <AlertDescription fontSize="sm">
            {apiErrorMessage(keysQuery.error, 'Failed to load keys.')}
          </AlertDescription>
        </Alert>
      ) : keys.length === 0 ? (
        <Text fontSize="sm" color="text.subtle" textAlign="center" py={2}>
          No keys yet.
        </Text>
      ) : (
        <Box borderWidth={1} borderColor="border.base" borderRadius="md" overflowX="auto">
          <Table size="sm" variant="simple">
            <Thead bg="background.table.header">
              <Tr>
                <Th>Name</Th>
                <Th>Prefix</Th>
                <Th>Status</Th>
                <Th>Created</Th>
                <Th>Last used</Th>
                <Th>Expires</Th>
                <Th />
              </Tr>
            </Thead>
            <Tbody>
              {keys.map((key) => {
                const status = keyStatus(key);
                return (
                  <Tr key={key.id} opacity={key.active ? 1 : 0.6} data-testid={`key-row-${key.id}`}>
                    <Td fontSize="sm">{key.name || '—'}</Td>
                    <Td>
                      <Code fontSize="xs">{key.key_prefix}…</Code>
                    </Td>
                    <Td>
                      <Badge colorScheme={status.color} fontSize="2xs">
                        {status.label}
                      </Badge>
                    </Td>
                    <Td fontSize="xs" color="text.subtle" whiteSpace="nowrap">
                      {formatDate(key.created_at)}
                    </Td>
                    <Td fontSize="xs" color="text.subtle" whiteSpace="nowrap">
                      {formatDate(key.last_used_at)}
                    </Td>
                    <Td fontSize="xs" color="text.subtle" whiteSpace="nowrap">
                      {key.expires_at ? formatDate(key.expires_at) : 'never'}
                    </Td>
                    <Td textAlign="right">
                      {key.active && (
                        <Button
                          size="xs"
                          variant="outlineDanger"
                          isLoading={revokeKey.isLoading && revokeKey.variables === key.id}
                          onClick={() => handleRevoke(key)}
                        >
                          Revoke
                        </Button>
                      )}
                    </Td>
                  </Tr>
                );
              })}
            </Tbody>
          </Table>
        </Box>
      )}
    </VStack>
  );
};

// ─── Members ─────────────────────────────────────────────────────────────────

export const AgentMembersPanel: React.FC<{ agent: Agent; isOpen: boolean }> = ({ agent, isOpen }) => {
  const isAdmin = useIsAdmin();
  const { data: me } = useMe();
  const membersQuery = useAgentMembers(agent.cuid, isOpen);
  const usersQuery = useUsers(isOpen && isAdmin);
  const addMember = useAddAgentMember(agent.cuid);
  const removeMember = useRemoveAgentMember(agent.cuid);
  const [selectedUser, setSelectedUser] = useState('');
  const [error, setError] = useState<string | null>(null);

  const members = membersQuery.data?.items ?? [];
  const candidates = useMemo<AtryumUser[]>(() => {
    const memberIDs = new Set(members.map((m) => m.user_id));
    return (usersQuery.data?.items ?? []).filter((u) => !u.disabled && !memberIDs.has(u.id));
  }, [members, usersQuery.data?.items]);

  const handleAdd = async () => {
    if (!selectedUser) return;
    setError(null);
    try {
      await addMember.mutateAsync(selectedUser);
      setSelectedUser('');
    } catch (err: unknown) {
      setError(apiErrorMessage(err, 'Failed to add member.'));
    }
  };

  const handleRemove = async (userID: string, label: string) => {
    if (!window.confirm(`Remove ${label} from ${agent.name}? Their keys for this agent will be revoked.`)) {
      return;
    }
    setError(null);
    try {
      await removeMember.mutateAsync(userID);
    } catch (err: unknown) {
      setError(apiErrorMessage(err, 'Failed to remove member.'));
    }
  };

  return (
    <VStack align="stretch" gap={4}>
      <Text fontSize="sm" color="text.subtle">
        Members can view this agent and issue API keys for it. Removing a member revokes the keys
        they created for this agent.
      </Text>

      {error && (
        <Alert status="error" borderRadius="md" py={2}>
          <AlertIcon />
          <AlertDescription fontSize="sm">{error}</AlertDescription>
        </Alert>
      )}

      {isAdmin && (
        <HStack align="flex-end" gap={3}>
          <FormControl flex={1}>
            <FormLabel fontSize="sm">Add member</FormLabel>
            <NativeSelect
              size="sm"
              value={selectedUser}
              onChange={(e) => setSelectedUser(e.target.value)}
              placeholder={usersQuery.isLoading ? 'Loading users…' : 'Select a user'}
              data-testid="member-select"
            >
              {candidates.map((u) => (
                <option key={u.id} value={u.id}>
                  {u.email || u.name || u.subject}
                  {u.name && u.email ? ` (${u.name})` : ''}
                </option>
              ))}
            </NativeSelect>
            {usersQuery.isSuccess && candidates.length === 0 && (
              <FormHelperText fontSize="xs">
                Every known user is already a member. Users appear here after their first login.
              </FormHelperText>
            )}
          </FormControl>
          <Button
            size="sm"
            variant="primary"
            isDisabled={!selectedUser}
            isLoading={addMember.isLoading}
            onClick={handleAdd}
          >
            Add
          </Button>
        </HStack>
      )}

      {membersQuery.isLoading ? (
        <HStack justify="center" py={4}>
          <Spinner size="sm" />
        </HStack>
      ) : membersQuery.isError ? (
        <Alert status="error" borderRadius="md" py={2}>
          <AlertIcon />
          <AlertDescription fontSize="sm">
            {apiErrorMessage(membersQuery.error, 'Failed to load members.')}
          </AlertDescription>
        </Alert>
      ) : members.length === 0 ? (
        <Text fontSize="sm" color="text.subtle" textAlign="center" py={2}>
          No members. Only admins can manage this agent.
        </Text>
      ) : (
        <Box borderWidth={1} borderColor="border.base" borderRadius="md" overflowX="auto">
          <Table size="sm" variant="simple">
            <Thead bg="background.table.header">
              <Tr>
                <Th>User</Th>
                <Th>Role</Th>
                <Th>Since</Th>
                <Th />
              </Tr>
            </Thead>
            <Tbody>
              {members.map((m) => {
                const label = m.email || m.name || m.user_id;
                return (
                  <Tr key={m.user_id}>
                    <Td fontSize="sm">
                      <Text>{label}</Text>
                      {m.name && m.email && (
                        <Text fontSize="xs" color="text.subtle">
                          {m.name}
                        </Text>
                      )}
                      {me?.user_id === m.user_id && (
                        <Badge ml={2} fontSize="2xs" colorScheme="blue">
                          you
                        </Badge>
                      )}
                    </Td>
                    <Td>
                      <Badge fontSize="2xs">{m.role}</Badge>
                    </Td>
                    <Td fontSize="xs" color="text.subtle" whiteSpace="nowrap">
                      {formatDate(m.created_at)}
                    </Td>
                    <Td textAlign="right">
                      {isAdmin && (
                        <Button
                          size="xs"
                          variant="outlineDanger"
                          isLoading={removeMember.isLoading && removeMember.variables === m.user_id}
                          onClick={() => handleRemove(m.user_id, label)}
                        >
                          Remove
                        </Button>
                      )}
                    </Td>
                  </Tr>
                );
              })}
            </Tbody>
          </Table>
        </Box>
      )}
    </VStack>
  );
};
