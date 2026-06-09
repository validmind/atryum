import React, { useCallback, useEffect, useState } from 'react';
import {
  Alert,
  AlertDescription,
  AlertIcon,
  Badge,
  Box,
  Button,
  Divider,
  Flex,
  FormControl,
  FormHelperText,
  FormLabel,
  HStack,
  Heading,
  Icon,
  Input,
  Modal,
  ModalBody,
  ModalCloseButton,
  ModalContent,
  ModalFooter,
  ModalHeader,
  ModalOverlay,
  Select,
  SimpleGrid,
  Spinner,
  Stack,
  Switch,
  Table,
  TableContainer,
  Tbody,
  Td,
  Text,
  Th,
  Thead,
  Tr,
  useDisclosure,
  useToast,
} from '@chakra-ui/react';
import { Select as GroupedSelect } from 'chakra-react-select';
import { Cog6ToothIcon, PlusIcon, TrashIcon } from '@heroicons/react/24/outline';

import { useSettings, useUpdateSettings } from '../hooks/useSettings';
import { useCreateLLMConfig, useDeleteLLMConfig, useLLMConfigs, useUpdateLLMConfig } from '../hooks/useLLMConfigs';
import {
  modelConfigsApi,
  vmDiscoveryApi,
  type LLMConfig,
  type LLMConfigInput,
  type LLMProvider,
  type ModelConfig,
  type VmCustomField,
  type VmOrg,
  type VmRecordType,
} from '../api/AtryumAPI';

const PROVIDER_LABELS: Record<LLMProvider, string> = {
  openai: 'OpenAI',
  anthropic: 'Anthropic',
  openai_compatible: 'OpenAI-compatible',
};

interface LLMConfigFormProps {
  initial?: LLMConfig;
  onSave: (input: LLMConfigInput) => Promise<void>;
  onClose: () => void;
  isLoading: boolean;
}

const LLMConfigForm: React.FC<LLMConfigFormProps> = ({ initial, onSave, onClose, isLoading }) => {
  const [name, setName] = useState(initial?.name ?? '');
  const [provider, setProvider] = useState<LLMProvider>(initial?.provider ?? 'openai');
  const [model, setModel] = useState(initial?.model ?? '');
  const [apiKey, setApiKey] = useState('');
  const [baseURL, setBaseURL] = useState(initial?.base_url ?? '');
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);

  const handleSubmit = async () => {
    const input: LLMConfigInput = { name, provider, model, enabled };
    if (apiKey) input.api_key = apiKey;
    if (baseURL) input.base_url = baseURL;
    await onSave(input);
  };

  return (
    <Stack gap={4}>
      <FormControl isRequired>
        <FormLabel fontSize="sm">Name</FormLabel>
        <Input size="sm" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. Local Llama 3" />
      </FormControl>

      <FormControl isRequired>
        <FormLabel fontSize="sm">Provider</FormLabel>
        <Select size="sm" value={provider} onChange={(e) => setProvider(e.target.value as LLMProvider)}>
          <option value="openai">OpenAI</option>
          <option value="anthropic">Anthropic</option>
          <option value="openai_compatible">OpenAI-compatible (Ollama, LM Studio, Azure…)</option>
        </Select>
      </FormControl>

      <FormControl isRequired>
        <FormLabel fontSize="sm">Model</FormLabel>
        <Input size="sm" value={model} onChange={(e) => setModel(e.target.value)} placeholder="e.g. gpt-4o, claude-3-5-sonnet-latest" />
      </FormControl>

      <FormControl>
        <FormLabel fontSize="sm">API Key</FormLabel>
        <Input
          size="sm"
          type="password"
          value={apiKey}
          onChange={(e) => setApiKey(e.target.value)}
          placeholder={initial?.api_key === '***' ? '(leave blank to keep existing)' : 'sk-…'}
        />
        <FormHelperText fontSize="xs">Stored encrypted. Leave blank to keep the existing key when editing.</FormHelperText>
      </FormControl>

      {provider === 'openai_compatible' && (
        <FormControl isRequired>
          <FormLabel fontSize="sm">Base URL</FormLabel>
          <Input size="sm" value={baseURL} onChange={(e) => setBaseURL(e.target.value)} placeholder="http://localhost:11434" />
          <FormHelperText fontSize="xs">Base URL of the OpenAI-compatible endpoint (e.g. Ollama: http://localhost:11434).</FormHelperText>
        </FormControl>
      )}

      <FormControl>
        <HStack>
          <Switch isChecked={enabled} onChange={(e) => setEnabled(e.target.checked)} size="sm" />
          <FormLabel mb={0} fontSize="sm">Enabled</FormLabel>
        </HStack>
      </FormControl>

      <HStack justify="flex-end" pt={2}>
        <Button size="sm" variant="ghost" onClick={onClose} isDisabled={isLoading}>Cancel</Button>
        <Button size="sm" variant="primary" onClick={() => void handleSubmit()} isLoading={isLoading} isDisabled={!name || !model}>
          {initial ? 'Save Changes' : 'Add LLM'}
        </Button>
      </HStack>
    </Stack>
  );
};

