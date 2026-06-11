/* eslint-disable react/jsx-no-bind */
/* eslint-disable react-hooks/exhaustive-deps */
import React, { useEffect, useState } from 'react';
import ResizablePanels from '../components/ResizablePanels';
import {
  Alert,
  AlertDescription,
  AlertIcon,
  Badge,
  Box,
  Button,
  Checkbox,
  Flex,
  FormControl,
  FormLabel,
  HStack,
  Icon,
  Input,
  NumberDecrementStepper,
  NumberIncrementStepper,
  NumberInput,
  NumberInputField,
  NumberInputStepper,
  Select,
  Spinner,
  Stack,
  Table,
  Tbody,
  Td,
  Text,
  Textarea,
  Th,
  Thead,
  Tr,
  VStack,
} from '@chakra-ui/react';
import { CircleStackIcon } from '@heroicons/react/24/outline';

import { ContentPageTitle } from '../components/Layout';
import {
  useServers,
  useCreateServer,
  useUpdateServer,
  useRemoveServer,
  useTestServer,
  useStartConnect,
  useConnectStatus,
} from '../hooks/useServers';
import {
  type AuthStatus,
  type ConnectionStatus,
  type Server,
  type ServerInput,
  type ServerMode,
} from '../api/AtryumAPI';

const CONNECTION_COLOR: Record<ConnectionStatus, string> = {
  ready: 'green',
  unreachable: 'red',
  unknown: 'gray',
};

const AUTH_COLOR: Record<AuthStatus, string> = {
  ok: 'green',
  missing_credentials: 'red',
  invalid: 'red',
  not_tested: 'gray',
  reauth_needed: 'orange',
};

const EMPTY_FORM: ServerInput = {
  name: '',
  mode: 'http',
  base_url: '',
  timeout_seconds: 30,
  auth_token: '',
  command: '',
  args: [],
  env: {},
  enabled: true,
  oauth_client_id: '',
  oauth_client_secret: '',
  oauth_authorize_url: '',
  oauth_token_url: '',
  oauth_scopes: '',
};

const errorMessage = (err: unknown, fallback: string): string => {
  if (err instanceof Error) return err.message;
  if (typeof err === 'object' && err !== null && 'message' in err) {
    const msg = (err as { message: unknown }).message;
    if (typeof msg === 'string') return msg;
  }
  return fallback;
};

const serverToInput = (s: Server): ServerInput => ({
  name: s.name,
  mode: s.mode,
  base_url: s.base_url ?? '',
  timeout_seconds: s.timeout_seconds ?? 30,
  auth_token: s.auth_token ?? '',
  command: s.command ?? '',
  args: s.args ?? [],
  env: s.env ?? {},
  enabled: s.enabled,
  oauth_client_id: s.oauth_client_id ?? '',
  oauth_client_secret: '',
  oauth_authorize_url: s.oauth_authorize_url ?? '',
  oauth_token_url: s.oauth_token_url ?? '',
  oauth_scopes: s.oauth_scopes ?? '',
});

