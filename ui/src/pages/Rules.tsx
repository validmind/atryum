import React, { useCallback, useState } from 'react';
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
  FormHelperText,
  FormLabel,
  HStack,
  Icon,
  IconButton,
  Input,
  Modal,
  ModalBody,
  ModalCloseButton,
  ModalContent,
  ModalFooter,
  ModalHeader,
  ModalOverlay,
  Spinner,
  Table,
  Tbody,
  Td,
  Text,
  Th,
  Thead,
  Tr,
  VStack,
  useDisclosure,
} from '@chakra-ui/react';
import { CreatableSelect, Select } from 'chakra-react-select';
import { ShieldCheckIcon, ChevronUpIcon, ChevronDownIcon } from '@heroicons/react/24/outline';
import { useQuery } from 'react-query';

import { ContentPageTitle } from '../components/Layout';
import {
  useRules,
  useCreateRule,
  useUpdateRule,
  useRemoveRule,
  useMoveRule,
  useServerTools,
} from '../hooks/useRules';
import { useServers } from '../hooks/useServers';
import { useAgents } from '../hooks/useAgents';
import { useSettings } from '../hooks/useSettings';
import { useLLMConfigs } from '../hooks/useLLMConfigs';
import { type Agent, type LLMConfig, type Rule, type RuleAction, type RuleInput, apiErrorMessage, modelConfigsApi } from '../api/AtryumAPI';

const ACTION_COLOR: Record<RuleAction, string> = {
  auto_approve: 'green',
  auto_deny: 'red',
  human_approval: 'orange',
  ai_evaluation: 'purple',
};

const ACTION_LABEL: Record<RuleAction, string> = {
  auto_approve: 'Auto Approve',
  auto_deny: 'Auto Deny',
  human_approval: 'Human Approval',
  ai_evaluation: 'AI Evaluation',
};

const BASE_ACTION_OPTIONS: { value: RuleAction; label: string }[] = [
  { value: 'auto_approve', label: 'Auto Approve' },
  { value: 'auto_deny', label: 'Auto Deny' },
  { value: 'human_approval', label: 'Human Approval' },
];

const AI_EVAL_OPTION: { value: RuleAction; label: string } = {
  value: 'ai_evaluation',
  label: 'AI Evaluation',
};

const EMPTY_FORM: RuleInput = {
  action: 'human_approval',
  server_patterns: [],
  tool_patterns: [],
  agent_cuids: [],
  description: '',
  model_config_cuid: '',
  atryum_llm_config_id: '',
  enabled: true,
};

const ruleToInput = (r: Rule): RuleInput => ({
  action: r.action,
  server_patterns: r.server_patterns,
  tool_patterns: r.tool_patterns,
  agent_cuids: r.agent_cuids ?? [],
  description: r.description ?? '',
  model_config_cuid: r.model_config_cuid ?? '',
  atryum_llm_config_id: r.atryum_llm_config_id ?? '',
  enabled: r.enabled,
});

type SelectOption = { value: string; label: string };

const toOptions = (names: string[]): SelectOption[] =>
  names.map((n) => ({ value: n, label: n }));
const fromOptions = (opts: readonly SelectOption[]): string[] =>
  opts.map((o) => o.value);
const formatCreateLabel = (input: string): string => `Add "${input}"`;
const patternLabel = (patterns: string[]): string =>
  patterns.length === 0 ? 'all' : patterns.join(', ');

const stopPropagation = (e: React.MouseEvent): void => e.stopPropagation();

type RuleRowProps = {
  rule: Rule;
  agents: Agent[];
  index: number;
  totalCount: number;
  isBusy: boolean;
  onEdit: (rule: Rule) => void;
  onMove: (id: string, direction: 'up' | 'down') => void;
};