const Settings: React.FC = () => {
  const toast = useToast();
  const { data: savedSettings, isLoading: loadingSettings, isError: settingsError } = useSettings();
  const updateMutation = useUpdateSettings();

  // ── Local LLM hooks ───────────────────────────────────────────────────────────
  const { data: llmConfigsData } = useLLMConfigs();
  const createLLMConfig = useCreateLLMConfig();
  const updateLLMConfig = useUpdateLLMConfig();
  const deleteLLMConfig = useDeleteLLMConfig();
  const llmConfigs: LLMConfig[] = llmConfigsData?.items ?? [];

  const { isOpen: isLLMModalOpen, onOpen: openLLMModal, onClose: closeLLMModal } = useDisclosure();
  const [editingLLM, setEditingLLM] = useState<LLMConfig | undefined>(undefined);

  // ── Form state ────────────────────────────────────────────────────────────────
  const [orgCUID, setOrgCUID] = useState('');
  const [recordTypeSlug, setRecordTypeSlug] = useState('');
  const [constitutionFieldKey, setConstitutionFieldKey] = useState('');
  const [summaryModelConfigCUID, setSummaryModelConfigCUID] = useState('');
  const [summaryAtryumLLMConfigID, setSummaryAtryumLLMConfigID] = useState('');

  // ── Discovery data ────────────────────────────────────────────────────────────
  const [orgs, setOrgs] = useState<VmOrg[]>([]);
  const [loadingOrgs, setLoadingOrgs] = useState(false);
  const [orgSelectionLocked, setOrgSelectionLocked] = useState(false);

  const [recordTypes, setRecordTypes] = useState<VmRecordType[]>([]);
  const [loadingRecordTypes, setLoadingRecordTypes] = useState(false);

  const [customFields, setCustomFields] = useState<VmCustomField[]>([]);
  const [loadingCustomFields, setLoadingCustomFields] = useState(false);

  const [modelConfigs, setModelConfigs] = useState<ModelConfig[]>([]);
  const [loadingModelConfigs, setLoadingModelConfigs] = useState(false);

  const [vmError, setVmError] = useState<string | null>(null);

  // ── Confirm destructive change ────────────────────────────────────────────────
  const [showConfirm, setShowConfirm] = useState(false);

  // ── Populate form when saved settings load ────────────────────────────────────
  useEffect(() => {
    if (!savedSettings) return;
    setOrgCUID(savedSettings.org_cuid ?? '');
    setRecordTypeSlug(savedSettings.agent_record_type_slug ?? '');
    setConstitutionFieldKey(savedSettings.constitution_field_key ?? '');
    setSummaryModelConfigCUID(savedSettings.summary_model_config_cuid ?? '');
    setSummaryAtryumLLMConfigID(savedSettings.summary_atryum_llm_config_id ?? '');
  }, [savedSettings]);

  // ── Load orgs + model configs on mount ────────────────────────────────────────
  useEffect(() => {
    let cancelled = false;

    const loadInitial = async () => {
      setLoadingOrgs(true);
      setLoadingModelConfigs(true);
      try {
        const [orgsResp, modelConfigsResp] = await Promise.all([
          vmDiscoveryApi.listOrganizations().catch(() => ({
            items: [] as VmOrg[],
            total: 0,
            single_org: false,
            auth_mode: '',
          })),
          modelConfigsApi.list().catch(() => ({ items: [] as ModelConfig[], total: 0 })),
        ]);
        if (cancelled) return;
        setOrgs(orgsResp.items);
        setOrgSelectionLocked(
          Boolean(orgsResp.single_org || orgsResp.auth_mode === 'api_key'),
        );
        setModelConfigs(modelConfigsResp.items);

        // If single-org, auto-select it
        if (orgsResp.single_org && orgsResp.items.length === 1 && !orgCUID) {
          setOrgCUID(orgsResp.items[0].cuid);
        }
      } catch {
        // VM discovery is optional — silence errors
      } finally {
        if (!cancelled) {
          setLoadingOrgs(false);
          setLoadingModelConfigs(false);
        }
      }
    };

    void loadInitial();
    return () => { cancelled = true; };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // ── Load record types when orgCUID is set ────────────────────────────────────
  useEffect(() => {
    if (!orgCUID) {
      setRecordTypes([]);
      return;
    }
    let cancelled = false;
    setLoadingRecordTypes(true);
    vmDiscoveryApi
      .listRecordTypes(orgCUID)
      .then((resp) => { if (!cancelled) setRecordTypes(resp.items); })
      .catch(() => {})
      .finally(() => { if (!cancelled) setLoadingRecordTypes(false); });
    return () => { cancelled = true; };
  }, [orgCUID]);

  // ── Load custom fields when org + record type are set ─────────────────────────
  useEffect(() => {
    if (!orgCUID || !recordTypeSlug) {
      setCustomFields([]);
      return;
    }
    let cancelled = false;
    setLoadingCustomFields(true);
    vmDiscoveryApi
      .listCustomFields(orgCUID, recordTypeSlug)
      .then((resp) => { if (!cancelled) setCustomFields(resp.items); })
      .catch(() => {})
      .finally(() => { if (!cancelled) setLoadingCustomFields(false); });
    return () => { cancelled = true; };
  }, [orgCUID, recordTypeSlug]);

  // ── Derived state ─────────────────────────────────────────────────────────────
  const hasSyncChanges =
    orgCUID !== (savedSettings?.org_cuid ?? '') ||
    recordTypeSlug !== (savedSettings?.agent_record_type_slug ?? '') ||
    constitutionFieldKey !== (savedSettings?.constitution_field_key ?? '');

  const hasSummaryChanges =
    summaryModelConfigCUID !== (savedSettings?.summary_model_config_cuid ?? '') ||
    summaryAtryumLLMConfigID !== (savedSettings?.summary_atryum_llm_config_id ?? '');

  const willDeleteSyncedAgents =
    (savedSettings?.org_cuid ?? '') !== '' &&
    (orgCUID !== (savedSettings?.org_cuid ?? '') ||
      recordTypeSlug !== (savedSettings?.agent_record_type_slug ?? ''));

  // ── Handlers ──────────────────────────────────────────────────────────────────
  const handleOrgChange = useCallback((e: React.ChangeEvent<HTMLSelectElement>) => {
    setOrgCUID(e.target.value);
    setRecordTypeSlug('');
    setConstitutionFieldKey('');
  }, []);

  const handleRecordTypeChange = useCallback(
    (e: React.ChangeEvent<HTMLSelectElement>) => {
      setRecordTypeSlug(e.target.value);
      setConstitutionFieldKey('');
    },
    [],
  );

  // Saves Agent Record Sync fields only; preserves saved summary model values.
  const commitSyncSave = useCallback(async () => {
    setShowConfirm(false);
    setVmError(null);
    try {
      const saved = await updateMutation.mutateAsync({
        org_cuid: orgCUID,
        agent_record_type_slug: recordTypeSlug,
        constitution_field_key: constitutionFieldKey,
        summary_model_config_cuid: savedSettings?.summary_model_config_cuid ?? '',
        summary_atryum_llm_config_id: savedSettings?.summary_atryum_llm_config_id ?? '',
      });
      if (saved.sync_error) {
        toast({
          title: 'Settings saved — sync failed',
          description: `Agent sync error: ${saved.sync_error}`,
          status: 'warning',
          duration: 8000,
          isClosable: true,
        });
      } else {
        toast({
          title: orgCUID ? 'Settings saved & agents synced' : 'Settings saved',
          status: 'success',
          duration: 3000,
          isClosable: true,
        });
      }
    } catch (err: unknown) {
      toast({
        title: 'Failed to save settings',
        description: err instanceof Error ? err.message : 'Unexpected error',
        status: 'error',
        duration: 5000,
        isClosable: true,
      });
    }
  }, [
    orgCUID,
    recordTypeSlug,
    constitutionFieldKey,
    savedSettings,
    updateMutation,
    toast,
  ]);

  // Saves Invocation Summary Model fields only; preserves saved sync values.
  const commitSummarySave = useCallback(async () => {
    try {
      await updateMutation.mutateAsync({
        org_cuid: savedSettings?.org_cuid ?? '',
        agent_record_type_slug: savedSettings?.agent_record_type_slug ?? '',
        constitution_field_key: savedSettings?.constitution_field_key ?? '',
        summary_model_config_cuid: summaryModelConfigCUID,
        summary_atryum_llm_config_id: summaryAtryumLLMConfigID,
      });
      toast({
        title: 'Summary model saved',
        status: 'success',
        duration: 3000,
        isClosable: true,
      });
    } catch (err: unknown) {
      toast({
        title: 'Failed to save summary model',
        description: err instanceof Error ? err.message : 'Unexpected error',
        status: 'error',
        duration: 5000,
        isClosable: true,
      });
    }
  }, [
    summaryModelConfigCUID,
    summaryAtryumLLMConfigID,
    savedSettings,
    updateMutation,
    toast,
  ]);

  const handleSaveClick = useCallback(() => {
    if (willDeleteSyncedAgents) {
      setShowConfirm(true);
    } else {
      void commitSyncSave();
    }
  }, [willDeleteSyncedAgents, commitSyncSave]);

  const isConnected = Boolean(savedSettings?.org_cuid && savedSettings?.agent_record_type_slug);
  const isBackendConfigured = savedSettings?.backend_configured ?? false;

  const handleOpenAddLLM = useCallback(() => {
    setEditingLLM(undefined);
    openLLMModal();
  }, [openLLMModal]);

  const handleOpenEditLLM = useCallback((cfg: LLMConfig) => {
    setEditingLLM(cfg);
    openLLMModal();
  }, [openLLMModal]);

  const handleSaveLLM = useCallback(async (input: LLMConfigInput) => {
    try {
      if (editingLLM) {
        await updateLLMConfig.mutateAsync({ id: editingLLM.id, input });
        toast({ title: 'LLM configuration updated', status: 'success', duration: 3000, isClosable: true });
      } else {
        await createLLMConfig.mutateAsync(input);
        toast({ title: 'LLM configuration added', status: 'success', duration: 3000, isClosable: true });
      }
      closeLLMModal();
    } catch (err: unknown) {
      toast({
        title: 'Failed to save LLM configuration',
        description: err instanceof Error ? err.message : 'Unexpected error',
        status: 'error',
        duration: 5000,
        isClosable: true,
      });
    }
  }, [editingLLM, createLLMConfig, updateLLMConfig, toast, closeLLMModal]);

  const handleDeleteLLM = useCallback(async (id: string) => {
    try {
      await deleteLLMConfig.mutateAsync(id);
      toast({ title: 'LLM configuration deleted', status: 'success', duration: 3000, isClosable: true });
    } catch {
      toast({ title: 'Failed to delete LLM configuration', status: 'error', duration: 4000, isClosable: true });
    }
  }, [deleteLLMConfig, toast]);

  if (loadingSettings) {
    return (
      <Stack align="center" pt={16} gap={4}>
        <Spinner size="lg" color="brand.base" />
        <Text color="text.subtle">Loading settings…</Text>
      </Stack>
    );
  }

  if (settingsError) {
    return (
      <Alert status="error" rounded="md">
        <AlertIcon />
        <AlertDescription>Failed to load settings.</AlertDescription>
      </Alert>
    );
  }

  return (
    <Stack gap={8} maxW="6xl">
      <Stack gap={1}>
        <HStack gap={3} color="text.heading">
          <Icon as={Cog6ToothIcon} boxSize={8} />
          <Heading as="h1" size="lg">Settings</Heading>
        </HStack>
        <Text color="text.subtle">
          Optionally connect Atryum to a ValidMind workspace to sync agents and enable AI evaluations.
        </Text>
      </Stack>

      {/* Connection status — only shown when a backend URL is configured */}
      {isBackendConfigured && (
        <HStack>
          <Badge colorScheme={isConnected ? 'green' : 'gray'} px={2} py={1} borderRadius="md">
            {isConnected ? 'Connected to ValidMind' : 'Not connected'}
          </Badge>
        </HStack>
      )}

      {vmError && (
        <Alert status="warning" borderRadius="md">
          <AlertIcon />
          <AlertDescription fontSize="sm">{vmError}</AlertDescription>
        </Alert>
      )}

      {/* Agent Sync + Local LLM side by side */}
      <SimpleGrid columns={2} gap={6} alignItems="start">

      {/* Agent Sync section — only shown when a backend URL is configured */}
      {isBackendConfigured && <Box
        borderWidth={1}
        borderColor="border.base"
        borderRadius="md"
        p={6}
        bg="background.container.subtle"
      >
        <Stack gap={6}>
          <Stack gap={1}>
            <Heading as="h2" size="sm" color="text.heading">
              Agent Record Sync
            </Heading>
            <Text fontSize="sm" color="text.subtle">
              Select the ValidMind organization, record type, and constitution field
              used when syncing agents. Leave blank to disable sync.
            </Text>
          </Stack>

          {/* Organization */}
          <FormControl>
            <FormLabel fontWeight="semibold" fontSize="sm">Organization</FormLabel>
            <Text fontSize="xs" color="text.subtle" mb={2}>
              The ValidMind organization whose agent records will be synced.
            </Text>
            {loadingOrgs ? (
              <Spinner size="sm" />
            ) : orgSelectionLocked && orgCUID ? (
              <Box
                borderWidth={1}
                borderColor="border.base"
                borderRadius="md"
                px={3}
                py={2}
              >
                <Text fontSize="sm">
                  {orgs.find((o) => o.cuid === orgCUID)?.name ?? 'Organization from API credentials'}
                </Text>
              </Box>
            ) : (
              <Select
                value={orgCUID}
                onChange={handleOrgChange}
                placeholder="Select an organization…"
                size="sm"
              >
                {orgs.map((o) => (
                  <option key={o.cuid} value={o.cuid}>
                    {o.name}
                  </option>
                ))}
              </Select>
            )}
          </FormControl>

          {/* Record Type */}
          <FormControl>
            <FormLabel fontWeight="semibold" fontSize="sm">Record Type</FormLabel>
            <Text fontSize="xs" color="text.subtle" mb={2}>
              The primary record type slug that identifies agent inventory models (e.g. <code>ai-agents</code>).
            </Text>
            {loadingRecordTypes ? (
              <Spinner size="sm" />
            ) : (
              <Select
                value={recordTypeSlug}
                onChange={handleRecordTypeChange}
                placeholder={orgCUID ? 'Select a record type…' : 'Select an organization first'}
                isDisabled={!orgCUID}
                size="sm"
              >
                {recordTypes.map((rt) => (
                  <option key={rt.cuid} value={rt.slug}>
                    {rt.name} ({rt.slug})
                  </option>
                ))}
              </Select>
            )}
          </FormControl>

          {/* Constitution Field */}
          <FormControl>
            <FormLabel fontWeight="semibold" fontSize="sm">Constitution Field (optional)</FormLabel>
            <Text fontSize="xs" color="text.subtle" mb={2}>
              Custom field key storing the agent's governing constitution text.
            </Text>
            {loadingCustomFields ? (
              <Spinner size="sm" />
            ) : (
              <Select
                value={constitutionFieldKey}
                onChange={(e) => setConstitutionFieldKey(e.target.value)}
                placeholder={recordTypeSlug ? 'Select a field…' : 'Select a record type first'}
                isDisabled={!recordTypeSlug}
                size="sm"
              >
                {customFields.map((cf) => (
                  <option key={cf.key} value={cf.key}>
                    {cf.name} ({cf.key})
                  </option>
                ))}
              </Select>
            )}
          </FormControl>

          {showConfirm && (
            <Alert status="warning" borderRadius="md" flexDirection="column" alignItems="flex-start" gap={3}>
              <HStack>
                <AlertIcon />
                <AlertDescription fontWeight="semibold">
                  Changing the org or record type will delete all synced agents. Manually-created agents are kept.
                </AlertDescription>
              </HStack>
              <HStack pl={6} gap={2}>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => setShowConfirm(false)}
                >
                  Cancel
                </Button>
                <Button
                  size="sm"
                  colorScheme="red"
                  onClick={() => void commitSyncSave()}
                  isLoading={updateMutation.isLoading}
                >
                  Delete synced agents &amp; save
                </Button>
              </HStack>
            </Alert>
          )}

          <Flex justify="flex-end">
            <Button
              variant="primary"
              size="sm"
              onClick={handleSaveClick}
              isLoading={updateMutation.isLoading}
              isDisabled={!hasSyncChanges || updateMutation.isLoading}
            >
              Save Settings
            </Button>
          </Flex>
        </Stack>
      </Box>}


      {/* ── Local LLM Configurations ────────────────────────────────────────── */}
      <Box
        borderWidth={1}
        borderColor="border.base"
        borderRadius="md"
        p={6}
        bg="background.container.subtle"
      >
        <Stack gap={4}>
          <HStack justify="space-between" align="flex-start">
            <Stack gap={1}>
              <Heading as="h2" size="sm" color="text.heading">
                Local LLM Configurations
              </Heading>
              <Text fontSize="sm" color="text.subtle">
                Configure local LLM providers (OpenAI, Anthropic, Ollama, etc.) for native AI evaluation rules without requiring a ValidMind connection.
              </Text>
            </Stack>
            <Button
              leftIcon={<Icon as={PlusIcon} />}
              size="sm"
              variant="secondary"
              onClick={handleOpenAddLLM}
              flexShrink={0}
            >
              Add LLM
            </Button>
          </HStack>

          {llmConfigs.length === 0 ? (
            <Box
              borderWidth={1}
              borderColor="border.base"
              borderRadius="md"
              p={8}
              textAlign="center"
              bg="background.base"
            >
              <Text color="text.subtle" fontSize="sm">
                No local LLMs configured. Add one to enable AI evaluation rules without ValidMind.
              </Text>
            </Box>
          ) : (
            <TableContainer>
              <Table size="sm" variant="simple">
                <Thead>
                  <Tr>
                    <Th>Name</Th>
                    <Th>Provider</Th>
                    <Th>Model</Th>
                    <Th>Status</Th>
                    <Th />
                  </Tr>
                </Thead>
                <Tbody>
                  {llmConfigs.map((cfg) => (
                    <Tr
                      key={cfg.id}
                      cursor="pointer"
                      _hover={{ bg: 'background.container.subtle' }}
                      onClick={() => handleOpenEditLLM(cfg)}
                    >
                      <Td fontWeight="medium">{cfg.name}</Td>
                      <Td>
                        <Badge variant="subtle" colorScheme="blue" fontSize="xs">
                          {PROVIDER_LABELS[cfg.provider] ?? cfg.provider}
                        </Badge>
                      </Td>
                      <Td>
                        <Text fontSize="xs" fontFamily="mono">{cfg.model}</Text>
                      </Td>
                      <Td>
                        <Badge colorScheme={cfg.enabled ? 'green' : 'gray'} fontSize="xs">
                          {cfg.enabled ? 'Enabled' : 'Disabled'}
                        </Badge>
                      </Td>
                      <Td onClick={(e) => e.stopPropagation()}>
                        <Button
                          size="xs"
                          variant="ghost"
                          colorScheme="red"
                          leftIcon={<Icon as={TrashIcon} />}
                          onClick={() => void handleDeleteLLM(cfg.id)}
                          isLoading={deleteLLMConfig.isLoading}
                        >
                          Delete
                        </Button>
                      </Td>
                    </Tr>
                  ))}
                </Tbody>
              </Table>
            </TableContainer>
          )}
        </Stack>
      </Box>

      {/* ── Invocation Summary Model ────────────────────────────────────────────── */}
      {(() => {
        const vmOpts = modelConfigs.map((mc) => ({ value: `vm:${mc.cuid}`, label: mc.name }));
        const localOpts = llmConfigs.map((c) => ({ value: `local:${c.id}`, label: c.name }));
        const summaryGroups: { label: string; options: { value: string; label: string }[] }[] = [];
        if (vmOpts.length > 0) summaryGroups.push({ label: 'ValidMind Models', options: vmOpts });
        if (localOpts.length > 0) summaryGroups.push({ label: 'Local LLMs', options: localOpts });
        const summaryValue =
          summaryModelConfigCUID
            ? (vmOpts.find((o) => o.value === `vm:${summaryModelConfigCUID}`) ?? null)
            : summaryAtryumLLMConfigID
              ? (localOpts.find((o) => o.value === `local:${summaryAtryumLLMConfigID}`) ?? null)
              : null;
        return (
          <Box
            borderWidth={1}
            borderColor="border.base"
            borderRadius="md"
            p={6}
            bg="background.container.subtle"
            sx={isBackendConfigured ? { gridColumn: '1 / -1' } : undefined}
          >
            <Stack gap={4}>
              <Stack gap={1}>
                <Heading as="h2" size="sm" color="text.heading">
                  Invocation Summary Model
                </Heading>
                <Text fontSize="sm" color="text.subtle">
                  LLM used to generate plain-language summaries on the Invocations page. Choose a ValidMind model configuration or a local LLM.
                </Text>
              </Stack>

              {loadingModelConfigs ? (
                <Spinner size="sm" />
              ) : summaryGroups.length === 0 ? (
                <Text fontSize="sm" color="text.subtle">
                  No models available. Connect to ValidMind or add a local LLM above.
                </Text>
              ) : (
                <FormControl>
                  <GroupedSelect
                    size="sm"
                    options={summaryGroups}
                    value={summaryValue}
                    onChange={(opt) => {
                      if (!opt) { setSummaryModelConfigCUID(''); setSummaryAtryumLLMConfigID(''); return; }
                      if (opt.value.startsWith('vm:')) { setSummaryModelConfigCUID(opt.value.slice(3)); setSummaryAtryumLLMConfigID(''); }
                      else { setSummaryAtryumLLMConfigID(opt.value.slice(6)); setSummaryModelConfigCUID(''); }
                    }}
                    placeholder="Select a model…"
                    isClearable
                    classNamePrefix="chakra-react-select"
                  />
                </FormControl>
              )}

              <Flex justify="flex-end">
                <Button
                  variant="primary"
                  size="sm"
                  onClick={() => void commitSummarySave()}
                  isLoading={updateMutation.isLoading}
                  isDisabled={!hasSummaryChanges || updateMutation.isLoading}
                >
                  Save
                </Button>
              </Flex>
            </Stack>
          </Box>
        );
      })()}

      </SimpleGrid>

      {/* ── LLM Config Modal ───────────────────────────────────────────────────── */}
      <Modal isOpen={isLLMModalOpen} onClose={closeLLMModal} size="md">
        <ModalOverlay />
        <ModalContent>
          <ModalHeader>{editingLLM ? 'Edit LLM Configuration' : 'Add LLM Configuration'}</ModalHeader>
          <ModalCloseButton />
          <ModalBody>
            <LLMConfigForm
              initial={editingLLM}
              onSave={handleSaveLLM}
              onClose={closeLLMModal}
              isLoading={createLLMConfig.isLoading || updateLLMConfig.isLoading}
            />
          </ModalBody>
          <ModalFooter />
        </ModalContent>
      </Modal>
    </Stack>
  );
};

export default Settings;
