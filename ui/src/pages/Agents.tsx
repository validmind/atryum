import React, { useCallback, useMemo, useState } from 'react';
import {
  Alert,
  AlertDescription,
  AlertIcon,
  Badge,
  Box,
  Button,
  Checkbox,
  Divider,
  Flex,
  FormControl,
  FormLabel,
  HStack,
  Icon,
  Input,
  Modal,
  ModalBody,
  ModalCloseButton,
  ModalContent,
  ModalFooter,
  ModalHeader,
  ModalOverlay,
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
  useDisclosure,
} from '@chakra-ui/react';
import { CreatableSelect, Select } from 'chakra-react-select';
import { CpuChipIcon } from '@heroicons/react/24/outline';
import { useQuery } from 'react-query';

import { ContentPageTitle } from '../components/Layout';
import { useAgents, useCreateAgent, useUpdateAgent, useDeleteAgent } from '../hooks/useAgents';
import { useSettings } from '../hooks/useSettings';
import type {
  Agent,
  AgentCreateInput,
  AgentUpdateInput,
  ClaudeManagedAgent,
  ClaudeManagedAgentBinding,
} from '../api/AtryumAPI';
import { agentsApi, apiErrorMessage } from '../api/AtryumAPI';

type SelectOption = { value: string; label: string };
type ManagedAgentOption = {
  value: string;
  label: string;
  binding: ClaudeManagedAgentBinding;
};
const toOptions = (ids: string[]): SelectOption[] =>
  ids.map((id) => ({ value: id, label: id }));
const fromOptions = (opts: readonly SelectOption[]): string[] =>
  opts.map((o) => o.value);

const formatDate = (iso: string): string => {
  try {
    return new Intl.DateTimeFormat('en-US', {
      dateStyle: 'medium',
      timeStyle: 'short',
    }).format(new Date(iso));
  } catch {
    return iso;
  }
};

const statusCode = (err: unknown): number | undefined => {
  if (typeof err !== 'object' || err === null || !('response' in err)) return undefined;
  return (err as { response?: { status?: number } }).response?.status;
};

const bindingKey = (binding: ClaudeManagedAgentBinding): string =>
  `${binding.account || 'default'}:${binding.claude_agent_id}`;

const bindingLabel = (binding: ClaudeManagedAgentBinding): string => {
  const name = binding.claude_agent_name || binding.claude_agent_id;
  return `${name} (${binding.claude_agent_id})`;
};

const toManagedAgentBinding = (
  agent: ClaudeManagedAgent,
  account: string,
): ClaudeManagedAgentBinding => ({
  account: account || 'default',
  claude_agent_id: agent.id,
  claude_agent_name: agent.name,
  claude_agent_model: agent.model,
  claude_agent_version: agent.version,
});

const toManagedAgentOption = (binding: ClaudeManagedAgentBinding): ManagedAgentOption => ({
  value: bindingKey(binding),
  label: bindingLabel(binding),
  binding,
});

// ─── Create Modal ─────────────────────────────────────────────────────────────

type CreateAgentModalProps = {
  isOpen: boolean;
  onClose: () => void;
};

type StatusMsg = { text: string; isError: boolean; lines?: string[] };

