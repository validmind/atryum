import React, { useState } from 'react';
import {
  Alert,
  AlertDescription,
  AlertIcon,
  Badge,
  Box,
  Button,
  Flex,
  HStack,
  Icon,
  Link,
  Select as NativeSelect,
  Spinner,
  Stack,
  Table,
  Tbody,
  Td,
  Text,
  Th,
  Thead,
  Tr,
} from '@chakra-ui/react';
import { UsersIcon } from '@heroicons/react/24/outline';
import { Link as RouterLink } from 'react-router-dom';

import { ContentPageTitle } from '../components/Layout';
import type { AtryumUser, UserRole } from '../api/AtryumAPI';
import { apiErrorMessage } from '../api/AtryumAPI';
import { useMe, useUpdateUser, useUsers } from '../hooks/useIdentity';

const formatDate = (iso?: string | null): string => {
  if (!iso) return '—';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleString();
};

const Users: React.FC = () => {
  const { data, isLoading, isError, error } = useUsers();
  const { data: me } = useMe();
  const updateUser = useUpdateUser();
  const [actionError, setActionError] = useState<string | null>(null);

  const users = data?.items ?? [];

  const setDisabled = async (user: AtryumUser, disabled: boolean) => {
    const label = user.email || user.name || user.subject;
    if (
      disabled &&
      !window.confirm(
        `Disable ${label}? Every API key they issued will be revoked and they will lose access to their agents.`,
      )
    ) {
      return;
    }
    setActionError(null);
    try {
      await updateUser.mutateAsync({ id: user.id, input: { disabled } });
    } catch (err: unknown) {
      setActionError(apiErrorMessage(err, 'Failed to update user.'));
    }
  };

  const setRole = async (user: AtryumUser, role: UserRole) => {
    setActionError(null);
    try {
      await updateUser.mutateAsync({ id: user.id, input: { role } });
    } catch (err: unknown) {
      setActionError(apiErrorMessage(err, 'Failed to update role.'));
    }
  };

  return (
    <Box>
      <Stack mb={6}>
        <HStack>
          <Flex width="full" justify="space-between">
            <HStack gap={4} pl={2} color="text.heading">
              <Icon as={UsersIcon} boxSize={10} />
              <ContentPageTitle>Users</ContentPageTitle>
            </HStack>
          </Flex>
        </HStack>
        <Text pl={2} color="text.subtle">
          Users are created automatically the first time they sign in through your identity
          provider. Disabling a user revokes every API key they issued and removes them from
          their agents. Roles follow the identity provider&apos;s admin claim until you change one
          here; a role set here is kept across logins.
        </Text>
      </Stack>

      {isError && (
        <Alert status="error" mb={4} borderRadius="md">
          <AlertIcon />
          <AlertDescription>{apiErrorMessage(error, 'Failed to load users.')}</AlertDescription>
        </Alert>
      )}
      {actionError && (
        <Alert status="error" mb={4} borderRadius="md">
          <AlertIcon />
          <AlertDescription fontSize="sm">{actionError}</AlertDescription>
        </Alert>
      )}

      <Box borderWidth={1} borderColor="border.base" borderRadius="md" overflow="hidden">
        <Table variant="simple" size="sm">
          <Thead bg="background.table.header">
            <Tr>
              <Th>User</Th>
              <Th>Identity</Th>
              <Th>Role</Th>
              <Th>Status</Th>
              <Th>Last login</Th>
              <Th>Access</Th>
              <Th />
            </Tr>
          </Thead>
          <Tbody>
            {isLoading ? (
              <Tr>
                <Td colSpan={7}>
                  <HStack justify="center" py={8}>
                    <Spinner size="sm" />
                    <Text color="text.subtle" fontSize="sm">
                      Loading users…
                    </Text>
                  </HStack>
                </Td>
              </Tr>
            ) : users.length === 0 ? (
              <Tr>
                <Td colSpan={7}>
                  <Text textAlign="center" py={8} color="text.subtle" fontSize="sm">
                    No users yet. Users appear here after their first sign-in.
                  </Text>
                </Td>
              </Tr>
            ) : (
              users.map((user) => {
                const isSelf = me?.user_id === user.id;
                const busy = updateUser.isLoading && updateUser.variables?.id === user.id;
                return (
                  <Tr key={user.id} opacity={user.disabled ? 0.5 : 1} data-testid={`user-row-${user.id}`}>
                    <Td fontSize="sm">
                      <HStack gap={2} align="center">
                        <Link
                          as={RouterLink}
                          to={`/users/${encodeURIComponent(user.id)}/agents`}
                          fontWeight="medium"
                          data-testid={`user-link-${user.id}`}
                        >
                          {user.email || user.name || user.subject}
                        </Link>
                        {isSelf && (
                          <Badge fontSize="2xs" colorScheme="blue">
                            you
                          </Badge>
                        )}
                      </HStack>
                      {user.name && user.email && (
                        <Text fontSize="xs" color="text.subtle">
                          {user.name}
                        </Text>
                      )}
                    </Td>
                    <Td maxW="260px">
                      <Text fontSize="xs" color="text.subtle" fontFamily="mono" noOfLines={1} title={`${user.issuer} · ${user.subject}`}>
                        {user.issuer.replace(/^https?:\/\//, '')} · {user.subject}
                      </Text>
                    </Td>
                    <Td>
                      <NativeSelect
                        size="xs"
                        w="110px"
                        value={user.role}
                        isDisabled={busy || isSelf}
                        title={
                          isSelf
                            ? 'You cannot change your own role'
                            : user.role_source === 'manual'
                              ? 'Set by an operator; logins will not change it'
                              : 'Follows the identity provider admin claim; changing it here makes it stick'
                        }
                        onChange={(e) => setRole(user, e.target.value as UserRole)}
                      >
                        <option value="member">member</option>
                        <option value="admin">admin</option>
                      </NativeSelect>
                    </Td>
                    <Td>
                      <Badge colorScheme={user.disabled ? 'red' : 'green'} fontSize="2xs">
                        {user.disabled ? 'disabled' : 'active'}
                      </Badge>
                    </Td>
                    <Td whiteSpace="nowrap" fontSize="sm" color="text.subtle">
                      {formatDate(user.last_login_at)}
                    </Td>
                    <Td whiteSpace="nowrap">
                      <HStack gap={1}>
                        <Button
                          as={RouterLink}
                          to={`/users/${encodeURIComponent(user.id)}/agents`}
                          size="xs"
                          variant="ghost"
                        >
                          Agents
                        </Button>
                        <Button
                          as={RouterLink}
                          to={`/users/${encodeURIComponent(user.id)}/keys`}
                          size="xs"
                          variant="ghost"
                        >
                          Keys
                        </Button>
                      </HStack>
                    </Td>
                    <Td textAlign="right">
                      {user.disabled ? (
                        <Button size="xs" variant="outline" isLoading={busy} onClick={() => setDisabled(user, false)}>
                          Re-enable
                        </Button>
                      ) : (
                        <Button
                          size="xs"
                          variant="outlineDanger"
                          isLoading={busy}
                          isDisabled={isSelf}
                          title={isSelf ? 'You cannot disable yourself' : undefined}
                          onClick={() => setDisabled(user, true)}
                        >
                          Disable
                        </Button>
                      )}
                    </Td>
                  </Tr>
                );
              })
            )}
          </Tbody>
        </Table>
      </Box>
    </Box>
  );
};

export default Users;