const Servers: React.FC = () => {
  const [showDisabled, setShowDisabled] = useState(true);
  const [selectedName, setSelectedName] = useState<string | null>(null);
  const [isCreating, setIsCreating] = useState(false);
  const [form, setForm] = useState<ServerInput>(EMPTY_FORM);
  const [argsText, setArgsText] = useState('[]');
  const [envText, setEnvText] = useState('{}');
  const [statusMsg, setStatusMsg] = useState<{ text: string; isError: boolean } | null>(null);
  const [connectPolling, setConnectPolling] = useState(false);
  const [useDefaultScopes, setUseDefaultScopes] = useState(true);

  const { data, isLoading, refetch } = useServers(showDisabled);
  const createServer = useCreateServer();
  const updateServer = useUpdateServer();
  const removeServer = useRemoveServer();
  const testServer = useTestServer();
  const startConnect = useStartConnect();

  useConnectStatus(selectedName, connectPolling, (result) => {
    setConnectPolling(false);
    setStatusMsg({
      text:
        result.message ??
        (result.status === 'succeeded'
          ? 'OAuth connect completed.'
          : 'OAuth connect failed.'),
      isError: result.status === 'failed',
    });
  });

  const servers = data?.items ?? [];

  const loadServer = (server: Server) => {
    setSelectedName(server.name);
    setIsCreating(false);
    const input = serverToInput(server);
    setForm(input);
    setArgsText(JSON.stringify(input.args ?? [], null, 2));
    setEnvText(JSON.stringify(input.env ?? {}, null, 2));
    setUseDefaultScopes(!(input.oauth_scopes ?? '').trim());
    setStatusMsg(null);
  };

  const startNew = () => {
    setSelectedName(null);
    setIsCreating(true);
    setForm(EMPTY_FORM);
    setArgsText('[]');
    setEnvText('{}');
    setUseDefaultScopes(true);
    setStatusMsg({ text: 'Fill in the fields below to create a new server.', isError: false });
  };

  useEffect(() => {
    if (!selectedName && !isCreating && servers.length > 0) {
      loadServer(servers[0]);
    }
  }, [servers.length]);

  const parseArgs = (): string[] => {
    try {
      const parsed = JSON.parse(argsText);
      if (!Array.isArray(parsed)) throw new Error('Args must be a JSON array.');
      return parsed;
    } catch {
      throw new Error('Args must be a valid JSON array.');
    }
  };

  const parseEnv = (): Record<string, string> => {
    try {
      const parsed = JSON.parse(envText);
      if (parsed === null || Array.isArray(parsed) || typeof parsed !== 'object')
        throw new Error('Env must be a JSON object.');
      return parsed;
    } catch {
      throw new Error('Env must be a valid JSON object.');
    }
  };

  const handleSave = async () => {
    try {
      const payload: ServerInput = {
        ...form,
        args: form.mode === 'stdio' ? parseArgs() : [],
        env: form.mode === 'stdio' ? parseEnv() : {},
      };
      if (isCreating) {
        const saved = await createServer.mutateAsync(payload);
        setSelectedName(saved.name);
        setIsCreating(false);
        setStatusMsg({ text: `Created server "${saved.name}".`, isError: false });
      } else {
        const saved = await updateServer.mutateAsync({ name: selectedName!, input: payload });
        setStatusMsg({ text: `Saved server "${saved.name}".`, isError: false });
      }
    } catch (err: unknown) {
      setStatusMsg({ text: errorMessage(err, 'Save failed.'), isError: true });
    }
  };

  const handleTest = async () => {
    if (!selectedName) return;
    try {
      const result = await testServer.mutateAsync(selectedName);
      setStatusMsg({ text: result.message, isError: !result.ok });
    } catch (err: unknown) {
      setStatusMsg({ text: errorMessage(err, 'Test failed.'), isError: true });
    }
  };

  const handleToggleEnabled = async () => {
    if (!selectedName) return;
    try {
      const server = servers.find((s) => s.name === selectedName);
      if (!server) return;
      if (server.enabled) {
        await removeServer.mutateAsync({ name: selectedName, disable: true });
        setStatusMsg({ text: `Disabled server "${selectedName}".`, isError: false });
        setForm((f) => ({ ...f, enabled: false }));
      } else {
        await updateServer.mutateAsync({ name: selectedName, input: { ...form, enabled: true } });
        setStatusMsg({ text: `Enabled server "${selectedName}".`, isError: false });
        setForm((f) => ({ ...f, enabled: true }));
      }
    } catch (err: unknown) {
      setStatusMsg({ text: errorMessage(err, 'Toggle failed.'), isError: true });
    }
  };

  const handleConnect = async () => {
    if (!selectedName) return;
    try {
      const result = await startConnect.mutateAsync(selectedName);
      window.open(result.connect_url, '_blank', 'noopener,noreferrer');
      setConnectPolling(true);
      setStatusMsg({ text: 'OAuth flow opened — waiting for completion…', isError: false });
    } catch (err: unknown) {
      setStatusMsg({ text: errorMessage(err, 'Connect failed.'), isError: true });
    }
  };

  const handleDelete = async () => {
    if (!selectedName) return;
    if (!window.confirm(`Delete server "${selectedName}"?`)) return;
    try {
      await removeServer.mutateAsync({ name: selectedName });
      setStatusMsg({ text: `Deleted server "${selectedName}".`, isError: false });
      setSelectedName(null);
      setIsCreating(false);
    } catch (err: unknown) {
      setStatusMsg({ text: errorMessage(err, 'Delete failed.'), isError: true });
    }
  };

  const isBusy =
    createServer.isLoading ||
    updateServer.isLoading ||
    removeServer.isLoading ||
    testServer.isLoading;

  const currentServer = servers.find((s) => s.name === selectedName);

  return (
    <Box h="full" display="flex" flexDirection="column">
      <Stack mb={6}>
        <HStack>
          <Flex width="full" justify="space-between">
            <HStack gap={4} pl={2} color="text.heading">
              <Icon as={CircleStackIcon} boxSize={10} />
              <ContentPageTitle>Servers</ContentPageTitle>
            </HStack>
          </Flex>
        </HStack>
        <Text pl={2} color="text.subtle">
          MCP server connections available to AI agents
        </Text>
      </Stack>

      <Box
        flex={1}
        borderWidth={1}
        borderColor="border.base"
        borderRadius="md"
        overflow="hidden"
        minH={0}
      >
        <ResizablePanels
          initialSplit={0.7}
          minLeft={240}
          minRight={320}
          left={
            <Box display="flex" flexDirection="column" overflow="hidden" h="full">
              <HStack p={3} borderBottomWidth={1} borderColor="border.base" justify="space-between">
                <Button size="sm" variant="primary" onClick={startNew}>
                  New Server
                </Button>
                <HStack gap={3}>
                  <Checkbox
                    size="sm"
                    isChecked={showDisabled}
                    onChange={(e) => setShowDisabled(e.target.checked)}
                  >
                    <Text fontSize="xs">Show disabled</Text>
                  </Checkbox>
                </HStack>
              </HStack>

              <Box flex={1} overflow="auto">
                {isLoading ? (
                  <HStack justify="center" py={8}>
                    <Spinner size="sm" color="brand.base" />
                  </HStack>
                ) : servers.length === 0 ? (
                  <Text p={4} color="text.subtle" fontSize="sm">
                    No servers configured.
                  </Text>
                ) : (
                  <Table size="sm" variant="simple">
                    <Thead position="sticky" top={0} bg="background.table.header" zIndex={1}>
                      <Tr>
                        <Th>Name</Th>
                        <Th>Connection</Th>
                        <Th>Auth</Th>
                      </Tr>
                    </Thead>
                    <Tbody>
                      {servers.map((server) => (
                        <Tr
                          key={server.name}
                          cursor="pointer"
                          opacity={server.enabled ? 1 : 0.5}
                          sx={{
                            '&, &:nth-of-type(odd), &:nth-of-type(even)': {
                              bg: selectedName === server.name ? 'background.table.row.selected' : undefined,
                            },
                            '&:hover, &:nth-of-type(odd):hover, &:nth-of-type(even):hover': {
                              bg: selectedName === server.name
                                ? 'background.table.row.selected'
                                : 'background.table.row.hover',
                            },
                          }}
                          onClick={() => loadServer(server)}
                        >
                          <Td
                            borderLeft="4px solid"
                            borderLeftColor={selectedName === server.name ? 'brand.base' : 'transparent'}
                            pl={selectedName === server.name ? 2 : 3}
                          >
                            <Text fontSize="sm" fontWeight="medium">{server.name}</Text>
                          </Td>
                          <Td>
                            <Badge colorScheme={CONNECTION_COLOR[server.connection_status]} fontSize="2xs">
                              {server.connection_status}
                            </Badge>
                          </Td>
                          <Td>
                            <Badge colorScheme={AUTH_COLOR[server.auth_status]} fontSize="2xs">
                              {server.auth_status}
                            </Badge>
                          </Td>
                        </Tr>
                      ))}
                    </Tbody>
                  </Table>
                )}
              </Box>
            </Box>
          }
          right={
            <Box overflow="auto" p={6} h="full">
              {!selectedName && !isCreating ? (
                <Flex h="full" align="center" justify="center">
                  <Text color="text.subtle">Select a server or click New Server</Text>
                </Flex>
              ) : (
                <VStack align="stretch" gap={5} maxW="560px">
                  {statusMsg && (
                    <Alert status={statusMsg.isError ? 'error' : 'success'} borderRadius="md" py={2}>
                      <AlertIcon />
                      <AlertDescription fontSize="sm">{statusMsg.text}</AlertDescription>
                    </Alert>
                  )}

                  {currentServer && (
                    <HStack gap={2} flexWrap="wrap">
                      <Badge colorScheme={CONNECTION_COLOR[currentServer.connection_status]}>
                        {currentServer.connection_status}
                      </Badge>
                      <Badge colorScheme={AUTH_COLOR[currentServer.auth_status]}>
                        {currentServer.auth_status}
                      </Badge>
                      {currentServer.oauth_provider_label && (
                        <Badge colorScheme="purple" title={`OAuth strategy: ${currentServer.oauth_provider_label}`}>
                          {currentServer.oauth_provider_label}
                        </Badge>
                      )}
                      {(() => {
                        const reg = currentServer.oauth_client_registration;
                        if (!reg) return null;
                        const labels: Record<string, [string, string]> = {
                          dynamic: ['registered dynamically', 'Client registered via RFC 7591 DCR.'],
                          preshared: ['pre-shared client', 'Credentials configured manually.'],
                          cimd: ['client metadata document', 'Server identifies client via CIMD.'],
                        };
                        const entry = labels[reg];
                        if (!entry) return null;
                        return (
                          <Badge colorScheme="cyan" title={entry[1]}>{entry[0]}</Badge>
                        );
                      })()}
                      {!currentServer.enabled && <Badge colorScheme="gray">disabled</Badge>}
                    </HStack>
                  )}

                  <FormControl isRequired>
                    <FormLabel fontSize="sm">Name</FormLabel>
                    <Input
                      size="sm"
                      value={form.name}
                      isReadOnly={!isCreating}
                      onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
                    />
                  </FormControl>

                  <FormControl isRequired>
                    <FormLabel fontSize="sm">Mode</FormLabel>
                    <Select
                      size="sm"
                      value={form.mode}
                      onChange={(e) => setForm((f) => ({ ...f, mode: e.target.value as ServerMode }))}
                    >
                      <option value="http">HTTP</option>
                      <option value="stdio">stdio</option>
                    </Select>
                  </FormControl>

                  {form.mode === 'http' && (
                    <>
                      <FormControl>
                        <FormLabel fontSize="sm">Base URL</FormLabel>
                        <Input
                          size="sm"
                          placeholder="https://example.com/mcp"
                          value={form.base_url ?? ''}
                          onChange={(e) => setForm((f) => ({ ...f, base_url: e.target.value }))}
                        />
                      </FormControl>
                      <FormControl>
                        <FormLabel fontSize="sm">Timeout (seconds)</FormLabel>
                        <NumberInput
                          size="sm"
                          min={1}
                          max={300}
                          value={form.timeout_seconds ?? 30}
                          onChange={(_, val) =>
                            setForm((f) => ({ ...f, timeout_seconds: isNaN(val) ? 30 : val }))
                          }
                        >
                          <NumberInputField />
                          <NumberInputStepper>
                            <NumberIncrementStepper />
                            <NumberDecrementStepper />
                          </NumberInputStepper>
                        </NumberInput>
                      </FormControl>
                      {(() => {
                        const providerID = currentServer?.oauth_provider_id ?? '';
                        const isOAuthManaged =
                          providerID === 'oauth_pkce' ||
                          providerID === 'oauth_client_secret' ||
                          providerID === 'oauth_dcr' ||
                          providerID === 'oauth_metadata';
                        if (isOAuthManaged) return null;
                        return (
                          <FormControl>
                            <FormLabel fontSize="sm">Bearer Token</FormLabel>
                            <Input
                              size="sm"
                              type="password"
                              placeholder="sk-…"
                              value={form.auth_token ?? ''}
                              onChange={(e) => setForm((f) => ({ ...f, auth_token: e.target.value }))}
                            />
                          </FormControl>
                        );
                      })()}

                      <Box as="details" borderTop="1px solid" borderColor="gray.200" pt={2}>
                        <Box as="summary" cursor="pointer" fontSize="sm" fontWeight="medium">
                          Manual OAuth configuration (advanced)
                        </Box>
                        <Stack mt={2} spacing={2}>
                          <Text fontSize="xs" color="gray.600">
                            Use when the server does not support Dynamic Client Registration.
                            {currentServer?.has_oauth_client_secret && (
                              <> <strong>A client_secret is currently stored.</strong></>
                            )}
                          </Text>
                          <FormControl>
                            <FormLabel fontSize="sm">Client ID</FormLabel>
                            <Input
                              size="sm"
                              placeholder="abcdef1234.5678"
                              value={form.oauth_client_id ?? ''}
                              onChange={(e) => setForm((f) => ({ ...f, oauth_client_id: e.target.value }))}
                            />
                          </FormControl>
                          <FormControl>
                            <FormLabel fontSize="sm">
                              Client Secret
                              {currentServer?.has_oauth_client_secret && (
                                <Text as="span" color="gray.500" ml={1} fontWeight="normal">
                                  (leave blank to keep stored value)
                                </Text>
                              )}
                            </FormLabel>
                            <Input
                              size="sm"
                              type="password"
                              placeholder={currentServer?.has_oauth_client_secret ? '••••••• (unchanged)' : ''}
                              value={form.oauth_client_secret ?? ''}
                              onChange={(e) => setForm((f) => ({ ...f, oauth_client_secret: e.target.value }))}
                            />
                          </FormControl>
                          <FormControl>
                            <FormLabel fontSize="sm">Authorize URL</FormLabel>
                            <Input
                              size="sm"
                              placeholder="auto-discovered when blank"
                              value={form.oauth_authorize_url ?? ''}
                              onChange={(e) => setForm((f) => ({ ...f, oauth_authorize_url: e.target.value }))}
                            />
                          </FormControl>
                          <FormControl>
                            <FormLabel fontSize="sm">Token URL</FormLabel>
                            <Input
                              size="sm"
                              placeholder="auto-discovered when blank"
                              value={form.oauth_token_url ?? ''}
                              onChange={(e) => setForm((f) => ({ ...f, oauth_token_url: e.target.value }))}
                            />
                          </FormControl>
                          <FormControl>
                            <Checkbox
                              isChecked={useDefaultScopes}
                              onChange={(e) => {
                                const checked = e.target.checked;
                                setUseDefaultScopes(checked);
                                if (checked) setForm((f) => ({ ...f, oauth_scopes: '' }));
                              }}
                            >
                              <Text fontSize="sm">
                                Use default scopes
                                <Text as="span" color="gray.500" ml={1}>
                                  (whatever the OAuth app declared with the provider)
                                </Text>
                              </Text>
                            </Checkbox>
                          </FormControl>
                          {!useDefaultScopes && (
                            <FormControl>
                              <FormLabel fontSize="sm">Scopes (requested)</FormLabel>
                              <Input
                                size="sm"
                                placeholder="space-separated, e.g. chat:write channels:history"
                                value={form.oauth_scopes ?? ''}
                                onChange={(e) => setForm((f) => ({ ...f, oauth_scopes: e.target.value }))}
                              />
                            </FormControl>
                          )}
                          {currentServer?.oauth_granted_scopes && (
                            <FormControl>
                              <FormLabel fontSize="sm">
                                Granted scopes
                                <Text as="span" color="gray.500" ml={1} fontWeight="normal">
                                  (from latest token exchange)
                                </Text>
                              </FormLabel>
                              <Textarea
                                size="sm"
                                rows={3}
                                isReadOnly
                                fontFamily="mono"
                                fontSize="xs"
                                value={currentServer.oauth_granted_scopes}
                              />
                            </FormControl>
                          )}
                        </Stack>
                      </Box>
                    </>
                  )}

                  {form.mode === 'stdio' && (
                    <>
                      <FormControl>
                        <FormLabel fontSize="sm">Command</FormLabel>
                        <Input
                          size="sm"
                          placeholder="npx"
                          value={form.command ?? ''}
                          onChange={(e) => setForm((f) => ({ ...f, command: e.target.value }))}
                        />
                      </FormControl>
                      <FormControl>
                        <FormLabel fontSize="sm">Args (JSON array)</FormLabel>
                        <Textarea
                          size="sm"
                          fontFamily="mono"
                          fontSize="xs"
                          rows={3}
                          value={argsText}
                          onChange={(e) => setArgsText(e.target.value)}
                          placeholder='["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]'
                        />
                      </FormControl>
                      <FormControl>
                        <FormLabel fontSize="sm">Env (JSON object)</FormLabel>
                        <Textarea
                          size="sm"
                          fontFamily="mono"
                          fontSize="xs"
                          rows={3}
                          value={envText}
                          onChange={(e) => setEnvText(e.target.value)}
                          placeholder='{"MY_VAR": "value"}'
                        />
                      </FormControl>
                    </>
                  )}

                  <Checkbox
                    isChecked={form.enabled}
                    onChange={(e) => setForm((f) => ({ ...f, enabled: e.target.checked }))}
                  >
                    <Text fontSize="sm">Enabled</Text>
                  </Checkbox>

                  <HStack gap={2} flexWrap="wrap">
                    <Button
                      variant="primary"
                      size="sm"
                      isLoading={createServer.isLoading || updateServer.isLoading}
                      onClick={handleSave}
                    >
                      {isCreating ? 'Create Server' : 'Save'}
                    </Button>
                    {!isCreating && (
                      <>
                        <Button
                          variant="outline"
                          size="sm"
                          isLoading={testServer.isLoading}
                          isDisabled={isBusy}
                          onClick={handleTest}
                        >
                          Test
                        </Button>
                        {(() => {
                          if (!currentServer) return null;
                          if (currentServer.mode !== 'http') return null;
                          if (!currentServer.base_url) return null;
                          const providerID = currentServer.oauth_provider_id ?? '';
                          const isStatic =
                            providerID === 'bearer_token' || providerID === 'custom_headers';
                          if (isStatic) return null;
                          const needsReauth =
                            currentServer.reauth_needed ||
                            currentServer.auth_status === 'missing_credentials' ||
                            currentServer.auth_status === 'invalid' ||
                            currentServer.auth_status === 'reauth_needed';
                          const label = needsReauth ? 'Reconnect' : 'Connect';
                          const title = currentServer.oauth_provider_label
                            ? `${label} via ${currentServer.oauth_provider_label}`
                            : `${label} via OAuth (discovery will run on click)`;
                          return (
                            <Button
                              variant="outline"
                              size="sm"
                              isLoading={startConnect.isLoading || connectPolling}
                              isDisabled={isBusy}
                              onClick={handleConnect}
                              title={title}
                            >
                              {connectPolling ? 'Connecting…' : label}
                            </Button>
                          );
                        })()}
                        <Button
                          variant="outline"
                          size="sm"
                          isLoading={removeServer.isLoading}
                          isDisabled={isBusy}
                          onClick={handleToggleEnabled}
                        >
                          {currentServer?.enabled ? 'Disable' : 'Enable'}
                        </Button>
                        <Button
                          variant="outlineDanger"
                          size="sm"
                          isLoading={removeServer.isLoading}
                          isDisabled={isBusy}
                          onClick={handleDelete}
                        >
                          Delete
                        </Button>
                      </>
                    )}
                    <Button
                      variant="ghost"
                      size="sm"
                      isDisabled={isBusy}
                      onClick={
                        isCreating
                          ? startNew
                          : () => currentServer ? loadServer(currentServer) : undefined
                      }
                    >
                      Reset
                    </Button>
                  </HStack>
                </VStack>
              )}
            </Box>
          }
        />
      </Box>
    </Box>
  );
};

export default Servers;
