import React, { useCallback, useEffect, useState } from 'react';
import {
  Alert,
  AlertDescription,
  AlertIcon,
  Button,
  Checkbox,
  FormControl,
  FormHelperText,
  FormLabel,
  Input,
  Modal,
  ModalBody,
  ModalCloseButton,
  ModalContent,
  ModalFooter,
  ModalHeader,
  ModalOverlay,
  Text,
  VStack,
  useToast,
} from '@chakra-ui/react';
import { CreatableSelect, Select } from 'chakra-react-select';
import { useQuery } from 'react-query';

import {
  useCreateRule,
  useUpdateRule,
  useRemoveRule,
  useServerTools,
} from '../hooks/useRules';
import { useServers } from '../hooks/useServers';
import { useAgents } from '../hooks/useAgents';
import { useSettings } from '../hooks/useSettings';
import { useLLMConfigs } from '../hooks/useLLMConfigs';
import {
  type LLMConfig,
  type Rule,
  type RuleAction,
  type RuleInput,
  apiErrorMessage,
  modelConfigsApi,
} from '../api/AtryumAPI';

// ─── Constants ────────────────────────────────────────────────────────────────

export const EMPTY_RULE_FORM: RuleInput = {
  action: 'human_approval',
  server_patterns: [],
  tool_patterns: [],
  agent_cuids: [],
  description: '',
  model_config_cuid: '',
  atryum_llm_config_id: '',
  enabled: true,
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

// ─── Helpers ──────────────────────────────────────────────────────────────────

type SelectOption = { value: string; label: string };

const toOptions = (names: string[]): SelectOption[] =>
  names.map((n) => ({ value: n, label: n }));
const fromOptions = (opts: readonly SelectOption[]): string[] =>
  opts.map((o) => o.value);
const formatCreateLabel = (input: string): string => `Add "${input}"`;

export const ruleToInput = (r: Rule): RuleInput => ({
  action: r.action,
  server_patterns: r.server_patterns,
  tool_patterns: r.tool_patterns,
  agent_cuids: r.agent_cuids ?? [],
  description: r.description ?? '',
  model_config_cuid: r.model_config_cuid ?? '',
  atryum_llm_config_id: r.atryum_llm_config_id ?? '',
  enabled: r.enabled,
});

// ─── Component ────────────────────────────────────────────────────────────────

export type CreateRuleModalProps = {
  isOpen: boolean;
  onClose: () => void;
  /** When provided, opens in edit mode for this rule. */
  rule?: Rule | null;
  /**
   * Initial form values for create mode, merged over EMPTY_RULE_FORM so you
   * only need to specify the fields you want pre-filled (e.g. from an
   * invocation's server/tool/agent).
   */
  initialValues?: Partial<RuleInput>;
};

export const CreateRuleModal: React.FC<CreateRuleModalProps> = ({
  isOpen,
  onClose,
  rule,
  initialValues,
}) => {
  const toast = useToast();
  const isEditing = !!rule;

  const buildInitialForm = useCallback((): RuleInput => {
    if (rule) return ruleToInput(rule);
    return { ...EMPTY_RULE_FORM, ...initialValues };
  }, [rule, initialValues]);

  const [form, setForm] = useState<RuleInput>(buildInitialForm);
  const [statusMsg, setStatusMsg] = useState<{
    text: string;
    isError: boolean;
  } | null>(null);

  useEffect(() => {
    if (isOpen) {
      setForm(buildInitialForm());
      setStatusMsg(null);
    }
  }, [isOpen, buildInitialForm]);

  // ── Queries ────────────────────────────────────────────────────────────────

  const { data: serversData } = useServers(true);
  const { data: toolsData, isFetching: toolsFetching } = useServerTools(
    form.server_patterns,
  );
  const { data: agentsData } = useAgents();
  const { isConnected } = useSettings();
  const { data: modelConfigsData } = useQuery(
    ['model-configs'],
    modelConfigsApi.list,
    { enabled: isConnected, staleTime: 60_000 },
  );
  const { data: llmConfigsData } = useLLMConfigs();

  const serverOptions = toOptions(
    (serversData?.items ?? []).map((s) => s.name),
  );
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

  const hasAIEval = isConnected || llmConfigOptions.length > 0;
  const actionOptions = hasAIEval
    ? [...BASE_ACTION_OPTIONS, AI_EVAL_OPTION]
    : BASE_ACTION_OPTIONS;

  const aiEvalGroups: { label: string; options: SelectOption[] }[] = [];
  if (isConnected && modelConfigOptions.length > 0) {
    aiEvalGroups.push({ label: 'ValidMind Models', options: modelConfigOptions });
  }
  if (llmConfigOptions.length > 0) {
    aiEvalGroups.push({ label: 'Local LLMs', options: llmConfigOptions });
  }

  const aiEvalValue: SelectOption | null = form.model_config_cuid
    ? (modelConfigOptions.find(
        (o) => o.value === `vm:${form.model_config_cuid}`,
      ) ?? null)
    : form.atryum_llm_config_id
      ? (llmConfigOptions.find(
          (o) => o.value === `local:${form.atryum_llm_config_id}`,
        ) ?? null)
      : null;

  // ── Mutations ──────────────────────────────────────────────────────────────

  const createRule = useCreateRule();
  const updateRule = useUpdateRule();
  const removeRule = useRemoveRule();

  const isBusy =
    createRule.isLoading || updateRule.isLoading || removeRule.isLoading;

  // ── Handlers ───────────────────────────────────────────────────────────────

  const handleServerChange = useCallback(
    (selected: readonly SelectOption[]) => {
      const newServers = fromOptions(selected);
      const validTools = new Set(toolOptions.map((t) => t.value));
      const filteredTools = form.tool_patterns.filter((t) =>
        validTools.has(t),
      );
      setForm((f) => ({
        ...f,
        server_patterns: newServers,
        tool_patterns: filteredTools,
      }));
    },
    [form.tool_patterns, toolOptions],
  );

  const handleSave = useCallback(async () => {
    try {
      if (isEditing && rule) {
        await updateRule.mutateAsync({ id: rule.id, input: form });
        toast({ title: 'Rule saved', status: 'success', duration: 3000, isClosable: true });
      } else {
        await createRule.mutateAsync(form);
        toast({ title: 'Rule created', status: 'success', duration: 3000, isClosable: true });
      }
      onClose();
    } catch (err: unknown) {
      setStatusMsg({ text: apiErrorMessage(err, 'Save failed.'), isError: true });
    }
  }, [createRule, form, isEditing, onClose, rule, toast, updateRule]);

  const handleDelete = useCallback(async () => {
    if (!rule) return;
    if (!window.confirm('Delete this rule?')) return;
    try {
      await removeRule.mutateAsync(rule.id);
      toast({ title: 'Rule deleted', status: 'success', duration: 3000, isClosable: true });
      onClose();
    } catch (err: unknown) {
      setStatusMsg({ text: apiErrorMessage(err, 'Delete failed.'), isError: true });
    }
  }, [onClose, removeRule, rule, toast]);

  // ── Render ─────────────────────────────────────────────────────────────────

  return (
    <Modal
      size="lg"
      isCentered
      closeOnEsc
      closeOnOverlayClick
      isOpen={isOpen}
      onClose={onClose}
    >
      <ModalOverlay />
      <ModalContent>
        <ModalHeader>{isEditing ? 'Edit Rule' : 'New Rule'}</ModalHeader>
        <ModalCloseButton />

        <ModalBody>
          <VStack align="stretch" gap={4}>
            {statusMsg && (
              <Alert
                status={statusMsg.isError ? 'error' : 'success'}
                borderRadius="md"
                py={2}
              >
                <AlertIcon />
                <AlertDescription fontSize="sm">{statusMsg.text}</AlertDescription>
              </Alert>
            )}

            {/* Action */}
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
                    model_config_cuid:
                      opt.value !== 'ai_evaluation' ? '' : f.model_config_cuid,
                    atryum_llm_config_id:
                      opt.value !== 'ai_evaluation' ? '' : f.atryum_llm_config_id,
                  }));
                }}
                classNamePrefix="chakra-react-select"
              />
            </FormControl>

            {/* Evaluation model */}
            {form.action === 'ai_evaluation' &&
              (aiEvalGroups.length > 0 ? (
                <FormControl isRequired>
                  <FormLabel fontSize="sm">Evaluation Model</FormLabel>
                  <Select
                    size="sm"
                    options={aiEvalGroups}
                    value={aiEvalValue}
                    onChange={(opt) => {
                      if (!opt) {
                        setForm((f) => ({
                          ...f,
                          model_config_cuid: '',
                          atryum_llm_config_id: '',
                        }));
                        return;
                      }
                      if (opt.value.startsWith('vm:')) {
                        setForm((f) => ({
                          ...f,
                          model_config_cuid: opt.value.slice(3),
                          atryum_llm_config_id: '',
                        }));
                      } else {
                        setForm((f) => ({
                          ...f,
                          atryum_llm_config_id: opt.value.slice(6),
                          model_config_cuid: '',
                        }));
                      }
                    }}
                    placeholder="Select a model…"
                    isClearable
                    classNamePrefix="chakra-react-select"
                  />
                  <FormHelperText fontSize="xs">
                    Choose a ValidMind model configuration or a locally-configured
                    LLM.
                  </FormHelperText>
                </FormControl>
              ) : (
                <Alert status="warning" borderRadius="md" py={2}>
                  <AlertIcon />
                  <AlertDescription fontSize="xs">
                    No evaluation models available. Connect to ValidMind or add a
                    local LLM in Settings.
                  </AlertDescription>
                </Alert>
              ))}

            {/* Agents */}
            <FormControl>
              <FormLabel fontSize="sm">Agents</FormLabel>
              <Select
                isMulti
                size="sm"
                placeholder="All agents"
                options={agentOptions}
                value={agentOptions.filter((o) =>
                  (form.agent_cuids ?? []).includes(o.value),
                )}
                onChange={(selected) =>
                  setForm((f) => ({
                    ...f,
                    agent_cuids: selected.map((o) => o.value),
                  }))
                }
                classNamePrefix="chakra-react-select"
              />
              <FormHelperText fontSize="xs">
                Restrict this rule to specific agents. Leave empty to apply to all.
              </FormHelperText>
            </FormControl>

            {/* Servers */}
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
                Pick an MCP server or type a coding harness source name. Leave
                empty to match all.
              </FormHelperText>
            </FormControl>

            {/* Tools */}
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
                Pick from discovered tools or type a tool name. Leave empty to
                match all.
              </FormHelperText>
            </FormControl>

            {/* Description */}
            <FormControl>
              <FormLabel fontSize="sm">Description (optional)</FormLabel>
              <Input
                size="sm"
                placeholder="e.g. Allow all read-only tools"
                value={form.description ?? ''}
                onChange={(e) =>
                  setForm((f) => ({ ...f, description: e.target.value }))
                }
              />
            </FormControl>

            {/* Enabled */}
            <Checkbox
              isChecked={form.enabled}
              onChange={(e) =>
                setForm((f) => ({ ...f, enabled: e.target.checked }))
              }
            >
              <Text fontSize="sm">Enabled</Text>
            </Checkbox>
          </VStack>
        </ModalBody>

        <ModalFooter gap={2}>
          {isEditing && (
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
          <Button variant="ghost" size="sm" isDisabled={isBusy} onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            size="sm"
            isLoading={createRule.isLoading || updateRule.isLoading}
            isDisabled={isBusy}
            onClick={handleSave}
          >
            {isEditing ? 'Save' : 'Create Rule'}
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  );
};