const CreateAgentModal: React.FC<CreateAgentModalProps> = ({ isOpen, onClose }) => {
  const [form, setForm] = useState<AgentCreateInput>({
    name: '',
    description: '',
    charter: '',
    enabled: true,
    agent_ids: [],
  });
  const [statusMsg, setStatusMsg] = useState<StatusMsg | null>(null);
  const createMutation = useCreateAgent();
  const { data: agentsData } = useAgents();
  const noOptionsMessage = useCallback(() => null, []);

  const handleClose = () => {
    setForm({ name: '', description: '', charter: '', enabled: true, agent_ids: [] });
    setStatusMsg(null);
    onClose();
  };

  const handleCreate = async () => {
    if (!form.name.trim()) {
      setStatusMsg({ text: 'Name is required.', isError: true });
      return;
    }
    const conflicts = (form.agent_ids ?? []).flatMap((id) => {
      const owner = agentsData?.items.find((a) => a.agent_ids.includes(id));
      return owner ? [`${id} is already in use by "${owner.name}"`] : [];
    });
    if (conflicts.length > 0) {
      setStatusMsg({
        text: 'Agent ID(s) already in use by another agent:',
        lines: conflicts,
        isError: true,
      });
      return;
    }
    try {
      await createMutation.mutateAsync(form);
      handleClose();
    } catch (err: unknown) {
      setStatusMsg({ text: apiErrorMessage(err, 'Create failed.'), isError: true });
    }
  };

  return (
    <Modal size="md" isCentered isOpen={isOpen} onClose={handleClose}>
      <ModalOverlay />
      <ModalContent>
        <ModalHeader>New Agent</ModalHeader>
        <ModalCloseButton />
        <ModalBody>
          <VStack align="stretch" gap={4}>
            {statusMsg && (
              <Alert status={statusMsg.isError ? 'error' : 'success'} borderRadius="md" py={2}>
                <AlertIcon />
                <AlertDescription fontSize="sm">
                  <Text>{statusMsg.text}</Text>
                  {statusMsg.lines && (
                    <Stack as="ul" mt={1} gap={0} pl={4} listStyleType="disc">
                      {statusMsg.lines.map((line) => (
                        <Text as="li" key={line} fontSize="sm">
                          {line}
                        </Text>
                      ))}
                    </Stack>
                  )}
                </AlertDescription>
              </Alert>
            )}
            <FormControl isRequired>
              <FormLabel fontSize="sm">Name</FormLabel>
              <Input
                size="sm"
                placeholder="e.g. Security Audit Agent"
                value={form.name}
                onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
              />
            </FormControl>
            <FormControl>
              <FormLabel fontSize="sm">Description (optional)</FormLabel>
              <Textarea
                size="sm"
                placeholder="What does this agent do?"
                value={form.description ?? ''}
                onChange={(e) => setForm((f) => ({ ...f, description: e.target.value }))}
                rows={3}
              />
            </FormControl>
            <FormControl>
              <FormLabel fontSize="sm">Charter (optional)</FormLabel>
              <Textarea
                size="sm"
                placeholder="Define the rules and constraints governing this agent's behavior. Used by local LLM-as-judge evaluation rules."
                value={form.charter ?? ''}
                onChange={(e) => setForm((f) => ({ ...f, charter: e.target.value }))}
                rows={5}
                fontFamily="mono"
                fontSize="xs"
              />
            </FormControl>
            <FormControl>
              <FormLabel fontSize="sm">Agent IDs (optional)</FormLabel>
              <Text fontSize="xs" color="text.subtle" mb={2}>
                JWT <code>sub</code> claims or client IDs. Type an ID and press Enter.
              </Text>
              <CreatableSelect
                isMulti
                isClearable
                placeholder="Type an agent ID and press Enter…"
                value={toOptions(form.agent_ids ?? [])}
                onChange={(selected) =>
                  setForm((f) => ({ ...f, agent_ids: fromOptions(selected) }))
                }
                components={{ DropdownIndicator: null }}
                noOptionsMessage={noOptionsMessage}
              />
            </FormControl>
            <Checkbox
              isChecked={form.enabled}
              onChange={(e) => setForm((f) => ({ ...f, enabled: e.target.checked }))}
            >
              <Text fontSize="sm">Enabled</Text>
            </Checkbox>
          </VStack>
        </ModalBody>
        <ModalFooter gap={2}>
          <Button variant="ghost" size="sm" onClick={handleClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            size="sm"
            isLoading={createMutation.isLoading}
            onClick={handleCreate}
          >
            Create Agent
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  );
};

// ─── Edit Modal ───────────────────────────────────────────────────────────────

type EditAgentModalProps = {
  agent: Agent;
  isOpen: boolean;
  onClose: () => void;
};

const EditAgentModal: React.FC<EditAgentModalProps> = ({ agent, isOpen, onClose }) => {
  const [name, setName] = useState(agent.name);
  const [description, setDescription] = useState(agent.description ?? '');
	const [charter, setCharter] = useState(agent.charter ?? '');
	const [enabled, setEnabled] = useState(agent.enabled);
	const [agentIDs, setAgentIDs] = useState<string[]>(agent.agent_ids);
	const [managedBindings, setManagedBindings] = useState<ClaudeManagedAgentBinding[]>(
		agent.claude_managed_agents ?? [],
	);
  const [managedAccount, setManagedAccount] = useState(
    agent.claude_managed_agents?.[0]?.account ?? 'default',
  );
	const [managedSearch, setManagedSearch] = useState('');
	const [forceManagedConnect, setForceManagedConnect] = useState(false);
	const [statusMsg, setStatusMsg] = useState<StatusMsg | null>(null);

	const updateMutation = useUpdateAgent();
	const deleteMutation = useDeleteAgent();
	const { data: agentsData } = useAgents();
	const previewDisclosure = useDisclosure();
	const charterPreviewQuery = useQuery(
		['agent-charter-preview', agent.cuid],
		() => agentsApi.getAgentCharterPreview(agent.cuid),
		{ enabled: previewDisclosure.isOpen, refetchOnWindowFocus: false, retry: false },
	);
	const accountsQuery = useQuery(
    ['claude-managed-agent-accounts'],
    () => agentsApi.managedAgentAccounts(),
    { enabled: isOpen, refetchOnWindowFocus: false, retry: false },
  );
  const accountItems = accountsQuery.data?.items ?? [];
  const selectedAccount = accountItems.some((account) => account.name === managedAccount)
    ? managedAccount
    : accountItems[0]?.name || managedAccount || 'default';
  const managedAgentsQuery = useQuery(
    ['claude-managed-agents', selectedAccount, managedSearch],
    () => agentsApi.managedAgents(selectedAccount, managedSearch),
    {
      enabled: isOpen && !accountsQuery.isError && !accountsQuery.isLoading,
      refetchOnWindowFocus: false,
      retry: false,
    },
  );
  const managedAgentsUnavailable = accountsQuery.isError && statusCode(accountsQuery.error) === 501;
  const managedAgentOptions = useMemo(() => {
    const byKey = new Map<string, ManagedAgentOption>();
    for (const binding of managedBindings) {
      byKey.set(bindingKey(binding), toManagedAgentOption(binding));
    }
    for (const managedAgent of managedAgentsQuery.data?.items ?? []) {
      const binding = toManagedAgentBinding(managedAgent, selectedAccount);
      byKey.set(bindingKey(binding), toManagedAgentOption(binding));
    }
    return Array.from(byKey.values());
	}, [managedAgentsQuery.data?.items, managedBindings, selectedAccount]);
	const selectedManagedAgentOptions = managedBindings.map(toManagedAgentOption);

	const handleUpdate = async () => {
		if (!name.trim()) {
			setStatusMsg({ text: 'Name is required.', isError: true });
			return;
		}
		const conflicts = agentIDs.flatMap((id) => {
			const owner = agentsData?.items.find((a) => a.cuid !== agent.cuid && a.agent_ids.includes(id));
			return owner ? [`${id} is already in use by "${owner.name}"`] : [];
    });
    if (conflicts.length > 0) {
      setStatusMsg({
        text: 'Agent ID(s) already in use by another agent:',
        lines: conflicts,
        isError: true,
			});
			return;
		}
		const input: AgentUpdateInput = {
			name,
			description,
      enabled,
      agent_ids: agentIDs,
      charter,
    };
    if (!managedAgentsUnavailable) {
			input.claude_managed_agents = managedBindings;
			input.force_claude_managed_agent_connect = forceManagedConnect;
		}
		try {
      await updateMutation.mutateAsync({ cuid: agent.cuid, input });
      setStatusMsg(null);
      onClose();
    } catch (err: unknown) {
      setStatusMsg({ text: apiErrorMessage(err, 'Update failed.'), isError: true });
    }
  };

  const handleDelete = async () => {
    if (!window.confirm(`Delete agent "${agent.name}"?`)) return;
    try {
      await deleteMutation.mutateAsync(agent.cuid);
      onClose();
    } catch (err: unknown) {
      setStatusMsg({ text: apiErrorMessage(err, 'Delete failed.'), isError: true });
    }
  };

  const noOptionsMessage = useCallback(() => null, []);

  const isBusy = updateMutation.isLoading || deleteMutation.isLoading;

  return (
    <>
    <Modal size="xl" isCentered isOpen={isOpen} onClose={onClose}>
      <ModalOverlay />
      <ModalContent>
        <ModalHeader>{agent.name}</ModalHeader>
        <ModalCloseButton />
        <ModalBody>
          <VStack align="stretch" gap={4}>
            {statusMsg && (
              <Alert status={statusMsg.isError ? 'error' : 'success'} borderRadius="md" py={2}>
                <AlertIcon />
                <AlertDescription fontSize="sm">
                  <Text>{statusMsg.text}</Text>
                  {statusMsg.lines && (
                    <Stack as="ul" mt={1} gap={0} pl={4} listStyleType="disc">
                      {statusMsg.lines.map((line) => (
                        <Text as="li" key={line} fontSize="sm">
                          {line}
                        </Text>
                      ))}
                    </Stack>
                  )}
                </AlertDescription>
              </Alert>
            )}

            <FormControl isRequired>
              <FormLabel fontSize="sm">Name</FormLabel>
              <Input
                size="sm"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </FormControl>

            <Divider />

            <FormControl>
              <FormLabel fontSize="sm">Description</FormLabel>
              <Textarea
                size="sm"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                rows={3}
              />
            </FormControl>

            <Divider />

            <FormControl>
              <Flex align="center" justify="space-between">
                <FormLabel fontSize="sm" mb={0}>
                  Charter
                  {agent.synced && (
                    <Text as="span" fontSize="xs" color="text.subtle" fontWeight="normal" ml={2}>
                      (read-only — managed by ValidMind sync)
                    </Text>
                  )}
                </FormLabel>
                <Button size="xs" variant="outline" onClick={previewDisclosure.onOpen}>
                  Preview charter
                </Button>
              </Flex>
              <Textarea
                size="sm"
                value={charter}
                onChange={(e) => setCharter(e.target.value)}
                isReadOnly={agent.synced}
                rows={6}
                placeholder={
                  agent.synced
                    ? 'Charter is managed by ValidMind sync'
                    : 'Define the rules and constraints governing this agent\'s behavior. Used by local LLM-as-judge evaluation rules.'
                }
                fontFamily="mono"
                fontSize="xs"
              />
            </FormControl>

            <Divider />

            <FormControl>
              <FormLabel fontSize="sm">Agent IDs</FormLabel>
              <Text fontSize="xs" color="text.subtle" mb={2}>
                JWT <code>sub</code> claims or client IDs that identify this agent.
                Type an ID and press Enter to add it.
              </Text>
              <CreatableSelect
                isMulti
                isClearable
                placeholder="Type an agent ID and press Enter…"
                value={toOptions(agentIDs)}
                onChange={(selected) => setAgentIDs(fromOptions(selected))}
                components={{ DropdownIndicator: null }}
                noOptionsMessage={noOptionsMessage}
              />
            </FormControl>

            {!managedAgentsUnavailable && <Divider />}

            {!managedAgentsUnavailable && (
            <FormControl>
              <FormLabel fontSize="sm">Claude Managed Agents</FormLabel>
              <Text fontSize="xs" color="text.subtle" mb={2}>
                Link Anthropic-hosted Claude agents to this Atryum agent. Session
                discovery will use these links.
              </Text>
              {accountsQuery.isError ? (
                <Alert status="info" borderRadius="md" py={2}>
                  <AlertIcon />
                  <AlertDescription fontSize="sm">
                    Claude Managed Agents bridge is not configured.
                  </AlertDescription>
                </Alert>
              ) : (
                <VStack align="stretch" gap={3}>
                  {accountItems.length > 1 && (
                    <FormControl>
                      <FormLabel fontSize="sm" color="text.subtle">Account</FormLabel>
                      <Select
                        size="sm"
                        value={{ value: selectedAccount, label: selectedAccount }}
                        options={accountItems.map((account) => ({
                          value: account.name,
                          label: account.workspace ? `${account.name} (${account.workspace})` : account.name,
                        }))}
                        onChange={(option) => {
                          if (option) setManagedAccount(option.value);
                        }}
                      />
                    </FormControl>
                  )}
                  {accountItems.length === 1 && (
                    <FormControl>
                      <FormLabel fontSize="sm" color="text.subtle">Account</FormLabel>
                      <Text fontSize="sm" color="text.subtle">
                        {accountItems[0].name}
                        {accountItems[0].workspace ? ` (${accountItems[0].workspace})` : ''}
                      </Text>
                    </FormControl>
                  )}
                  {managedAgentsQuery.isError && (
                    <Alert status="warning" borderRadius="md" py={2}>
                      <AlertIcon />
                      <AlertDescription fontSize="sm">
                        {apiErrorMessage(managedAgentsQuery.error, 'Failed to load Claude Managed Agents.')}
                      </AlertDescription>
                    </Alert>
                  )}
                  <FormControl>
                    <FormLabel fontSize="sm" color="text.subtle">Linked Agents</FormLabel>
                    <Select<ManagedAgentOption, true>
                      isMulti
                      isClearable
                      isLoading={managedAgentsQuery.isLoading || managedAgentsQuery.isFetching}
                      placeholder="Search Claude Managed Agents…"
                      value={selectedManagedAgentOptions}
                      options={managedAgentOptions}
                      onInputChange={(value) => setManagedSearch(value)}
                      onChange={(selected) => setManagedBindings(selected.map((option) => option.binding))}
                      noOptionsMessage={() => 'No Claude Managed Agents found'}
                    />
                  </FormControl>
                  {managedBindings.length > 0 && (
                    <VStack align="stretch" gap={2}>
                      {managedBindings.map((binding) => (
                        <Box
                          key={bindingKey(binding)}
                          borderWidth={1}
                          borderColor="border.base"
                          borderRadius="md"
                          px={3}
                          py={2}
                        >
                          <HStack justify="space-between" align="start" gap={3}>
                            <Box minW={0}>
                              <Text fontSize="sm" fontWeight="medium" noOfLines={1}>
                                {binding.claude_agent_name || binding.claude_agent_id}
                              </Text>
                              <Text fontSize="xs" color="text.subtle" fontFamily="mono" noOfLines={1}>
                                {binding.claude_agent_id}
                              </Text>
                              <Text fontSize="xs" color="text.subtle">
                                {binding.account}
                                {binding.claude_agent_model ? ` · ${binding.claude_agent_model}` : ''}
                                {binding.claude_agent_version ? ` · v${binding.claude_agent_version}` : ''}
                              </Text>
                            </Box>
                            <Button
                              size="xs"
                              variant="ghost"
                              onClick={() =>
                                setManagedBindings((items) =>
                                  items.filter((item) => bindingKey(item) !== bindingKey(binding)),
                                )
                              }
                            >
                              Remove
                            </Button>
                          </HStack>
                        </Box>
                      ))}
                    </VStack>
                  )}
                  <Checkbox
                    isChecked={forceManagedConnect}
                    onChange={(e) => setForceManagedConnect(e.target.checked)}
                  >
                    <Text fontSize="sm">Force connect if Claude metadata says another Atryum instance owns it</Text>
                  </Checkbox>
                </VStack>
              )}
            </FormControl>
            )}

            <Divider />

            <HStack gap={8} align="flex-start">
              <FormControl flex={1}>
                <FormLabel fontSize="sm" color="text.subtle">CUID</FormLabel>
                <Text fontSize="xs" fontFamily="mono" color="text.subtle">
                  {agent.cuid}
                </Text>
              </FormControl>
              <FormControl flex={1}>
                <FormLabel fontSize="sm" color="text.subtle">Created</FormLabel>
                <Text fontSize="sm" color="text.subtle">
                  {formatDate(agent.synced_at)}
                </Text>
              </FormControl>
            </HStack>

            <Divider />

            <Checkbox isChecked={enabled} onChange={(e) => setEnabled(e.target.checked)}>
              <Text fontSize="sm">Enabled</Text>
            </Checkbox>
          </VStack>
        </ModalBody>
        <ModalFooter gap={2}>
          <Button
            variant="outlineDanger"
            size="sm"
            isLoading={deleteMutation.isLoading}
            isDisabled={isBusy || agent.synced}
            title={agent.synced ? 'Managed by ValidMind sync — change the org or record type in Settings to remove' : undefined}
            onClick={handleDelete}
            mr="auto"
          >
            Delete
          </Button>
          <Button variant="ghost" size="sm" isDisabled={isBusy} onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            size="sm"
            isLoading={updateMutation.isLoading}
            isDisabled={isBusy}
            onClick={handleUpdate}
          >
            Save
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>

    <Modal size="xl" isCentered isOpen={previewDisclosure.isOpen} onClose={previewDisclosure.onClose}>
      <ModalOverlay />
      <ModalContent>
        <ModalHeader>Charter preview — {agent.name}</ModalHeader>
        <ModalCloseButton />
        <ModalBody>
          {charterPreviewQuery.isLoading ? (
            <Flex align="center" justify="center" py={8}>
              <Spinner size="md" />
            </Flex>
          ) : charterPreviewQuery.isError ? (
            <Alert status="error" borderRadius="md" py={2}>
              <AlertIcon />
              <AlertDescription fontSize="sm">
                {apiErrorMessage(charterPreviewQuery.error, 'Failed to load charter preview.')}
              </AlertDescription>
            </Alert>
          ) : !charterPreviewQuery.data || charterPreviewQuery.data.segments.length === 0 ? (
            <Text fontSize="sm" color="text.subtle" py={4}>
              No charter configured for this agent.
            </Text>
          ) : (
            <VStack align="stretch" gap={4}>
              {charterPreviewQuery.data.segments.map((segment, idx) => (
                <Box key={`${segment.header}-${idx}`}>
                  <HStack mb={1}>
                    <Badge colorScheme="purple" textTransform="none">
                      {segment.header || 'Charter'}
                    </Badge>
                  </HStack>
                  <Box
                    as="pre"
                    fontFamily="mono"
                    fontSize="xs"
                    whiteSpace="pre-wrap"
                    borderWidth="1px"
                    borderRadius="md"
                    p={3}
                    bg="bg.subtle"
                  >
                    {segment.text}
                  </Box>
                </Box>
              ))}
            </VStack>
          )}
        </ModalBody>
        <ModalFooter>
          <Button variant="ghost" size="sm" onClick={previewDisclosure.onClose}>
            Close
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
    </>
  );
};

// ─── Page ─────────────────────────────────────────────────────────────────────

const Agents: React.FC = () => {
  const { data, isLoading, isError, refetch } = useAgents();
  const { isConnected } = useSettings();
  const [isSyncing, setIsSyncing] = useState(false);
  const [syncError, setSyncError] = useState<string | null>(null);
  const {
    isOpen: isCreateOpen,
    onOpen: onCreateOpen,
    onClose: onCreateClose,
  } = useDisclosure();
  const [selectedAgent, setSelectedAgent] = useState<Agent | null>(null);
  const [editOpenCount, setEditOpenCount] = useState(0);
  const editDisclosure = useDisclosure();

  const handleSync = useCallback(async () => {
    setIsSyncing(true);
    setSyncError(null);
    try {
      await agentsApi.sync();
      await refetch();
    } catch (err: unknown) {
      setSyncError(err instanceof Error ? err.message : 'Sync failed.');
    } finally {
      setIsSyncing(false);
    }
  }, [refetch]);

  const agents = data?.items ?? [];

  const handleRowClick = useCallback(
    (agent: Agent) => {
      setSelectedAgent(agent);
      setEditOpenCount((c) => c + 1);
      editDisclosure.onOpen();
    },
    [editDisclosure],
  );

  return (
    <Box>
      <Stack mb={6}>
        <HStack>
          <Flex width="full" justify="space-between">
            <HStack gap={4} pl={2} color="text.heading">
              <Icon as={CpuChipIcon} boxSize={10} />
              <ContentPageTitle>Agents</ContentPageTitle>
            </HStack>
            <HStack gap={2}>
              {isConnected && (
                <Button
                  size="sm"
                  variant="outline"
                  isLoading={isSyncing}
                  onClick={handleSync}
                >
                  Sync from ValidMind
                </Button>
              )}
              <Button variant="primary" size="sm" onClick={onCreateOpen}>
                New Agent
              </Button>
            </HStack>
          </Flex>
        </HStack>
        <Text pl={2} color="text.subtle">
          Agents are identified by JWT sub claims or client IDs. Associate them
          here to target rules at specific agents.
        </Text>
      </Stack>

      {isError && (
        <Alert status="error" mb={4} borderRadius="md">
          <AlertIcon />
          <AlertDescription>Failed to load agents.</AlertDescription>
        </Alert>
      )}

      {syncError && (
        <Alert status="warning" mb={4} borderRadius="md">
          <AlertIcon />
          <AlertDescription fontSize="sm">{syncError}</AlertDescription>
        </Alert>
      )}

      <Box
        borderWidth={1}
        borderColor="border.base"
        borderRadius="md"
        overflow="hidden"
      >
        <Table variant="simple" size="sm">
          <Thead bg="background.table.header">
            <Tr>
              <Th>Name</Th>
              <Th>Description</Th>
              <Th># Agent IDs</Th>
              <Th>Enabled</Th>
              <Th>Created</Th>
            </Tr>
          </Thead>
          <Tbody>
            {isLoading ? (
              <Tr>
                <Td colSpan={5}>
                  <HStack justify="center" py={8}>
                    <Spinner size="sm" />
                    <Text color="text.subtle" fontSize="sm">
                      Loading agents…
                    </Text>
                  </HStack>
                </Td>
              </Tr>
            ) : agents.length === 0 ? (
              <Tr>
                <Td colSpan={5}>
                  <Text
                    textAlign="center"
                    py={8}
                    color="text.subtle"
                    fontSize="sm"
                  >
                    No agents yet. Click &ldquo;New Agent&rdquo; to create one.
                  </Text>
                </Td>
              </Tr>
            ) : (
              agents.map((agent) => (
                <Tr
                  key={agent.cuid}
                  cursor="pointer"
                  opacity={agent.enabled ? 1 : 0.5}
                  sx={{
                    '&:hover, &:nth-of-type(odd):hover, &:nth-of-type(even):hover': {
                      bg: 'background.table.row.hover',
                    },
                  }}
                  onClick={() => handleRowClick(agent)}
                >
                  <Td fontWeight="medium" fontSize="sm">
                    {agent.name}
                  </Td>
                  <Td maxW="300px">
                    <Text
                      fontSize="sm"
                      color="text.subtle"
                      noOfLines={2}
                      title={agent.description}
                    >
                      {agent.description || '—'}
                    </Text>
                  </Td>
                  <Td>
                    {(() => {
                      const claudeCount = agent.claude_managed_agents?.length ?? 0;
                      const totalIDs = agent.agent_ids.length + claudeCount;
                      return (
                        <Badge
                          colorScheme={totalIDs === 0 ? 'gray' : 'blue'}
                          fontSize="2xs"
                        >
                          {totalIDs} Agent ID
                          {totalIDs !== 1 ? 's' : ''}
                        </Badge>
                      );
                    })()}
                  </Td>
                  <Td>
                    <Badge
                      colorScheme={agent.enabled ? 'green' : 'gray'}
                      fontSize="2xs"
                    >
                      {agent.enabled ? 'yes' : 'no'}
                    </Badge>
                  </Td>
                  <Td whiteSpace="nowrap" fontSize="sm" color="text.subtle">
                    {formatDate(agent.synced_at)}
                  </Td>
                </Tr>
              ))
            )}
          </Tbody>
        </Table>
      </Box>

      <CreateAgentModal isOpen={isCreateOpen} onClose={onCreateClose} />

      {selectedAgent && (
        <EditAgentModal
          key={`${selectedAgent.cuid}-${editOpenCount}`}
          agent={selectedAgent}
          isOpen={editDisclosure.isOpen}
          onClose={editDisclosure.onClose}
        />
      )}
    </Box>
  );
};

export default Agents;
