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
  FormLabel,
  HStack,
  Heading,
  Icon,
  Select,
  Spinner,
  Stack,
  Text,
  useToast,
} from '@chakra-ui/react';
import { Cog6ToothIcon } from '@heroicons/react/24/outline';

import { useSettings, useUpdateSettings } from '../hooks/useSettings';
import {
  modelConfigsApi,
  vmDiscoveryApi,
  type ModelConfig,
  type VmCustomField,
  type VmOrg,
  type VmRecordType,
} from '../api/AtryumAPI';

const Settings: React.FC = () => {
  const toast = useToast();
  const { data: savedSettings, isLoading: loadingSettings, isError: settingsError } = useSettings();
  const updateMutation = useUpdateSettings();

  // ── Form state ────────────────────────────────────────────────────────────────
  const [orgCUID, setOrgCUID] = useState('');
  const [recordTypeSlug, setRecordTypeSlug] = useState('');
  const [constitutionFieldKey, setConstitutionFieldKey] = useState('');
  const [summaryModelConfigCUID, setSummaryModelConfigCUID] = useState('');

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
  const hasChanges =
    orgCUID !== (savedSettings?.org_cuid ?? '') ||
    recordTypeSlug !== (savedSettings?.agent_record_type_slug ?? '') ||
    constitutionFieldKey !== (savedSettings?.constitution_field_key ?? '') ||
    summaryModelConfigCUID !== (savedSettings?.summary_model_config_cuid ?? '');

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

  const commitSave = useCallback(async () => {
    setShowConfirm(false);
    setVmError(null);
    try {
      const saved = await updateMutation.mutateAsync({
        org_cuid: orgCUID,
        agent_record_type_slug: recordTypeSlug,
        constitution_field_key: constitutionFieldKey,
        summary_model_config_cuid: summaryModelConfigCUID,
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
    summaryModelConfigCUID,
    updateMutation,
    toast,
  ]);

  const handleSaveClick = useCallback(() => {
    if (willDeleteSyncedAgents) {
      setShowConfirm(true);
    } else {
      void commitSave();
    }
  }, [willDeleteSyncedAgents, commitSave]);

  const isConnected = Boolean(savedSettings?.org_cuid && savedSettings?.agent_record_type_slug);

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
    <Stack gap={8} maxW="3xl">
      <Stack gap={1}>
        <HStack gap={3} color="text.heading">
          <Icon as={Cog6ToothIcon} boxSize={8} />
          <Heading as="h1" size="lg">Settings</Heading>
        </HStack>
        <Text color="text.subtle">
          Optionally connect Atryum to a ValidMind workspace to sync agents and enable AI evaluations.
        </Text>
      </Stack>

      {/* Connection status */}
      <HStack>
        <Badge colorScheme={isConnected ? 'green' : 'gray'} px={2} py={1} borderRadius="md">
          {isConnected ? 'Connected to ValidMind' : 'Not connected'}
        </Badge>
      </HStack>

      {vmError && (
        <Alert status="warning" borderRadius="md">
          <AlertIcon />
          <AlertDescription fontSize="sm">{vmError}</AlertDescription>
        </Alert>
      )}

      {/* Agent Sync section */}
      <Box
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

          <Divider />

          {/* Invocation Summary Model */}
          <FormControl>
            <FormLabel fontWeight="semibold" fontSize="sm">Invocation Summary Model (optional)</FormLabel>
            <Text fontSize="xs" color="text.subtle" mb={2}>
              The ValidMind model configuration used to generate summaries on the Invocations page.
            </Text>
            {loadingModelConfigs ? (
              <Spinner size="sm" />
            ) : (
              <Select
                value={summaryModelConfigCUID}
                onChange={(e) => setSummaryModelConfigCUID(e.target.value)}
                placeholder={
                  modelConfigs.length > 0
                    ? 'Select a model configuration…'
                    : 'No model configurations available'
                }
                isDisabled={modelConfigs.length === 0}
                size="sm"
              >
                {modelConfigs.map((mc) => (
                  <option key={mc.cuid} value={mc.cuid}>
                    {mc.name}
                  </option>
                ))}
              </Select>
            )}
          </FormControl>

          <Flex justify="flex-end">
            <Button
              variant="primary"
              size="sm"
              onClick={handleSaveClick}
              isLoading={updateMutation.isLoading}
              isDisabled={!hasChanges || updateMutation.isLoading}
            >
              Save Settings
            </Button>
          </Flex>
        </Stack>
      </Box>

      {/* Destructive confirmation */}
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
              onClick={() => void commitSave()}
              isLoading={updateMutation.isLoading}
            >
              Delete synced agents &amp; save
            </Button>
          </HStack>
        </Alert>
      )}

      {savedSettings?.updated_at && (
        <Text fontSize="xs" color="text.subtle">
          Last saved:{' '}
          {new Date(savedSettings.updated_at).toLocaleString(undefined, {
            dateStyle: 'medium',
            timeStyle: 'short',
          })}
        </Text>
      )}
    </Stack>
  );
};

export default Settings;
