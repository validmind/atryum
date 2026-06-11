/* eslint-disable react/jsx-no-bind */
import React, { useCallback, useMemo, useState } from "react";
import ResizablePanels from "../components/ResizablePanels";
import {
  Badge,
  Box,
  Button,
  ButtonGroup,
  CloseButton,
  Code,
  Collapse,
  Flex,
  FormControl,
  FormLabel,
  HStack,
  Icon,
  IconButton,
  Input,
  Menu,
  MenuButton,
  MenuItem,
  MenuList,
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
import { useCreateRule } from "../hooks/useRules";
import { useAgents } from "../hooks/useAgents";
import { useInvocationStream } from "../hooks/useInvocationStream";
import {
  invocationsApi,
  type Invocation,
  type InvocationEvent,
  type InvocationStatus,
  type RuleAction,
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
} from "../utils/invocationDisplay";

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

const truncateMiddle = (
  value: string,
  startChars = 8,
  endChars = 6,
): string => {
  if (value.length <= startChars + endChars + 1) return value;
  return `${value.slice(0, startChars)}…${value.slice(-endChars)}`;
};

const wildcardToRegExp = (pattern: string): RegExp => {
  const escapedPattern = pattern.replace(/[.+^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`^${escapedPattern.replace(/\*/g, ".*")}$`);
};

const matchesAnyPattern = (
  patterns: string[],
  value: string | null | undefined,
): boolean => {
  if (patterns.length === 0) return true;
  const normalizedValue = value ?? "";
  return patterns.some((pattern) =>
    wildcardToRegExp(pattern).test(normalizedValue),
  );
};

const matchesRuleScope = (invocation: Invocation, rule: RuleInput): boolean =>
  rule.user_pattern === "*" &&
  matchesAnyPattern(rule.server_patterns, invocation.server_name) &&
  matchesAnyPattern(rule.tool_patterns, invocation.tool_name);

const loadPendingInvocations = async (): Promise<Invocation[]> => {
  const limit = 50;
  const pendingInvocations: Invocation[] = [];
  for (let offset = 0; ; offset += limit) {
    const page = await invocationsApi.list({
      status: "pending_approval",
      limit,
      offset,
    });
    pendingInvocations.push(...page.items);
    if (page.items.length < limit) return pendingInvocations;
  }
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
  const [denyMessage, setDenyMessage] = useState("");
  const [showDenyInput, setShowDenyInput] = useState(false);
  const [detailClosed, setDetailClosed] = useState(false);
  const [showFilters, setShowFilters] = useState(false);
  const [showArgsJson, setShowArgsJson] = useState(false);
  const [denyMode, setDenyMode] = useState<"once" | "always">("once");
  const [showCustomizeScope, setShowCustomizeScope] = useState(false);
  const [ruleForm, setRuleForm] = useState({
    server_patterns: "",
    tool_patterns: "",
    user_pattern: "*",
    description: "",
  });
  const [showCreateRule, setShowCreateRule] = useState(false);
  const [createRuleForm, setCreateRuleForm] = useState<{
    action: RuleAction;
    server_patterns: string;
    tool_patterns: string;
    user_pattern: string;
    description: string;
  }>({
    action: "auto_approve",
    server_patterns: "",
    tool_patterns: "",
    user_pattern: "*",
    description: "",
  });

  const filters = useMemo(
    () =>
      Object.fromEntries(
        Object.entries(appliedFilters).filter(([, v]) => v !== ""),
      ),
    [appliedFilters],
  );

  const { data, isLoading, isError } = useInvocations(filters);
  const { data: agentsData } = useAgents();

  const items = useMemo(() => data?.items ?? [], [data?.items]);

  const agentByAgentID = useMemo(() => {
    const map = new Map<string, { cuid: string; name: string }>();
    for (const agent of agentsData?.items ?? []) {
      for (const id of agent.agent_ids) {
        map.set(id, { cuid: agent.cuid, name: agent.name });
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
  }, [items, selectedId]);

  const { data: detail, isLoading: detailLoading } =
    useInvocationDetail(resolvedSelectedId);
  const { data: eventsData, isLoading: eventsLoading } =
    useInvocationEvents(resolvedSelectedId);

  useInvocationStream(filters, resolvedSelectedId, true);

  const approve = useApproveInvocation();
  const deny = useDenyInvocation();
  const summarize = useSummarizeInvocation();
  const createRule = useCreateRule();
  const { data: settings } = useSettings();
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

  const buildScopeRule = (action: RuleAction): RuleInput => ({
    action,
    server_patterns: ruleForm.server_patterns
      ? ruleForm.server_patterns
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean)
      : [],
    tool_patterns: ruleForm.tool_patterns
      ? ruleForm.tool_patterns
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean)
      : [],
    user_pattern: ruleForm.user_pattern || "*",
    description: ruleForm.description || undefined,
    enabled: true,
  });

  const resetRuleForm = (serverName?: string, toolName?: string) => {
    setRuleForm({
      server_patterns: serverName ?? "",
      tool_patterns: toolName ?? "",
      user_pattern: "*",
      description: "",
    });
    setShowCustomizeScope(false);
  };

  const resetCreateRuleForm = (serverName?: string, toolName?: string) => {
    setCreateRuleForm({
      action: "auto_approve",
      server_patterns: serverName ?? "",
      tool_patterns: toolName ?? "",
      user_pattern: "*",
      description: "",
    });
    setShowCreateRule(false);
  };

  const handleApply = () => setAppliedFilters({ ...draftFilters });

  const handleCloseDetail = () => {
    setDetailClosed(true);
    setSelectedId(null);
  };

  const handleApproveOnce = async () => {
    if (!resolvedSelectedId) return;
    await approve.mutateAsync({ id: resolvedSelectedId });
    setShowDenyInput(false);
    resetRuleForm();
  };

  const handleAlwaysApprove = async () => {
    if (!resolvedSelectedId) return;
    const rule = buildScopeRule("auto_approve");
    await approve.mutateAsync({ id: resolvedSelectedId, createRule: rule });

    const matchingPendingInvocations = (await loadPendingInvocations()).filter(
      (invocation) =>
        invocation.invocation_id !== resolvedSelectedId &&
        invocation.status === "pending_approval" &&
        matchesRuleScope(invocation, rule),
    );

    if (
      matchingPendingInvocations.length > 0 &&
      window.confirm(
        `Apply this auto-approve rule to ${matchingPendingInvocations.length} other pending approval${
          matchingPendingInvocations.length === 1 ? "" : "s"
        } now?`,
      )
    ) {
      await Promise.all(
        matchingPendingInvocations.map((invocation) =>
          approve.mutateAsync({ id: invocation.invocation_id }),
        ),
      );
    }

    setShowDenyInput(false);
    resetRuleForm();
  };

  const handleDenyOnce = async () => {
    if (!resolvedSelectedId) return;
    await deny.mutateAsync({ id: resolvedSelectedId, message: denyMessage });
    setDenyMessage("");
    setShowDenyInput(false);
    resetRuleForm();
  };

  const handleAlwaysDeny = async () => {
    if (!resolvedSelectedId) return;
    const rule = buildScopeRule("auto_deny");
    await deny.mutateAsync({
      id: resolvedSelectedId,
      message: denyMessage,
      createRule: rule,
    });
    setDenyMessage("");
    setShowDenyInput(false);
    resetRuleForm();
  };

  const handleSaveRule = async () => {
    const input: RuleInput = {
      action: createRuleForm.action,
      server_patterns: createRuleForm.server_patterns
        ? createRuleForm.server_patterns
            .split(",")
            .map((s) => s.trim())
            .filter(Boolean)
        : [],
      tool_patterns: createRuleForm.tool_patterns
        ? createRuleForm.tool_patterns
            .split(",")
            .map((s) => s.trim())
            .filter(Boolean)
        : [],
      user_pattern: createRuleForm.user_pattern || "*",
      description: createRuleForm.description || undefined,
      enabled: true,
    };
    try {
      await createRule.mutateAsync(input);
      resetCreateRuleForm();
      toast({
        title: "Rule created",
        status: "success",
        duration: 3000,
        isClosable: true,
      });
    } catch (err) {
      const message =
        (err as { response?: { data?: { error?: { message?: string } } } })
          ?.response?.data?.error?.message ??
        (err as Error)?.message ??
        "Failed to create rule";
      toast({
        title: "Failed to create rule",
        description: message,
        status: "error",
        duration: 6000,
        isClosable: true,
      });
    }
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
                <Flex justify="flex-end">
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
                          classNamePrefix="chakra-react-select"
                          isClearable
                          options={STATUS_OPTIONS}
                          placeholder="Any"
                          size="sm"
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
                    No invocations match the current filters.
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
                        <Th>ID</Th>
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
                              resetRuleForm(inv.server_name, inv.tool_name);
                              resetCreateRuleForm(
                                inv.server_name,
                                inv.tool_name,
                              );
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
                            <Tooltip
                              hasArrow
                              label={inv.invocation_id}
                              placement="top-start"
                              openDelay={300}>
                              <Text
                                fontSize="xs"
                                fontFamily="mono"
                                color="text.subtle"
                                whiteSpace="nowrap">
                                {truncateMiddle(inv.invocation_id)}
                              </Text>
                            </Tooltip>
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
                                  {STATUS_LABEL[inv.status] ?? inv.status}
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
                                {STATUS_LABEL[detail.status] ?? detail.status}
                              </Tag>
                            );
                          })()}
                          {getDisposition(detail).map((d) =>
                            d.label !== "—" ? (
                              <Badge
                                key={d.label}
                                colorScheme={d.color}
                                fontSize="xs">
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
                        {detail.matched_rule_id && (
                          <Text fontSize="xs" color="text.subtle">
                            Matched rule:{" "}
                            <Code fontSize="xs">{detail.matched_rule_id}</Code>
                          </Text>
                        )}
                        <Text fontSize="sm" color="text.subtle">
                          {formatDate(detail.submitted_at)}
                        </Text>
                        <Text fontSize="sm" color="text.subtle">
                          {detail.server_name || "none"} ·{" "}
                          {detail.tool_name || "—"}
                        </Text>
                        <Text
                          fontSize="xs"
                          fontFamily="mono"
                          color="text.subtle">
                          {detail.invocation_id}
                        </Text>
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
                        onClick={handleCloseDetail}
                        aria-label="Close details"
                        title="Close details"
                        data-testid="invocation-detail-close-button"
                      />
                    </Flex>

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
                                    onClick={() => {
                                      setDenyMode("always");
                                      setShowDenyInput(true);
                                    }}
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

                        <Box mt={3}>
                          <Button
                            variant="ghost"
                            size="xs"
                            onClick={() => {
                              if (!showCustomizeScope) {
                                setRuleForm((f) => ({
                                  ...f,
                                  server_patterns:
                                    f.server_patterns ||
                                    detail.server_name ||
                                    "",
                                  tool_patterns:
                                    f.tool_patterns || detail.tool_name || "",
                                }));
                              }
                              setShowCustomizeScope((v) => !v);
                            }}>
                            {showCustomizeScope
                              ? "▲ Hide rule scope"
                              : "▼ Customize rule scope"}
                          </Button>
                          <Collapse in={showCustomizeScope} animateOpacity>
                            <VStack
                              align="stretch"
                              gap={2}
                              mt={2}
                              p={3}
                              borderWidth={1}
                              borderColor="border.base"
                              borderRadius="md">
                              <Text
                                fontSize="xs"
                                color="text.subtle"
                                fontWeight="semibold">
                                Rule scope for &ldquo;Always approve&rdquo; /
                                &ldquo;Always deny&rdquo;
                              </Text>
                              <FormControl size="sm">
                                <FormLabel fontSize="xs" mb={1}>
                                  Server patterns (comma-separated)
                                </FormLabel>
                                <Input
                                  size="sm"
                                  fontFamily="mono"
                                  placeholder="e.g. github, *"
                                  value={ruleForm.server_patterns}
                                  onChange={(e) =>
                                    setRuleForm((f) => ({
                                      ...f,
                                      server_patterns: e.target.value,
                                    }))
                                  }
                                />
                              </FormControl>
                              <FormControl size="sm">
                                <FormLabel fontSize="xs" mb={1}>
                                  Tool patterns (comma-separated)
                                </FormLabel>
                                <Input
                                  size="sm"
                                  fontFamily="mono"
                                  placeholder="e.g. list_issues, *"
                                  value={ruleForm.tool_patterns}
                                  onChange={(e) =>
                                    setRuleForm((f) => ({
                                      ...f,
                                      tool_patterns: e.target.value,
                                    }))
                                  }
                                />
                              </FormControl>
                              <FormControl size="sm">
                                <FormLabel fontSize="xs" mb={1}>
                                  User pattern
                                </FormLabel>
                                <Input
                                  size="sm"
                                  fontFamily="mono"
                                  placeholder="* for any"
                                  value={ruleForm.user_pattern}
                                  onChange={(e) =>
                                    setRuleForm((f) => ({
                                      ...f,
                                      user_pattern: e.target.value,
                                    }))
                                  }
                                />
                              </FormControl>
                              <FormControl size="sm">
                                <FormLabel fontSize="xs" mb={1}>
                                  Description (optional)
                                </FormLabel>
                                <Input
                                  size="sm"
                                  placeholder="e.g. Allow GitHub read tools"
                                  value={ruleForm.description}
                                  onChange={(e) =>
                                    setRuleForm((f) => ({
                                      ...f,
                                      description: e.target.value,
                                    }))
                                  }
                                />
                              </FormControl>
                            </VStack>
                          </Collapse>
                        </Box>
                      </Box>
                    )}

                    <Button
                      variant="outline"
                      size="sm"
                      alignSelf="flex-start"
                      leftIcon={<Icon as={ShieldCheckIcon} boxSize={4} />}
                      onClick={() => {
                        setCreateRuleForm((f) => ({
                          ...f,
                          server_patterns:
                            f.server_patterns || detail.server_name || "",
                          tool_patterns:
                            f.tool_patterns || detail.tool_name || "",
                        }));
                        setShowCreateRule(true);
                      }}
                      data-testid="invocation-create-rule-button">
                      Create Rule From This
                    </Button>
                    <Modal
                      isOpen={showCreateRule}
                      onClose={() => setShowCreateRule(false)}
                      size="lg"
                      isCentered>
                      <ModalOverlay />
                      <ModalContent data-testid="create-rule-modal">
                        <ModalHeader>
                          Create Rule From This Invocation
                        </ModalHeader>
                        <ModalCloseButton data-testid="create-rule-modal-close-button" />
                        <ModalBody>
                          <VStack align="stretch" gap={2}>
                            <FormControl size="sm">
                              <FormLabel fontSize="xs" mb={1}>
                                Action
                              </FormLabel>
                              <Select
                                classNamePrefix="chakra-react-select"
                                size="sm"
                                options={[
                                  {
                                    value: "auto_approve",
                                    label: "Auto Approve",
                                  },
                                  { value: "auto_deny", label: "Auto Deny" },
                                ]}
                                value={{
                                  value: createRuleForm.action,
                                  label:
                                    createRuleForm.action === "auto_approve"
                                      ? "Auto Approve"
                                      : "Auto Deny",
                                }}
                                onChange={(opt) =>
                                  opt &&
                                  setCreateRuleForm((f) => ({
                                    ...f,
                                    action: opt.value as RuleAction,
                                  }))
                                }
                              />
                            </FormControl>
                            <FormControl size="sm">
                              <FormLabel fontSize="xs" mb={1}>
                                Server patterns (comma-separated)
                              </FormLabel>
                              <Input
                                size="sm"
                                fontFamily="mono"
                                placeholder="e.g. github, *"
                                value={createRuleForm.server_patterns}
                                onChange={(e) =>
                                  setCreateRuleForm((f) => ({
                                    ...f,
                                    server_patterns: e.target.value,
                                  }))
                                }
                              />
                            </FormControl>
                            <FormControl size="sm">
                              <FormLabel fontSize="xs" mb={1}>
                                Tool patterns (comma-separated)
                              </FormLabel>
                              <Input
                                size="sm"
                                fontFamily="mono"
                                placeholder="e.g. list_issues, *"
                                value={createRuleForm.tool_patterns}
                                onChange={(e) =>
                                  setCreateRuleForm((f) => ({
                                    ...f,
                                    tool_patterns: e.target.value,
                                  }))
                                }
                              />
                            </FormControl>
                            <FormControl size="sm">
                              <FormLabel fontSize="xs" mb={1}>
                                User pattern
                              </FormLabel>
                              <Input
                                size="sm"
                                fontFamily="mono"
                                placeholder="* for any"
                                value={createRuleForm.user_pattern}
                                onChange={(e) =>
                                  setCreateRuleForm((f) => ({
                                    ...f,
                                    user_pattern: e.target.value,
                                  }))
                                }
                              />
                            </FormControl>
                            <FormControl size="sm">
                              <FormLabel fontSize="xs" mb={1}>
                                Description (optional)
                              </FormLabel>
                              <Input
                                size="sm"
                                placeholder="e.g. Allow GitHub read tools"
                                value={createRuleForm.description}
                                onChange={(e) =>
                                  setCreateRuleForm((f) => ({
                                    ...f,
                                    description: e.target.value,
                                  }))
                                }
                              />
                            </FormControl>
                          </VStack>
                        </ModalBody>
                        <ModalFooter gap={2}>
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => setShowCreateRule(false)}
                            data-testid="create-rule-modal-cancel-button">
                            Cancel
                          </Button>
                          <Button
                            variant="primary"
                            size="sm"
                            isLoading={createRule.isLoading}
                            onClick={handleSaveRule}
                            data-testid="create-rule-modal-save-button">
                            Save rule
                          </Button>
                        </ModalFooter>
                      </ModalContent>
                    </Modal>

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
