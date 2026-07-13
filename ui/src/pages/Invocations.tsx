/* eslint-disable react/jsx-no-bind */
import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import ResizablePanels from "../components/ResizablePanels";
import {
  Badge,
  Box,
  Button,
  ButtonGroup,
  CloseButton,
  Code,
  Collapse,
  Divider,
  Flex,
  FormControl,
  FormLabel,
  Heading,
  HStack,
  Icon,
  IconButton,
  Input,
  Menu,
  MenuButton,
  MenuItem,
  MenuList,
  Spinner,
  Stack,
  Table,
  Tag,
  Tbody,
  Td,
  Text,
  Textarea,
  Th,
  Thead,
  Tooltip,
  Tr,
  VStack,
  Alert,
  AlertDescription,
  AlertIcon,
  useDisclosure,
  useToast,
} from "@chakra-ui/react";
import { Select } from "chakra-react-select";
import {
  QueueListIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  ShieldCheckIcon,
} from "@heroicons/react/24/outline";

import { ContentPageTitle } from "../components/Layout";
import { CreateRuleModal } from "../components/CreateRuleModal";
import DiffViewer from "../components/DiffViewer";
import { extractDiff } from "../components/diffUtils";
import AgentIcon, { detectAgentKind } from "../components/AgentIcon";
import {
  useInvocations,
  useInvocationDetail,
  useInvocationEvents,
  useApproveInvocation,
  useDenyInvocation,
  useSummarizeInvocation,
} from "../hooks/useInvocations";
import { useSettings } from "../hooks/useSettings";
import { useRules } from "../hooks/useRules";
import { useAgents } from "../hooks/useAgents";
import { useInvocationStream } from "../hooks/useInvocationStream";
import {
  invocationsApi,
  type InvocationEvent,
  type InvocationStatus,
  type RuleInput,
} from "../api/AtryumAPI";
import {
  STATUS_COLOR,
  STATUS_LABEL,
  getDisposition,
  formatDate,
  isAIEvaluated,
  getConfidenceColor,
  formatConfidence,
  buildInvocationAudit,
  type AuditEntry,
  type AuditStep,
} from "../utils/invocationDisplay";

const PAGE_SIZE = 50;

const STATUSES: InvocationStatus[] = [
  "pending_approval",
  "approved",
  "denied",
  "executing",
  "succeeded",
  "failed",
  "expired",
  "cancelled",
];

const STATUS_OPTIONS = STATUSES.map((status) => ({
  label: STATUS_LABEL[status],
  value: status,
}));


const AUDIT_STEP_ICON: Record<AuditStep["variant"], string> = {
  approve: "✓",
  deny: "✗",
  defer: "→",
  pending: "…",
  info: "→",
  error: "✗",
};

const ACTION_LABEL: Partial<Record<string, string>> = {
  auto_approve: "Auto-approve",
  auto_deny: "Auto-deny",
  human_approval: "Human approval",
  ai_evaluation: "AI evaluation",
};

const buildRuleDescription = (
  action: string | undefined,
  serverName: string | null | undefined,
  toolName: string | null | undefined,
): string => {
  const parts: string[] = [];
  if (action && ACTION_LABEL[action]) parts.push(ACTION_LABEL[action]!);
  const target = [serverName, toolName].filter(Boolean).join("/");
  if (target) parts.push(target);
  return parts.join(" ");
};

const AuditEntryRow: React.FC<{ entry: AuditEntry }> = ({ entry }) => {
  const [isOpen, setIsOpen] = useState(false);

  const badge = entry.isAIEvaluation ? (
    <Badge colorScheme="purple" fontSize="xs">
      AI Evaluation
    </Badge>
  ) : entry.ruleName !== null ? (
    <Badge colorScheme="purple" fontSize="xs">
      Rule
    </Badge>
  ) : null;

  return (
    <Box borderWidth={1} borderColor="border.base" borderRadius="md" overflow="hidden">
      <Flex
        justify="space-between"
        align="center"
        px={3}
        py={2}
        cursor="pointer"
        _hover={{ bg: "background.container.subtle" }}
        onClick={() => setIsOpen((v) => !v)}>
        <HStack gap={2} flex={1} minW={0}>
          <Icon
            as={isOpen ? ChevronUpIcon : ChevronDownIcon}
            boxSize={4}
            color="text.subtle"
            flexShrink={0}
          />
          <Text fontSize="sm" fontWeight="medium" noOfLines={1}>
            {entry.ruleName ?? "*No rule matched*"}
          </Text>
          {badge}
        </HStack>
      </Flex>
      <Collapse in={isOpen} animateOpacity>
        <VStack align="stretch" gap={0} px={3} pb={2}>
          {entry.steps.map((step, i) => (
            <Box
              key={i}
              pt={2}
              borderTopWidth={i > 0 ? 1 : 0}
              borderColor="border.base">
              <Text fontSize="xs" fontWeight="medium">
                {AUDIT_STEP_ICON[step.variant]} {step.text}
              </Text>
              {step.timestamp && (
                <Text fontSize="xs" color="text.subtle" mt={0.5}>
                  {formatDate(step.timestamp)}
                </Text>
              )}
            </Box>
          ))}
        </VStack>
      </Collapse>
    </Box>
  );
};

const InvocationAuditSection: React.FC<{ entries: AuditEntry[] }> = ({
  entries,
}) => {
  if (entries.length === 0) return null;
  return (
    <Box>
      <Text
        fontSize="xs"
        fontWeight="semibold"
        textTransform="uppercase"
        color="text.subtle"
        letterSpacing="wide"
        mb={2}>
        Matched {entries.length === 1 ? "Rule" : "Rules"}
      </Text>
      <VStack align="stretch" gap={2}>
        {entries.map((entry, i) => (
          <AuditEntryRow key={entry.ruleId ?? 'unmatched'} entry={entry} />
        ))}
      </VStack>
    </Box>
  );
};