const RuleRow: React.FC<RuleRowProps> = ({
  rule,
  agents,
  index,
  totalCount,
  isBusy,
  onEdit,
  onMove,
}) => {
  const handleClick = useCallback(() => onEdit(rule), [onEdit, rule]);
  const handleMoveUp = useCallback(() => onMove(rule.id, 'up'), [onMove, rule.id]);
  const handleMoveDown = useCallback(() => onMove(rule.id, 'down'), [onMove, rule.id]);
  const selectedAgentCuids = rule.agent_cuids ?? [];
  const agentByCuid = new Map(agents.map((agent) => [agent.cuid, agent]));

  return (
    <Tr
      cursor="pointer"
      opacity={rule.enabled ? 1 : 0.5}
      sx={{
        '&:hover, &:nth-of-type(odd):hover, &:nth-of-type(even):hover': {
          bg: 'background.table.row.hover',
        },
      }}
      onClick={handleClick}
    >
      <Td onClick={stopPropagation}>
        <HStack gap={0}>
          <IconButton
            aria-label="Move up"
            icon={<Icon as={ChevronUpIcon} boxSize={3} />}
            size="xs"
            variant="ghost"
            isDisabled={index === 0 || isBusy}
            onClick={handleMoveUp}
          />
          <IconButton
            aria-label="Move down"
            icon={<Icon as={ChevronDownIcon} boxSize={3} />}
            size="xs"
            variant="ghost"
            isDisabled={index === totalCount - 1 || isBusy}
            onClick={handleMoveDown}
          />
        </HStack>
      </Td>
      <Td>
        <Badge colorScheme={ACTION_COLOR[rule.action]} fontSize="2xs" whiteSpace="nowrap">
          {ACTION_LABEL[rule.action]}
        </Badge>
      </Td>
      <Td><Text fontSize="sm">{rule.description || '—'}</Text></Td>
      <Td>
        {selectedAgentCuids.length === 0 ? (
          <Badge colorScheme="gray" fontSize="2xs" variant="subtle">
            All agents
          </Badge>
        ) : (
          <HStack gap={1} wrap="wrap">
            {selectedAgentCuids.map((cuid) => {
              const agent = agentByCuid.get(cuid);
              return (
                <Badge
                  key={cuid}
                  colorScheme={agent?.enabled === false ? 'gray' : 'blue'}
                  fontSize="2xs"
                  variant="subtle"
                  title={agent ? cuid : `Unknown agent: ${cuid}`}
                >
                  {agent?.name ?? cuid}
                </Badge>
              );
            })}
          </HStack>
        )}
      </Td>
      <Td>
        <Text
          fontSize="xs"
          fontFamily="mono"
          color={rule.server_patterns.length === 0 ? 'text.subtle' : undefined}
        >
          {patternLabel(rule.server_patterns)}
        </Text>
      </Td>
      <Td>
        <Text
          fontSize="xs"
          fontFamily="mono"
          color={rule.tool_patterns.length === 0 ? 'text.subtle' : undefined}
        >
          {patternLabel(rule.tool_patterns)}
        </Text>
      </Td>
      <Td>
        <Badge colorScheme={rule.enabled ? 'green' : 'gray'} fontSize="2xs">
          {rule.enabled ? 'yes' : 'no'}
        </Badge>
      </Td>
    </Tr>
  );
};

