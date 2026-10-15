import React, { useState } from 'react';
import {
  Alert,
  AlertDescription,
  AlertIcon,
  Badge,
  Box,
  Button,
  Code,
  HStack,
  Icon,
  Link,
  Spinner,
  Stack,
  Tab,
  TabList,
  TabPanel,
  TabPanels,
  Tabs,
  Table,
  Tbody,
  Td,
  Text,
  Th,
  Thead,
  Tr,
} from '@chakra-ui/react';
import { ArrowLeftIcon, UserIcon } from '@heroicons/react/24/outline';
import { Link as RouterLink, useLocation, useNavigate, useParams } from 'react-router-dom';

import { ContentPageTitle } from '../components/Layout';
import { keyStatus } from '../components/AgentAccess';
import type { AgentAPIKey, AtryumUser, UserAgent } from '../api/AtryumAPI';
import { apiErrorMessage } from '../api/AtryumAPI';
import {
  useMe,
  useRemoveUserFromAgent,
  useRevokeUserKey,
  useUser,
  useUserAgents,
  useUserKeys,
} from '../hooks/useIdentity';

export type UserDetailTab = 'agents' | 'keys';

const TABS: UserDetailTab[] = ['agents', 'keys'];

/** Which tab a /users/:id/... path points at; anything unrecognised opens Agents. */
export const userDetailTab = (pathname: string): UserDetailTab => {
  const last = pathname.replace(/\/+$/, '').split('/').pop();
  return last === 'keys' ? 'keys' : 'agents';
};

export const userDisplayName = (user: Pick<AtryumUser, 'email' | 'name' | 'subject'>): string =>
  user.email || user.name || user.subject;

const formatDate = (iso?: string | null): string => {
  if (!iso) return '—';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '—' : d.toLocaleString();
};

// ─── Agents tab ──────────────────────────────────────────────────────────────

const UserAgentsTable: React.FC<{ user: AtryumUser }> = ({ user }) => {
  const query = useUserAgents(user.id);
  const remove = useRemoveUserFromAgent(user.id);
  const [error, setError] = useState<string | null>(null);
  const agents = query.data?.items ?? [];

  const handleRemove = async (agent: UserAgent) => {
    if (
      !window.confirm(
        `Remove ${userDisplayName(user)} from ${agent.agent_name || agent.agent_id}? Their keys for this agent will be revoked.`,
      )
    ) {
      return;
    }
    setError(null);
    try {
      await remove.mutateAsync(agent.agent_id);
    } catch (err: unknown) {
      setError(apiErrorMessage(err, 'Failed to remove membership.'));
    }
  };

  return (
    <Stack gap={4}>
      <Text fontSize="sm" color="text.subtle">
        Agents this user is a member of. Members can view the agent and issue API keys for it.
        To add the user to an agent, open that agent&apos;s <strong>Members</strong> tab on the{' '}
        <Link as={RouterLink} to="/agents" color="primary.500">
          Agents
        </Link>{' '}
        page.
      </Text>

      {error && (
        <Alert status="error" borderRadius="md" py={2}>
          <AlertIcon />
          <AlertDescription fontSize="sm">{error}</AlertDescription>
        </Alert>
      )}

      {query.isLoading ? (
        <HStack justify="center" py={8}>
          <Spinner size="sm" />
          <Text color="text.subtle" fontSize="sm">
            Loading agents…
          </Text>
        </HStack>
      ) : query.isError ? (
        <Alert status="error" borderRadius="md">
          <AlertIcon />
          <AlertDescription fontSize="sm">
            {apiErrorMessage(query.error, 'Failed to load agents.')}
          </AlertDescription>
        </Alert>
      ) : agents.length === 0 ? (
        <Text fontSize="sm" color="text.subtle" textAlign="center" py={8}>
          {user.role === 'admin'
            ? 'No memberships. Admins can see every agent without being a member.'
            : 'Not a member of any agent yet.'}
        </Text>
      ) : (
        <Box borderWidth={1} borderColor="border.base" borderRadius="md" overflowX="auto">
          <Table size="sm" variant="simple">
            <Thead bg="background.table.header">
              <Tr>
                <Th>Agent</Th>
                <Th>Status</Th>
                <Th>Role</Th>
                <Th>Member since</Th>
                <Th />
              </Tr>
            </Thead>
            <Tbody>
              {agents.map((agent) => (
                <Tr
                  key={agent.agent_id}
                  opacity={agent.enabled ? 1 : 0.6}
                  data-testid={`user-agent-row-${agent.agent_id}`}
                >
                  <Td fontSize="sm">
                    <Text fontWeight="medium">{agent.agent_name || agent.agent_id}</Text>
                    <Text fontSize="xs" color="text.subtle" fontFamily="mono">
                      {agent.agent_id}
                    </Text>
                  </Td>
                  <Td>
                    <Badge colorScheme={agent.enabled ? 'green' : 'gray'} fontSize="2xs">
                      {agent.enabled ? 'enabled' : 'disabled'}
                    </Badge>
                  </Td>
                  <Td>
                    <Badge fontSize="2xs">{agent.role}</Badge>
                  </Td>
                  <Td fontSize="xs" color="text.subtle" whiteSpace="nowrap">
                    {formatDate(agent.created_at)}
                  </Td>
                  <Td textAlign="right">
                    <Button
                      size="xs"
                      variant="outlineDanger"
                      isLoading={remove.isLoading && remove.variables === agent.agent_id}
                      onClick={() => handleRemove(agent)}
                    >
                      Remove
                    </Button>
                  </Td>
                </Tr>
              ))}
            </Tbody>
          </Table>
        </Box>
      )}
    </Stack>
  );
};