const EventRow: React.FC<{ event: InvocationEvent }> = ({ event }) => {
  const [showDetails, setShowDetails] = useState(false);
  const hasData = event.data != null;
  return (
    <>
      <Tr
        onClick={hasData ? () => setShowDetails((v) => !v) : undefined}
        cursor={hasData ? "pointer" : "default"}
        _hover={hasData ? { bg: "background.container.subtle" } : undefined}>
        <Td textAlign="center" w="1">
          {hasData ? (
            <IconButton
              variant="ghost"
              size="xs"
              aria-label={showDetails ? "Hide details" : "See details"}
              icon={
                <Icon
                  as={showDetails ? ChevronUpIcon : ChevronDownIcon}
                  boxSize={4}
                />
              }
              onClick={(e) => {
                e.stopPropagation();
                setShowDetails((v) => !v);
              }}
            />
          ) : (
            <Text fontSize="xs" color="text.subtle">
              —
            </Text>
          )}
        </Td>
        <Td>
          <Text fontSize="xs" textTransform="capitalize" fontFamily="mono">
            {event.type.replace(/^invocation\./i, "")}
          </Text>
        </Td>
        <Td fontSize="xs" color="text.subtle" whiteSpace="nowrap">
          {formatDate(event.timestamp)}
        </Td>
      </Tr>
      {hasData && (
        <Tr>
          <Td colSpan={3} p={0} borderBottomWidth={showDetails ? undefined : 0}>
            <Collapse in={showDetails} animateOpacity>
              <Code
                display="block"
                fontSize="2xs"
                whiteSpace="pre-wrap"
                p={3}
                m={2}
                borderRadius="md"
                bg="background.container.subtle">
                {JSON.stringify(event.data, null, 2)}
              </Code>
            </Collapse>
          </Td>
        </Tr>
      )}
    </>
  );
};

