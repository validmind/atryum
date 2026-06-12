import React, { useCallback, useState } from 'react';
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
import { CreatableSelect } from 'chakra-react-select';
import { CpuChipIcon } from '@heroicons/react/24/outline';

import { ContentPageTitle } from '../components/Layout';
import { useAgents, useCreateAgent, useUpdateAgent, useDeleteAgent } from '../hooks/useAgents';
import { useSettings } from '../hooks/useSettings';
import type { Agent, AgentCreateInput, AgentUpdateInput } from '../api/AtryumAPI';
import { agentsApi } from '../api/AtryumAPI';

type SelectOption = { value: string; label: string };
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

const errorMessage = (err: unknown, fallback: string): string => {
  // Prefer the API response body: { error: { message: "..." } }
  if (typeof err === 'object' && err !== null) {
    const apiMsg = (err as { response?: { data?: { error?: { message?: unknown } } } }).response
      ?.data?.error?.message;
    if (typeof apiMsg === 'string' && apiMsg) return apiMsg;
  }
  if (err instanceof Error) return err.message;
  if (typeof err === 'object' && err !== null && 'message' in err) {
    const msg = (err as { message: unknown }).message;
    if (typeof msg === 'string') return msg;
  }
  return fallback;
};

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
      setStatusMsg({ text: errorMessage(err, 'Create failed.'), isError: true });
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
  const [statusMsg, setStatusMsg] = useState<StatusMsg | null>(null);

  const updateMutation = useUpdateAgent();
  const deleteMutation = useDeleteAgent();
  const { data: agentsData } = useAgents();

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
    const input: AgentUpdateInput = { name, description, enabled, agent_ids: agentIDs, charter };
    try {
      await updateMutation.mutateAsync({ cuid: agent.cuid, input });
      setStatusMsg(null);
      onClose();
    } catch (err: unknown) {
      setStatusMsg({ text: errorMessage(err, 'Update failed.'), isError: true });
    }
  };

  const handleDelete = async () => {
    if (!window.confirm(`Delete agent "${agent.name}"?`)) return;
    try {
      await deleteMutation.mutateAsync(agent.cuid);
      onClose();
    } catch (err: unknown) {
      setStatusMsg({ text: errorMessage(err, 'Delete failed.'), isError: true });
    }
  };

  const noOptionsMessage = useCallback(() => null, []);

  const isBusy = updateMutation.isLoading || deleteMutation.isLoading;

  return (
    <Modal size="lg" isCentered isOpen={isOpen} onClose={onClose}>
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
              <FormLabel fontSize="sm">
                Charter
                {agent.synced && (
                  <Text as="span" fontSize="xs" color="text.subtle" fontWeight="normal" ml={2}>
                    (read-only — managed by ValidMind sync)
                  </Text>
                )}
              </FormLabel>
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
                    <Badge
                      colorScheme={agent.agent_ids.length === 0 ? 'gray' : 'blue'}
                      fontSize="2xs"
                    >
                      {agent.agent_ids.length} Agent ID
                      {agent.agent_ids.length !== 1 ? 's' : ''}
                    </Badge>
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