// ─── API keys tab ────────────────────────────────────────────────────────────

const UserKeysTable: React.FC<{ user: AtryumUser }> = ({ user }) => {
  const query = useUserKeys(user.id);
  const revoke = useRevokeUserKey(user.id);
  const [error, setError] = useState<string | null>(null);
  const keys = query.data?.items ?? [];
  const activeCount = keys.filter((k) => k.active).length;

  const handleRevoke = async (key: AgentAPIKey) => {
    const label = key.name ? `"${key.name}" (${key.key_prefix}…)` : `${key.key_prefix}…`;
    if (!window.confirm(`Revoke key ${label}? Anything using it will stop authenticating immediately.`)) {
      return;
    }
    setError(null);
    try {
      await revoke.mutateAsync({ agentID: key.agent_id, keyID: key.id });
    } catch (err: unknown) {
      setError(apiErrorMessage(err, 'Failed to revoke key.'));
    }
  };

  return (
    <Stack gap={4}>
      <HStack justify="space-between" align="flex-start">
        <Text fontSize="sm" color="text.subtle">
          Every API key this user has issued, across all agents. Requests made with a key are
          attributed to both the agent and this user. New keys are issued from an agent&apos;s{' '}
          <strong>API keys</strong> tab.
        </Text>
        <Badge colorScheme={activeCount > 0 ? 'green' : 'gray'} fontSize="2xs" flexShrink={0}>
          {activeCount} active
        </Badge>
      </HStack>

      {error && (
        <Alert status="error" borderRadius="md" py={2}>
          <AlertIcon />
          <AlertDescription fontSize="sm">{error}</AlertDescription>
        </Alert>
      )}

      {query.isLoading ? (
        <HStack justify="center" py={8}>
          <Spinner size="sm" />
          <Text color="text.subtle" fontSize="sm">
            Loading keys…
          </Text>
        </HStack>
      ) : query.isError ? (
        <Alert status="error" borderRadius="md">
          <AlertIcon />
          <AlertDescription fontSize="sm">
            {apiErrorMessage(query.error, 'Failed to load keys.')}
          </AlertDescription>
        </Alert>
      ) : keys.length === 0 ? (
        <Text fontSize="sm" color="text.subtle" textAlign="center" py={8}>
          This user has not issued any API keys.
        </Text>
      ) : (
        <Box borderWidth={1} borderColor="border.base" borderRadius="md" overflowX="auto">
          <Table size="sm" variant="simple">
            <Thead bg="background.table.header">
              <Tr>
                <Th>Agent</Th>
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
                  <Tr key={key.id} opacity={key.active ? 1 : 0.6} data-testid={`user-key-row-${key.id}`}>
                    <Td fontSize="sm">
                      <Text fontWeight="medium">{key.agent_name || key.agent_id}</Text>
                      {key.agent_name && (
                        <Text fontSize="xs" color="text.subtle" fontFamily="mono">
                          {key.agent_id}
                        </Text>
                      )}
                    </Td>
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
                          isLoading={revoke.isLoading && revoke.variables?.keyID === key.id}
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
    </Stack>
  );
};

// ─── Page ────────────────────────────────────────────────────────────────────

const UserDetail: React.FC = () => {
  const { id = '' } = useParams<{ id: string }>();
  const location = useLocation();
  const navigate = useNavigate();
  const { data: me } = useMe();
  const userQuery = useUser(id);
  const agentsQuery = useUserAgents(id);
  const keysQuery = useUserKeys(id);

  const tab = userDetailTab(location.pathname);
  const user = userQuery.data;
  const isSelf = me?.user_id === id;
  const agentCount = agentsQuery.data?.items.length;
  const activeKeyCount = keysQuery.data?.items.filter((k) => k.active).length;

  return (
    <Box>
      <Stack mb={6} gap={3}>
        <Link
          as={RouterLink}
          to="/users"
          fontSize="sm"
          color="text.subtle"
          display="inline-flex"
          alignItems="center"
          gap={1}
          w="fit-content"
          pl={2}
        >
          <Icon as={ArrowLeftIcon} boxSize={3.5} />
          All users
        </Link>
        <HStack gap={4} pl={2} color="text.heading" align="center">
          <Icon as={UserIcon} boxSize={10} />
          <Stack gap={1}>
            <HStack gap={3} align="center" wrap="wrap">
              <ContentPageTitle>
                {user ? userDisplayName(user) : userQuery.isLoading ? 'Loading…' : 'User'}
              </ContentPageTitle>
              {user && (
                <HStack gap={2}>
                  <Badge fontSize="2xs">{user.role}</Badge>
                  <Badge colorScheme={user.disabled ? 'red' : 'green'} fontSize="2xs">
                    {user.disabled ? 'disabled' : 'active'}
                  </Badge>
                  {isSelf && (
                    <Badge fontSize="2xs" colorScheme="blue">
                      you
                    </Badge>
                  )}
                </HStack>
              )}
            </HStack>
            {user && (
              <Text fontSize="sm" color="text.subtle" fontWeight="normal">
                {user.name && user.email ? `${user.name} · ` : ''}
                <Text as="span" fontFamily="mono" fontSize="xs">
                  {user.issuer.replace(/^https?:\/\//, '')} · {user.subject}
                </Text>
              </Text>
            )}
          </Stack>
        </HStack>
      </Stack>

      {userQuery.isError ? (
        <Alert status="error" borderRadius="md">
          <AlertIcon />
          <AlertDescription>{apiErrorMessage(userQuery.error, 'Failed to load user.')}</AlertDescription>
        </Alert>
      ) : userQuery.isLoading || !user ? (
        <HStack justify="center" py={8}>
          <Spinner size="sm" />
        </HStack>
      ) : (
        <Tabs
          index={TABS.indexOf(tab)}
          onChange={(i) => navigate(`/users/${encodeURIComponent(id)}/${TABS[i]}`, { replace: true })}
          variant="enclosed"
          size="sm"
        >
          <TabList>
            <Tab data-testid="user-tab-agents">
              Agents
              {agentCount !== undefined && (
                <Badge ml={2} fontSize="2xs">
                  {agentCount}
                </Badge>
              )}
            </Tab>
            <Tab data-testid="user-tab-keys">
              API keys
              {activeKeyCount !== undefined && (
                <Badge ml={2} fontSize="2xs" colorScheme={activeKeyCount > 0 ? 'green' : 'gray'}>
                  {activeKeyCount}
                </Badge>
              )}
            </Tab>
          </TabList>
          <TabPanels>
            <TabPanel px={0}>
              <UserAgentsTable user={user} />
            </TabPanel>
            <TabPanel px={0}>
              <UserKeysTable user={user} />
            </TabPanel>
          </TabPanels>
        </Tabs>
      )}
    </Box>
  );
};

export default UserDetail;