const Invocations: React.FC = () => {
  const toast = useToast();
  const [draftFilters, setDraftFilters] = useState({
    server: "",
    tool: "",
    status: "",
    client_name: "",
  });
  const [appliedFilters, setAppliedFilters] = useState({
    server: "",
    tool: "",
    status: "",
    client_name: "",
  });
  const [selectedId, setSelectedId] = useState<string | null>(null);

  // Select an invocation when deep-linked via URL hash (e.g. from a
  // desktop approval notification: /ui/invocations#<invocation_id>).
  useEffect(() => {
    const selectFromHash = () => {
      const hashId = window.location.hash.replace(/^#/, "");
      if (!hashId) return;
      setDetailClosed(false);
      setSelectedId(hashId);
    };
    selectFromHash();
    window.addEventListener("hashchange", selectFromHash);
    return () => window.removeEventListener("hashchange", selectFromHash);
  }, []);
  const [denyMessage, setDenyMessage] = useState("");
  const [showDenyInput, setShowDenyInput] = useState(false);
  const [detailClosed, setDetailClosed] = useState(false);
  const [showFilters, setShowFilters] = useState(false);
  const [showArgsJson, setShowArgsJson] = useState(false);
  const [page, setPage] = useState(1);
  const [clearedInvocationIds, setClearedInvocationIds] = useState<Set<string>>(
    () => new Set(),
  );
  const [denyMode, setDenyMode] = useState<"once" | "always">("once");
  const [ruleModalInitial, setRuleModalInitial] = useState<
    Partial<RuleInput> | undefined
  >();
  // Stored as a ref so the callback is never stale without causing re-renders.
  const ruleModalOnSuccessRef = useRef<
    (() => void | Promise<void>) | undefined
  >(undefined);
  const {
    isOpen: isRuleModalOpen,
    onOpen: openRuleModal,
    onClose: closeRuleModal,
  } = useDisclosure();

  const filters = useMemo(
    () =>
      Object.fromEntries(
        Object.entries(appliedFilters).filter(([, v]) => v !== ""),
      ),
    [appliedFilters],
  );

  const { data, isLoading, isError } = useInvocations({
    ...filters,
    offset: (page - 1) * PAGE_SIZE,
    limit: PAGE_SIZE,
  });
  const { data: agentsData } = useAgents();

  const rawItems = useMemo(() => data?.items ?? [], [data?.items]);
  const total = data?.total ?? 0;
  const totalPages = Math.ceil(total / PAGE_SIZE);
  const items = useMemo(
    () =>
      rawItems.filter((item) => !clearedInvocationIds.has(item.invocation_id)),
    [clearedInvocationIds, rawItems],
  );
  const hasClearedLoadedInvocations = rawItems.length > 0 && items.length === 0;

  const agentByAgentID = useMemo(() => {
    const map = new Map<string, { cuid: string; name: string }>();
    for (const agent of agentsData?.items ?? []) {
      for (const id of agent.agent_ids) {
        if (map.has(id)) {
          console.warn(
            `[Atryum] Duplicate agent_id "${id}" found on agent "${agent.name}" — already claimed by "${map.get(id)!.name}". This is a misconfiguration; only one agent should own each agent_id.`,
          );
        }
        map.set(id, { cuid: agent.cuid, name: agent.name });
      }
      for (const binding of agent.claude_managed_agents ?? []) {
        if (!map.has(binding.claude_agent_id)) {
          map.set(binding.claude_agent_id, { cuid: agent.cuid, name: agent.name });
        }
      }
    }
    return map;
  }, [agentsData?.items]);

  const resolvedSelectedId = useMemo(() => {
    if (detailClosed) return null;
    if (items.length === 0) return null;
    if (!selectedId) return items[0].invocation_id;
    const stillSelected = items.some(
      (item) => item.invocation_id === selectedId,
    );
    return stillSelected ? selectedId : items[0].invocation_id;
  }, [detailClosed, items, selectedId]);

  const { data: detail, isLoading: detailLoading } =
    useInvocationDetail(resolvedSelectedId);
  const { data: eventsData, isLoading: eventsLoading } =
    useInvocationEvents(resolvedSelectedId);

  useInvocationStream(filters, resolvedSelectedId, true);

  const approve = useApproveInvocation();
  const deny = useDenyInvocation();
  const summarize = useSummarizeInvocation();
  const { data: rulesData } = useRules();
  const { data: settings } = useSettings();

  const auditEntries = useMemo(
    () =>
      detail
        ? buildInvocationAudit(
            detail,
            rulesData?.items ?? [],
            eventsData?.items ?? [],
          )
        : [],
    [detail, rulesData?.items, eventsData?.items],
  );
  const summaryModelConfigCuid = settings?.summary_model_config_cuid ?? "";
  const hasSummaryModel = Boolean(
    summaryModelConfigCuid || (settings?.summary_atryum_llm_config_id ?? ""),
  );

  const [summarizingInvocationId, setSummarizingInvocationId] = useState<
    string | null
  >(null);
  const [summaryErrorInvocationId, setSummaryErrorInvocationId] = useState<
    string | null
  >(null);

  const isCurrentInvocationSummarizing =
    summarize.isLoading && summarizingInvocationId === resolvedSelectedId;
  const didCurrentInvocationSummaryFail =
    summaryErrorInvocationId === resolvedSelectedId;

  const handleSummarize = useCallback(async () => {
    if (!resolvedSelectedId) return;
    setSummarizingInvocationId(resolvedSelectedId);
    setSummaryErrorInvocationId(null);
    try {
      await summarize.mutateAsync({
        id: resolvedSelectedId,
        modelConfigCuid: summaryModelConfigCuid || undefined,
      });
    } catch {
      setSummaryErrorInvocationId(resolvedSelectedId);
    }
  }, [resolvedSelectedId, summarize, summaryModelConfigCuid]);


  const handleApply = () => {
    setAppliedFilters({ ...draftFilters });
    setPage(1);
  };

  const handleCloseDetail = () => {
    setDetailClosed(true);
    setSelectedId(null);
  };

  const handleClearInvocations = () => {
    if (items.length === 0) return;
    setClearedInvocationIds((ids) => {
      const nextIds = new Set(ids);
      for (const item of items) {
        nextIds.add(item.invocation_id);
      }
      return nextIds;
    });
    setSelectedId(null);
    setShowDenyInput(false);
    setShowArgsJson(false);
    setDenyMessage("");
  };

  const handleApproveOnce = async () => {
    if (!resolvedSelectedId) return;
    await approve.mutateAsync({ id: resolvedSelectedId });
    setShowDenyInput(false);
  };

  const handleAlwaysApprove = () => {
    const agentEntry = detail?.agent_id
      ? agentByAgentID.get(detail.agent_id)
      : undefined;
    setRuleModalInitial({
      action: "auto_approve",
      server_patterns: detail?.server_name ? [detail.server_name] : [],
      tool_patterns: detail?.tool_name ? [detail.tool_name] : [],
      agent_cuids: agentEntry?.cuid ? [agentEntry.cuid] : [],
      description: buildRuleDescription(
        "auto_approve",
        detail?.server_name,
        detail?.tool_name,
      ),
      insert_before: "",
    });
    ruleModalOnSuccessRef.current = resolvedSelectedId
      ? () => approve.mutateAsync({ id: resolvedSelectedId })
      : undefined;
    openRuleModal();
  };

  const handleDenyOnce = async () => {
    if (!resolvedSelectedId) return;
    await deny.mutateAsync({ id: resolvedSelectedId, message: denyMessage });
    setDenyMessage("");
    setShowDenyInput(false);
  };

  const handleAlwaysDeny = () => {
    const agentEntry = detail?.agent_id
      ? agentByAgentID.get(detail.agent_id)
      : undefined;
    setRuleModalInitial({
      action: "auto_deny",
      server_patterns: detail?.server_name ? [detail.server_name] : [],
      tool_patterns: detail?.tool_name ? [detail.tool_name] : [],
      agent_cuids: agentEntry?.cuid ? [agentEntry.cuid] : [],
      description: buildRuleDescription(
        "auto_deny",
        detail?.server_name,
        detail?.tool_name,
      ),
      insert_before: "",
    });
    ruleModalOnSuccessRef.current = resolvedSelectedId
      ? () => deny.mutateAsync({ id: resolvedSelectedId, message: denyMessage })
      : undefined;
    openRuleModal();
  };

  const isPending = detail?.status === "pending_approval";

  return (
    <Box h="full" display="flex" flexDirection="column">
      <Stack mb={4} gap={2}>
        <HStack>
          <Flex width="full" justify="space-between">
            <HStack gap={4} pl={2} color="text.heading">
              <Icon as={QueueListIcon} boxSize={10} />
              <ContentPageTitle>Invocations</ContentPageTitle>
            </HStack>
          </Flex>
        </HStack>
        <Text pl={2} color="text.subtle">
          AI agent tool call invocations awaiting or past approval
        </Text>
      </Stack>

      <Box
        flex={1}
        borderWidth={1}
        borderColor="border.base"
        borderRadius="md"
        overflow="hidden"
        minH={0}>
        <ResizablePanels
          initialSplit={0.7}
          minLeft={260}
          minRight={320}
          left={
            <Box
              display="flex"
              flexDirection="column"
              overflow="hidden"
              h="full">
              <Box p={3} borderBottomWidth={1} borderColor="border.base">
                <Flex justify="flex-end" gap={2}>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={handleClearInvocations}
                    isDisabled={items.length === 0}
                    data-testid="invocations-clear-button">
                    Clear
                  </Button>
                  {showFilters ? (
                    <CloseButton
                      size="sm"
                      onClick={() => setShowFilters(false)}
                      aria-label="Close filters"
                      title="Close filters"
                      data-testid="invocations-filter-toggle"
                    />
                  ) : (
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => setShowFilters(true)}
                      data-testid="invocations-filter-toggle">
                      Filter
                    </Button>
                  )}
                </Flex>
                <Collapse in={showFilters} animateOpacity>
                  <VStack gap={2} align="stretch" mt={3}>
                    <HStack gap={2} align="end">
                      <FormControl size="sm">
                        <FormLabel fontSize="xs" mb={1}>
                          Server
                        </FormLabel>
                        <Input
                          size="sm"
                          placeholder="Any"
                          value={draftFilters.server}
                          onChange={(e) =>
                            setDraftFilters((f) => ({
                              ...f,
                              server: e.target.value,
                            }))
                          }
                        />
                      </FormControl>
                      <FormControl size="sm">
                        <FormLabel fontSize="xs" mb={1}>
                          Tool
                        </FormLabel>
                        <Input
                          size="sm"
                          placeholder="Any"
                          value={draftFilters.tool}
                          onChange={(e) =>
                            setDraftFilters((f) => ({
                              ...f,
                              tool: e.target.value,
                            }))
                          }
                        />
                      </FormControl>
                      <FormControl size="sm">
                        <FormLabel fontSize="xs" mb={1}>
                          Agent
                        </FormLabel>
                        <Input
                          size="sm"
                          placeholder="e.g. claude-code"
                          value={draftFilters.client_name}
                          onChange={(e) =>
                            setDraftFilters((f) => ({
                              ...f,
                              client_name: e.target.value,
                            }))
                          }
                        />
                      </FormControl>
                    </HStack>
                    <HStack gap={2} align="end">
                      <FormControl size="sm" flex={1}>
                        <FormLabel fontSize="xs" mb={1}>
                          Status
                        </FormLabel>
                        <Select
                          isClearable
                          options={STATUS_OPTIONS}
                          placeholder="Any"
                          size="sm"
                          menuPortalTarget={document.body}
                          menuPosition="fixed"
                          value={
                            STATUS_OPTIONS.find(
                              (o) => o.value === draftFilters.status,
                            ) ?? null
                          }
                          onChange={(option) =>
                            setDraftFilters((f) => ({
                              ...f,
                              status: option?.value ?? "",
                            }))
                          }
                        />
                      </FormControl>
                      <Button
                        size="sm"
                        variant="primary"
                        onClick={handleApply}
                        data-testid="invocations-apply-filter-button">
                        Apply Filter
                      </Button>
                    </HStack>
                  </VStack>
                </Collapse>
              </Box>

              <Box flex={1} overflow="auto">
                {isLoading ? (
                  <HStack justify="center" py={8}>
                    <Spinner size="sm" color="brand.base" />
                  </HStack>
                ) : isError ? (
                  <Text p={4} color="text.error" fontSize="sm">
                    Failed to load invocations.
                  </Text>
                ) : items.length === 0 ? (
                  <Text p={4} color="text.subtle" fontSize="sm">
                    {hasClearedLoadedInvocations
                      ? "Cleared. New invocations will appear here."
                      : "No invocations match the current filters."}
                  </Text>
                ) : (
                  <Table size="sm" variant="simple">
                    <Thead
                      position="sticky"
                      top={0}
                      bg="background.table.header"
                      zIndex={1}>
                      <Tr>
                        <Th>Agent</Th>
                        <Th>Server</Th>
                        <Th>Tool</Th>
                        <Th>Agent Record</Th>
                        <Th>Status</Th>
                        <Th>Decided By</Th>
                        <Th>Submitted</Th>
                      </Tr>
                    </Thead>
                    <Tbody>
                      {items.map((inv) => (
                        <Tr
                          key={inv.invocation_id}
                          cursor="pointer"
                          sx={{
                            "&, &:nth-of-type(odd), &:nth-of-type(even)": {
                              bg:
                                resolvedSelectedId === inv.invocation_id
                                  ? "background.table.row.selected"
                                  : undefined,
                            },
                            "&:hover, &:nth-of-type(odd):hover, &:nth-of-type(even):hover":
                              {
                                bg:
                                  resolvedSelectedId === inv.invocation_id
                                    ? "background.table.row.selected"
                                    : "background.table.row.hover",
                              },
                          }}
                          onClick={() => {
                            setDetailClosed(false);
                            setSelectedId(inv.invocation_id);
                            if (resolvedSelectedId !== inv.invocation_id) {
                              setShowDenyInput(false);
                              setShowArgsJson(false);
                              setDenyMessage("");
                            }
                          }}>
                          <Td
                            borderLeft="4px solid"
                            borderLeftColor={
                              resolvedSelectedId === inv.invocation_id
                                ? "brand.base"
                                : "transparent"
                            }
                            pl={
                              resolvedSelectedId === inv.invocation_id ? 2 : 3
                            }>
                            {(() => {
                              const name = inv.agent_client_name ?? "";
                              const version = inv.agent_client_version ?? "";
                              const agentID = inv.agent_id ?? "";
                              const tooltipLines = [
                                version ? `version ${version}` : null,
                                agentID ? `agent_id ${agentID}` : null,
                              ].filter(Boolean);
                              const label = name
                                ? name
                                : detectAgentKind(name) === "unknown"
                                  ? "Unknown"
                                  : "—";
                              return (
                                <Tooltip
                                  hasArrow
                                  isDisabled={tooltipLines.length === 0}
                                  label={tooltipLines.join("\n")}
                                  whiteSpace="pre-line"
                                  placement="top-start"
                                  openDelay={300}>
                                  <HStack gap={2} align="center">
                                    <AgentIcon name={name} size={20} />
                                    <Text fontSize="xs" color="text.base">
                                      {label}
                                    </Text>
                                  </HStack>
                                </Tooltip>
                              );
                            })()}
                          </Td>
                          <Td>
                            <Text fontSize="xs" color="text.subtle">
                              {inv.server_name || "none"}
                            </Text>
                          </Td>
                          <Td>
                            <Text fontSize="xs" color="text.subtle">
                              {inv.tool_name || "—"}
                            </Text>
                          </Td>
                          <Td>
                            {(() => {
                              const agentID = inv.agent_id ?? "";
                              if (!agentID) {
                                return (
                                  <Text fontSize="xs" color="text.subtle">
                                    —
                                  </Text>
                                );
                              }
                              const record = agentByAgentID.get(agentID);
                              if (record) {
                                return (
                                  <Badge
                                    colorScheme="gray"
                                    fontSize="2xs"
                                    title={`agent_id: ${agentID}`}>
                                    {record.name}
                                  </Badge>
                                );
                              }
                              return (
                                <Badge
                                  colorScheme="gray"
                                  fontSize="2xs"
                                  title={`agent_id: ${agentID} (no matching agent record)`}>
                                  Unassociated
                                </Badge>
                              );
                            })()}
                          </Td>
                          <Td>
                            {(() => {
                              const statusColor =
                                STATUS_COLOR[inv.status] ?? "gray";
                              return (
                                <Tag
                                  colorScheme={statusColor}
                                  size="sm"
                                  textTransform="capitalize">
                                  <Box
                                    as="span"
                                    boxSize="8px"
                                    borderRadius="full"
                                    bg={`${statusColor}.500`}
                                    mr={1.5}
                                  />
                                  <Box as="span" mb="1px">
                                    {STATUS_LABEL[inv.status] ?? inv.status}
                                  </Box>
                                </Tag>
                              );
                            })()}
                          </Td>
                          <Td>
                            <HStack gap={1}>
                              {getDisposition(inv).map((d) =>
                                d.label !== "—" ? (
                                  <Badge
                                    key={d.label}
                                    colorScheme={d.color}
                                    fontSize="2xs"
                                    title={inv.approval?.reason ?? undefined}>
                                    {d.label}
                                  </Badge>
                                ) : null,
                              )}
                              {isAIEvaluated(inv) &&
                                inv.approval?.confidence_score != null && (
                                  <Badge
                                    colorScheme={getConfidenceColor(
                                      inv.approval.confidence_score,
                                    )}
                                    fontSize="2xs"
                                    title={`Confidence: ${formatConfidence(inv.approval.confidence_score)}`}>
                                    {formatConfidence(
                                      inv.approval.confidence_score,
                                    )}
                                  </Badge>
                                )}
                            </HStack>
                          </Td>
                          <Td>
                            <Text fontSize="xs" color="text.subtle">
                              {formatDate(inv.submitted_at)}
                            </Text>
                          </Td>
                        </Tr>
                      ))}
                    </Tbody>
                  </Table>
                )}
              </Box>
              {totalPages > 1 && (
                <Box
                  px={3}
                  py={2}
                  borderTopWidth={1}
                  borderColor="border.base"
                  flexShrink={0}>
                  <HStack justify="space-between" align="center">
                    <Text fontSize="xs" color="text.subtle">
                      Showing {(page - 1) * PAGE_SIZE + 1}–
                      {Math.min(page * PAGE_SIZE, total)} of {total}
                    </Text>
                    <HStack gap={1}>
                      <Button
                        size="xs"
                        variant="outline"
                        isDisabled={page <= 1 || isLoading}
                        onClick={() => setPage((p) => p - 1)}>
                        Prev
                      </Button>
                      <Text fontSize="xs" color="text.subtle">
                        {page} / {totalPages}
                      </Text>
                      <Button
                        size="xs"
                        variant="outline"
                        isDisabled={page >= totalPages || isLoading}
                        onClick={() => setPage((p) => p + 1)}>
                        Next
                      </Button>
                    </HStack>
                  </HStack>
                </Box>
              )}
            </Box>
          }
          right={
            !resolvedSelectedId ? null : (
              <Box overflow="auto" p={4} h="full">
                {detailLoading ? (
                  <HStack justify="center" py={8}>
                    <Spinner size="md" color="brand.base" />
                  </HStack>
                ) : !detail ? (
                  <Alert status="error" borderRadius="md">
                    <AlertIcon />
                    <AlertDescription>
                      Could not load invocation detail.
                    </AlertDescription>
                  </Alert>
                ) : (
                  <VStack align="stretch" gap={4}>
                    <Flex
                      justify="space-between"
                      align="start"
                      wrap="nowrap"
                      gap={2}>
                      <VStack align="start" gap={4} flex={1} minW={0}>
                        <VStack align="start" gap={1}>
                          <Heading size="md" fontFamily="mono">
                            {detail.server_name || "none"} ·{" "}
                            {detail.tool_name || "—"}
                          </Heading>
                          <Text fontSize="sm">
                            {formatDate(detail.submitted_at)}
                          </Text>
                          <Text
                            fontSize="xs"
                            fontFamily="mono"
                            color="text.subtle">
                            {detail.invocation_id}
                          </Text>
                        </VStack>
                        <VStack align="start" gap={1}>
                          <HStack gap={2}>
                            {(() => {
                              const statusColor =
                                STATUS_COLOR[detail.status] ?? "gray";
                              return (
                                <Tag
                                  colorScheme={statusColor}
                                  size="md"
                                  textTransform="capitalize">
                                  <Box
                                    as="span"
                                    boxSize="8px"
                                    borderRadius="full"
                                    bg={`${statusColor}.500`}
                                    mr={1.5}
                                  />
                                  <Box as="span" mb="1px">
									{STATUS_LABEL[detail.status] ??
										detail.status}
                                  </Box>
                                </Tag>
                              );
                            })()}
                            {getDisposition(detail).map((d) =>
                              d.label !== "—" ? (
                                <Badge
                                  key={d.label}
                                  colorScheme={d.color}
                                  fontSize="sm">
                                  {d.label}
                                </Badge>
                              ) : null,
                            )}
                            {isAIEvaluated(detail) &&
                              detail.approval?.confidence_score != null && (
                                <Badge
                                  colorScheme={getConfidenceColor(
                                    detail.approval.confidence_score,
                                  )}
                                  fontSize="xs">
                                  Confidence:{" "}
                                  {formatConfidence(
                                    detail.approval.confidence_score,
                                  )}
                                </Badge>
                              )}
                          </HStack>
                          {detail.approval?.reason && (
                            <Text
                              fontSize="xs"
                              fontFamily="mono"
                              color="text.subtle">
                              {detail.approval.reason}
                            </Text>
                          )}
                        </VStack>
                        {detail.matched_rule_id && (
                          <Text fontSize="xs" color="text.subtle">
                            Matched rule:{" "}
                            <Code fontSize="xs">{detail.matched_rule_id}</Code>
                          </Text>
                        )}

                        {(detail.agent_id ||
                          detail.agent_client_name ||
                          detail.user_id) && (
                          <VStack
                            align="start"
                            gap={0}
                            mt={1}
                            pt={1}
                            borderTopWidth={1}
                            borderColor="border.base"
                            w="full">
                            {detail.agent_client_name && (
                              <HStack gap={2} align="center">
                                <AgentIcon
                                  name={detail.agent_client_name}
                                  size={18}
                                />
                                <Text fontSize="xs" color="text.base">
                                  <Text as="span" color="text.subtle">
                                    Agent type:{" "}
                                  </Text>
                                  {detail.agent_client_name}
                                  {detail.agent_client_version
                                    ? ` ${detail.agent_client_version}`
                                    : ""}
                                </Text>
                              </HStack>
                            )}
                            {detail.agent_id && (
                              <Text
                                fontSize="xs"
                                fontFamily="mono"
                                color="text.subtle">
                                <Text
                                  as="span"
                                  fontFamily="body"
                                  color="text.subtle">
                                  Agent ID:{" "}
                                </Text>
                                {detail.agent_id}
                              </Text>
                            )}
                            {detail.user_id && (
                              <Text
                                fontSize="xs"
                                fontFamily="mono"
                                color="text.subtle">
                                <Text
                                  as="span"
                                  fontFamily="body"
                                  color="text.subtle">
                                  User:{" "}
                                </Text>
                                {detail.user_id}
                              </Text>
                            )}
                          </VStack>
                        )}
                      </VStack>
                      <CloseButton
                        size="sm"
                        flexShrink={0}
                        onClick={handleCloseDetail}
                        aria-label="Close details"
                        title="Close details"
                        data-testid="invocation-detail-close-button"
                      />
                    </Flex>

                    {auditEntries.length > 0 && (
                      <>
                        <InvocationAuditSection entries={auditEntries} />
                        <Divider />
                      </>
                    )}

                    <Box
                      p={3}
                      borderWidth={1}
                      borderColor="border.base"
                      borderRadius="md"
                      bg="background.container.subtle">
                      <Flex
                        justify="space-between"
                        align="center"
                        mb={detail.summary ? 2 : 0}
                        gap={2}
                        wrap="wrap">
                        <Text
                          fontSize="xs"
                          fontWeight="semibold"
                          textTransform="uppercase"
                          color="text.subtle"
                          letterSpacing="wide">
                          Summary
                        </Text>
                        <Button
                          variant="outline"
                          size="xs"
                          isLoading={isCurrentInvocationSummarizing}
                          isDisabled={
                            !hasSummaryModel || isCurrentInvocationSummarizing
                          }
                          title={
                            !hasSummaryModel
                              ? "Set an Invocation Summary Model in Settings to enable"
                              : undefined
                          }
                          onClick={handleSummarize}>
                          {detail.summary ? "Re-summarize" : "Summarize"}
                        </Button>
                      </Flex>
                      {detail.summary ? (
                        <Text fontSize="sm" whiteSpace="pre-wrap">
                          {detail.summary}
                        </Text>
                      ) : (
                        <Text fontSize="xs" color="text.subtle">
                          {hasSummaryModel
                            ? "No summary yet. Click Summarize to generate one."
                            : "No summary model configured. Set one in Settings to enable summarization."}
                        </Text>
                      )}
                      {didCurrentInvocationSummaryFail && (
                        <Text fontSize="xs" color="text.error" mt={2}>
                          Failed to summarize invocation.
                        </Text>
                      )}
                    </Box>

                    {detail.input != null &&
                      !(
                        typeof detail.input === "object" &&
                        detail.input !== null &&
                        Object.keys(detail.input as object).length === 0
                      ) && (
                        <Box>
                          <Text
                            fontSize="xs"
                            fontWeight="semibold"
                            textTransform="uppercase"
                            color="text.subtle"
                            letterSpacing="wide"
                            mb={2}>
                            Arguments
                          </Text>
                          {(() => {
                            const d = extractDiff(detail.input);
                            if (d)
                              return (
                                <DiffViewer diff={d.diff} filePath={d.path} />
                              );

                            const input = detail.input;
                            const isPlainObject =
                              typeof input === "object" &&
                              input !== null &&
                              !Array.isArray(input);

                            const renderArgValue = (value: unknown) => {
                              if (value === null) return "null";
                              if (value === undefined) return "undefined";
                              if (typeof value === "string") return value;
                              if (
                                typeof value === "number" ||
                                typeof value === "boolean"
                              ) {
                                return String(value);
                              }
                              return JSON.stringify(value, null, 2);
                            };

                            const jsonBlock = (
                              <Code
                                display="block"
                                whiteSpace="pre-wrap"
                                fontSize="xs"
                                p={3}
                                borderRadius="md"
                                bg="background.container.subtle"
                                w="full"
                                data-testid="invocation-arguments-json">
                                {JSON.stringify(input, null, 2)}
                              </Code>
                            );

                            if (!isPlainObject) return jsonBlock;

                            return (
                              <VStack align="stretch" gap={2}>
                                <Box
                                  borderWidth={1}
                                  borderColor="border.base"
                                  borderRadius="md"
                                  overflow="hidden">
                                  <Table
                                    size="sm"
                                    variant="simple"
                                    data-testid="invocation-arguments-table">
                                    <Thead bg="background.table.header">
                                      <Tr>
                                        <Th>Key</Th>
                                        <Th>Value</Th>
                                      </Tr>
                                    </Thead>
                                    <Tbody>
                                      {Object.entries(
                                        input as Record<string, unknown>,
                                      ).map(([key, value]) => (
                                        <Tr
                                          key={key}
                                          data-testid="invocation-arguments-row"
                                          data-testid-id={key}>
                                          <Td
                                            fontFamily="mono"
                                            fontSize="xs"
                                            fontWeight="medium"
                                            verticalAlign="top"
                                            whiteSpace="nowrap">
                                            {key}
                                          </Td>
                                          <Td fontSize="xs" verticalAlign="top">
                                            <Box
                                              as="pre"
                                              m={0}
                                              whiteSpace="pre-wrap"
                                              wordBreak="break-word"
                                              fontFamily={
                                                typeof value === "object" &&
                                                value !== null
                                                  ? "mono"
                                                  : "body"
                                              }>
                                              {renderArgValue(value)}
                                            </Box>
                                          </Td>
                                        </Tr>
                                      ))}
                                    </Tbody>
                                  </Table>
                                </Box>
                                <Box alignSelf="flex-start">
                                  <Button
                                    variant="link"
                                    size="xs"
                                    leftIcon={
                                      <Icon
                                        as={
                                          showArgsJson
                                            ? ChevronUpIcon
                                            : ChevronDownIcon
                                        }
                                        boxSize={4}
                                      />
                                    }
                                    onClick={() => setShowArgsJson((v) => !v)}
                                    data-testid="invocation-arguments-json-toggle">
                                    {showArgsJson
                                      ? "Hide raw JSON"
                                      : "Show raw JSON"}
                                  </Button>
                                </Box>
                                <Collapse in={showArgsJson} animateOpacity>
                                  {jsonBlock}
                                </Collapse>
                              </VStack>
                            );
                          })()}
                        </Box>
                      )}

                    {isPending && (
                      <Box
                        p={4}
                        borderWidth={1}
                        borderColor="border.base"
                        borderRadius="md"
                        bg="background.container.subtle">
                        <Text fontWeight="medium" mb={3}>
                          Approval Required
                        </Text>

                        {!showDenyInput ? (
                          <HStack
                            gap={2}
                            w="full"
                            data-testid="invocation-approval-actions">
                            <ButtonGroup
                              isAttached
                              colorScheme="green"
                              w="full"
                              size="sm">
                              <Button
                                w="full"
                                isLoading={approve.isLoading}
                                onClick={handleApproveOnce}
                                data-testid="invocation-approve-button">
                                Approve
                              </Button>
                              <Menu placement="bottom-end">
                                <MenuButton
                                  as={IconButton}
                                  aria-label="More approve options"
                                  icon={
                                    <Icon as={ChevronDownIcon} boxSize={4} />
                                  }
                                  isDisabled={approve.isLoading}
                                  borderLeftWidth="1px"
                                  borderLeftColor="whiteAlpha.400"
                                  data-testid="invocation-approve-menu-button"
                                />
                                <MenuList>
                                  <MenuItem
                                    onClick={handleAlwaysApprove}
                                    data-testid="invocation-always-approve-menu-item">
                                    Always approve
                                  </MenuItem>
                                </MenuList>
                              </Menu>
                            </ButtonGroup>
                            <ButtonGroup
                              isAttached
                              colorScheme="red"
                              size="sm"
                              w="full">
                              <Button
                                w="full"
                                onClick={() => {
                                  setDenyMode("once");
                                  setShowDenyInput(true);
                                }}
                                data-testid="invocation-deny-button">
                                Deny
                              </Button>
                              <Menu placement="bottom-end">
                                <MenuButton
                                  as={IconButton}
                                  aria-label="More deny options"
                                  icon={
                                    <Icon as={ChevronDownIcon} boxSize={4} />
                                  }
                                  borderLeftWidth="1px"
                                  borderLeftColor="red.200"
                                  data-testid="invocation-deny-menu-button"
                                />
                                <MenuList>
                                  <MenuItem
                                    onClick={handleAlwaysDeny}
                                    data-testid="invocation-always-deny-menu-item">
                                    Always deny
                                  </MenuItem>
                                </MenuList>
                              </Menu>
                            </ButtonGroup>
                          </HStack>
                        ) : (
                          <VStack
                            align="stretch"
                            gap={2}
                            data-testid="invocation-deny-form">
                            <Textarea
                              size="sm"
                              placeholder="Reason for denial (optional)"
                              value={denyMessage}
                              onChange={(e) => setDenyMessage(e.target.value)}
                              rows={3}
                              data-testid="invocation-deny-reason-input"
                            />
                            <HStack gap={2}>
                              <Button
                                variant="outlineDanger"
                                size="sm"
                                isLoading={deny.isLoading}
                                onClick={
                                  denyMode === "always"
                                    ? handleAlwaysDeny
                                    : handleDenyOnce
                                }
                                data-testid="invocation-deny-confirm-button">
                                {denyMode === "always" ? "Always deny" : "Deny"}
                              </Button>
                              <Button
                                variant="ghost"
                                size="sm"
                                onClick={() => {
                                  setShowDenyInput(false);
                                  setDenyMessage("");
                                  setDenyMode("once");
                                }}
                                data-testid="invocation-deny-cancel-button">
                                Cancel
                              </Button>
                            </HStack>
                          </VStack>
                        )}

                      </Box>
                    )}

                    <Button
                      variant="outline"
                      size="sm"
                      alignSelf="flex-start"
                      leftIcon={<Icon as={ShieldCheckIcon} boxSize={4} />}
                      onClick={() => {
                        const agentEntry = detail.agent_id
                          ? agentByAgentID.get(detail.agent_id)
                          : undefined;
                        setRuleModalInitial({
                          server_patterns: detail.server_name
                            ? [detail.server_name]
                            : [],
                          tool_patterns: detail.tool_name
                            ? [detail.tool_name]
                            : [],
                          agent_cuids: agentEntry?.cuid
                            ? [agentEntry.cuid]
                            : [],
                          description: buildRuleDescription(
                            undefined,
                            detail.server_name,
                            detail.tool_name,
                          ),
                        });
                        ruleModalOnSuccessRef.current = undefined;
                        openRuleModal();
                      }}
                      data-testid="invocation-create-rule-button">
                      Create Rule From This
                    </Button>

                    <CreateRuleModal
                      isOpen={isRuleModalOpen}
                      onClose={closeRuleModal}
                      onSuccess={ruleModalOnSuccessRef.current}
                      {...(ruleModalInitial
                        ? { initialValues: ruleModalInitial }
                        : {})}
                    />

                    {detail.result != null && (
                      <Box>
                        <Text
                          fontSize="xs"
                          fontWeight="semibold"
                          textTransform="uppercase"
                          color="text.subtle"
                          letterSpacing="wide"
                          mb={2}>
                          Result
                        </Text>
                        {(() => {
                          const d = extractDiff(detail.result);
                          if (d)
                            return (
                              <DiffViewer diff={d.diff} filePath={d.path} />
                            );
                          return (
                            <Code
                              display="block"
                              whiteSpace="pre-wrap"
                              fontSize="xs"
                              p={3}
                              borderRadius="md"
                              bg="background.container.subtle"
                              w="full">
                              {JSON.stringify(detail.result, null, 2)}
                            </Code>
                          );
                        })()}
                      </Box>
                    )}

                    {detail.error != null && (
                      <Box>
                        <Text
                          fontSize="xs"
                          fontWeight="semibold"
                          textTransform="uppercase"
                          color="text.subtle"
                          letterSpacing="wide"
                          mb={2}>
                          Error
                        </Text>
                        <Code
                          display="block"
                          whiteSpace="pre-wrap"
                          fontSize="xs"
                          p={3}
                          borderRadius="md"
                          bg="background.container.subtle"
                          w="full">
                          {JSON.stringify(detail.error, null, 2)}
                        </Code>
                      </Box>
                    )}

                    <Box>
                      <Text
                        fontSize="xs"
                        fontWeight="semibold"
                        textTransform="uppercase"
                        color="text.subtle"
                        letterSpacing="wide"
                        mb={2}>
                        Events
                      </Text>
                      {eventsLoading ? (
                        <Spinner size="sm" color="brand.base" />
                      ) : (eventsData?.items ?? []).length === 0 ? (
                        <Text fontSize="sm" color="text.subtle">
                          No events recorded.
                        </Text>
                      ) : (
                        <Box
                          borderWidth={1}
                          borderColor="border.base"
                          borderRadius="md"
                          overflow="hidden">
                          <Table size="sm" variant="simple">
                            <Thead bg="background.table.header">
                              <Tr>
                                <Th />
                                <Th>Event</Th>
                                <Th>Date</Th>
                              </Tr>
                            </Thead>
                            <Tbody>
                              {(eventsData?.items ?? []).map((evt, i) => (
                                <EventRow
                                  key={`${evt.timestamp}-${evt.type}-${String(i)}`}
                                  event={evt}
                                />
                              ))}
                            </Tbody>
                          </Table>
                        </Box>
                      )}
                    </Box>
                  </VStack>
                )}
              </Box>
            )
          }
        />
      </Box>
    </Box>
  );
};

export default Invocations;