const Rules: React.FC = () => {
  const { isOpen, onOpen, onClose } = useDisclosure();
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [isCreating, setIsCreating] = useState(false);
  const [form, setForm] = useState<RuleInput>(EMPTY_FORM);
  const [statusMsg, setStatusMsg] = useState<{ text: string; isError: boolean } | null>(null);

  const { data: rulesData, isLoading, refetch } = useRules();
  const { data: serversData } = useServers(true);
  const { data: toolsData, isFetching: toolsFetching } = useServerTools(form.server_patterns);
  const { data: agentsData } = useAgents();
  const { isConnected } = useSettings();
  const { data: modelConfigsData } = useQuery(
    ['model-configs'],
    modelConfigsApi.list,
    { enabled: isConnected, staleTime: 60_000 },
  );
  const { data: llmConfigsData } = useLLMConfigs();

  const createRule = useCreateRule();
  const updateRule = useUpdateRule();
  const removeRule = useRemoveRule();
  const moveRule = useMoveRule();

  const rules = rulesData?.items ?? [];
  const serverOptions = toOptions((serversData?.items ?? []).map((s) => s.name));
  const toolOptions = toOptions((toolsData ?? []).map((t) => t.name));
  const agentOptions = (agentsData?.items ?? []).map((a) => ({
    value: a.cuid,
    label: a.name,
  }));
  const modelConfigOptions = (modelConfigsData?.items ?? []).map((mc) => ({
    value: `vm:${mc.cuid}`,
    label: mc.name,
  }));
  const llmConfigOptions = ((llmConfigsData?.items ?? []) as LLMConfig[])
    .filter((c) => c.enabled)
    .map((c) => ({ value: `local:${c.id}`, label: c.name }));

  // ai_evaluation is available when either ValidMind is connected OR local LLMs are configured
  const hasAIEval = isConnected || llmConfigOptions.length > 0;
  const actionOptions = hasAIEval
    ? [...BASE_ACTION_OPTIONS, AI_EVAL_OPTION]
    : BASE_ACTION_OPTIONS;

  // Build grouped options for the single AI evaluation model picker
  const aiEvalGroups: { label: string; options: SelectOption[] }[] = [];
  if (isConnected && modelConfigOptions.length > 0) {
    aiEvalGroups.push({ label: 'ValidMind Models', options: modelConfigOptions });
  }
  if (llmConfigOptions.length > 0) {
    aiEvalGroups.push({ label: 'Local LLMs', options: llmConfigOptions });
  }

  // Derive the currently selected grouped option value
  const aiEvalValue: SelectOption | null =
    form.model_config_cuid
      ? (modelConfigOptions.find((o) => o.value === `vm:${form.model_config_cuid}`) ?? null)
      : form.atryum_llm_config_id
        ? (llmConfigOptions.find((o) => o.value === `local:${form.atryum_llm_config_id}`) ?? null)
        : null;

  const openForCreate = useCallback(() => {
    setSelectedId(null);
    setIsCreating(true);
    setForm(EMPTY_FORM);
    setStatusMsg(null);
    onOpen();
  }, [onOpen]);

  const openForEdit = useCallback(
    (rule: Rule) => {
      setSelectedId(rule.id);
      setIsCreating(false);
      setForm(ruleToInput(rule));
      setStatusMsg(null);
      onOpen();
    },
    [onOpen],
  );

  const handleClose = useCallback(() => {
    setStatusMsg(null);
    onClose();
  }, [onClose]);

  const handleSave = useCallback(async () => {
    try {
      if (isCreating) {
        await createRule.mutateAsync(form);
        handleClose();
      } else {
        await updateRule.mutateAsync({ id: selectedId!, input: form });
        handleClose();
      }
    } catch (err: unknown) {
      setStatusMsg({ text: apiErrorMessage(err, 'Save failed.'), isError: true });
    }
  }, [createRule, form, handleClose, isCreating, selectedId, updateRule]);

  const handleDelete = useCallback(async () => {
    if (!selectedId) return;
    if (!window.confirm('Delete this rule?')) return;
    try {
      await removeRule.mutateAsync(selectedId);
      handleClose();
    } catch (err: unknown) {
      setStatusMsg({ text: apiErrorMessage(err, 'Delete failed.'), isError: true });
    }
  }, [handleClose, removeRule, selectedId]);

  const handleMove = useCallback(
    async (id: string, direction: 'up' | 'down') => {
      try {
        await moveRule.mutateAsync({ id, direction });
      } catch {
        // non-critical
      }
    },
    [moveRule],
  );

  const handleServerChange = useCallback(
    (selected: readonly SelectOption[]) => {
      const newServers = fromOptions(selected);
      const validTools = new Set(toolOptions.map((t) => t.value));
      const filteredTools = form.tool_patterns.filter((t) => validTools.has(t));
      setForm((f) => ({ ...f, server_patterns: newServers, tool_patterns: filteredTools }));
    },
    [form.tool_patterns, toolOptions],
  );

  const isBusy =
    createRule.isLoading ||
    updateRule.isLoading ||
    removeRule.isLoading ||
    moveRule.isLoading;

  return (
    <Box>
      <Box mb={6}>
        <HStack mb={2}>
          <Flex width="full" justify="space-between">
            <HStack gap={4} pl={2} color="text.heading">
              <Icon as={ShieldCheckIcon} boxSize={10} />
              <ContentPageTitle>Rules</ContentPageTitle>
            </HStack>
          </Flex>
        </HStack>
        <Text pl={2} color="text.subtle">
          Approval rules evaluated in order — first match wins
        </Text>
      </Box>

      <HStack mb={4} justify="space-between">
        <Button size="sm" variant="primary" onClick={openForCreate}>
          New Rule
        </Button>
      </HStack>

      <Box borderWidth={1} borderColor="border.base" borderRadius="md" overflow="hidden">
        {isLoading ? (
          <HStack justify="center" py={10}>
            <Spinner size="sm" color="brand.base" />
          </HStack>
        ) : rules.length === 0 ? (
          <Text p={6} color="text.subtle" fontSize="sm">
            No rules configured. First match wins — more specific rules should be ordered first.
          </Text>
        ) : (
          <Table size="sm" variant="simple">
            <Thead bg="background.table.header">
              <Tr>
                <Th w="60px">Order</Th>
                <Th>Action</Th>
                <Th>Description</Th>
                <Th>Agent</Th>
                <Th>Servers</Th>
                <Th>Tools</Th>
                <Th>Enabled</Th>
              </Tr>
            </Thead>
            <Tbody>
              {rules.map((rule, idx) => (
                <RuleRow
                  key={rule.id}
                  rule={rule}
                  agents={agentsData?.items ?? []}
                  index={idx}
                  totalCount={rules.length}
                  isBusy={isBusy}
                  onEdit={openForEdit}
                  onMove={handleMove}
                />
              ))}
            </Tbody>
          </Table>
        )}
      </Box>

      <Modal size="lg" isCentered closeOnEsc closeOnOverlayClick isOpen={isOpen} onClose={handleClose}>
        <ModalOverlay />
        <ModalContent>
          <ModalHeader>{isCreating ? 'New Rule' : 'Edit Rule'}</ModalHeader>
          <ModalCloseButton />

          <ModalBody>
            <VStack align="stretch" gap={4}>
              {statusMsg && (
                <Alert status={statusMsg.isError ? 'error' : 'success'} borderRadius="md" py={2}>
                  <AlertIcon />
                  <AlertDescription fontSize="sm">{statusMsg.text}</AlertDescription>
                </Alert>
              )}

              <FormControl isRequired>
                <FormLabel fontSize="sm">Action</FormLabel>
                <Select
                  size="sm"
                  options={actionOptions}
                  value={actionOptions.find((o) => o.value === form.action) ?? null}
                  onChange={(opt) => {
                    if (!opt) return;
                    setForm((f) => ({
                      ...f,
                      action: opt.value as RuleAction,
                      model_config_cuid: opt.value !== 'ai_evaluation' ? '' : f.model_config_cuid,
                      atryum_llm_config_id: opt.value !== 'ai_evaluation' ? '' : f.atryum_llm_config_id,
                    }));
                  }}
                  classNamePrefix="chakra-react-select"
                />
              </FormControl>

              {form.action === 'ai_evaluation' && (
                aiEvalGroups.length > 0 ? (
                  <FormControl isRequired>
                    <FormLabel fontSize="sm">Evaluation Model</FormLabel>
                    <Select
                      size="sm"
                      options={aiEvalGroups}
                      value={aiEvalValue}
                      onChange={(opt) => {
                        if (!opt) {
                          setForm((f) => ({ ...f, model_config_cuid: '', atryum_llm_config_id: '' }));
                          return;
                        }
                        if (opt.value.startsWith('vm:')) {
                          setForm((f) => ({ ...f, model_config_cuid: opt.value.slice(3), atryum_llm_config_id: '' }));
                        } else {
                          setForm((f) => ({ ...f, atryum_llm_config_id: opt.value.slice(6), model_config_cuid: '' }));
                        }
                      }}
                      placeholder="Select a model…"
                      isClearable
                      classNamePrefix="chakra-react-select"
                    />
                    <FormHelperText fontSize="xs">
                      Choose a ValidMind model configuration or a locally-configured LLM.
                    </FormHelperText>
                  </FormControl>
                ) : (
                  <Alert status="warning" borderRadius="md" py={2}>
                    <AlertIcon />
                    <AlertDescription fontSize="xs">
                      No evaluation models available. Connect to ValidMind or add a local LLM in Settings.
                    </AlertDescription>
                  </Alert>
                )
              )}

              <FormControl>
                <FormLabel fontSize="sm">Agents</FormLabel>
                <Select
                  isMulti
                  size="sm"
                  placeholder="All agents"
                  options={agentOptions}
                  value={agentOptions.filter((o) => (form.agent_cuids ?? []).includes(o.value))}
                  onChange={(selected) =>
                    setForm((f) => ({ ...f, agent_cuids: selected.map((o) => o.value) }))
                  }
                  classNamePrefix="chakra-react-select"
                />
                <FormHelperText fontSize="xs">
                  Restrict this rule to specific agents. Leave empty to apply to all.
                </FormHelperText>
              </FormControl>

              <FormControl>
                <FormLabel fontSize="sm">Servers / Sources</FormLabel>
                <CreatableSelect
                  isMulti
                  size="sm"
                  placeholder="All servers"
                  options={serverOptions}
                  value={toOptions(form.server_patterns)}
                  onChange={handleServerChange}
                  formatCreateLabel={formatCreateLabel}
                  classNamePrefix="chakra-react-select"
                />
                <FormHelperText fontSize="xs">
                  Pick an MCP server or type a coding harness source name. Leave empty to match all.
                </FormHelperText>
              </FormControl>

              <FormControl>
                <FormLabel fontSize="sm">Tools</FormLabel>
                <CreatableSelect
                  isMulti
                  size="sm"
                  placeholder={
                    form.server_patterns.length === 0
                      ? 'Select servers first or type a tool name'
                      : toolsFetching
                        ? 'Loading tools…'
                        : 'All tools'
                  }
                  isLoading={toolsFetching}
                  options={toolOptions}
                  value={toOptions(form.tool_patterns)}
                  onChange={(selected) =>
                    setForm((f) => ({ ...f, tool_patterns: fromOptions(selected) }))
                  }
                  formatCreateLabel={formatCreateLabel}
                  classNamePrefix="chakra-react-select"
                />
                <FormHelperText fontSize="xs">
                  Pick from discovered tools or type a tool name. Leave empty to match all.
                </FormHelperText>
              </FormControl>

              <FormControl>
                <FormLabel fontSize="sm">Description (optional)</FormLabel>
                <Input
                  size="sm"
                  placeholder="e.g. Allow all read-only tools"
                  value={form.description ?? ''}
                  onChange={(e) => setForm((f) => ({ ...f, description: e.target.value }))}
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
            {!isCreating && (
              <Button
                variant="outlineDanger"
                size="sm"
                isLoading={removeRule.isLoading}
                isDisabled={isBusy}
                onClick={handleDelete}
                mr="auto"
              >
                Delete
              </Button>
            )}
            <Button variant="ghost" size="sm" isDisabled={isBusy} onClick={handleClose}>
              Cancel
            </Button>
            <Button
              variant="primary"
              size="sm"
              isLoading={createRule.isLoading || updateRule.isLoading}
              isDisabled={isBusy}
              onClick={handleSave}
            >
              {isCreating ? 'Create Rule' : 'Save'}
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>
    </Box>
  );
};

export default Rules;
