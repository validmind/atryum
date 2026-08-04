import React, { useCallback, useMemo, useState } from 'react';
import {
  Alert,
  AlertDescription,
  AlertIcon,
  Badge,
  Box,
  Button,
  Code,
  Flex,
  FormControl,
  FormHelperText,
  FormLabel,
  HStack,
  Icon,
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
  Textarea,
  Th,
  Thead,
  Tr,
  VStack,
  useDisclosure,
} from '@chakra-ui/react';
import { Select } from 'chakra-react-select';
import { ClipboardDocumentListIcon } from '@heroicons/react/24/outline';
import { useSearchParams } from 'react-router-dom';

import { ContentPageTitle } from '../components/Layout';
import {
  usePlans,
  usePlanDetail,
  useApprovePlan,
  useDenyPlan,
  useExpirePlan,
  useRevisePlan,
} from '../hooks/usePlans';
import { usePlanStream } from '../hooks/usePlanStream';
import { apiErrorMessage, type Plan, type PlanStatus } from '../api/AtryumAPI';

const STATUS_COLOR: Record<PlanStatus, string> = {
  received: 'gray',
  pending_approval: 'orange',
  approved: 'green',
  denied: 'red',
  needs_revision: 'yellow',
  superseded: 'gray',
  completed: 'blue',
  expired: 'gray',
  cancelled: 'gray',
};

const STATUS_LABEL: Record<PlanStatus, string> = {
  received: 'Received',
  pending_approval: 'Pending Approval',
  approved: 'Approved',
  denied: 'Denied',
  needs_revision: 'Needs Revision',
  superseded: 'Superseded',
  completed: 'Completed',
  expired: 'Expired',
  cancelled: 'Cancelled',
};

const STATUS_FILTER_OPTIONS = [
  { value: '', label: 'All statuses' },
  ...Object.entries(STATUS_LABEL).map(([value, label]) => ({ value, label })),
];

const formatTimestamp = (value?: string | null): string =>
  value ? new Date(value).toLocaleString() : '—';

const PlanStatusBadge: React.FC<{ status: PlanStatus }> = ({ status }) => (
  <Badge colorScheme={STATUS_COLOR[status] ?? 'gray'} fontSize="2xs" whiteSpace="nowrap">
    {STATUS_LABEL[status] ?? status}
  </Badge>
);

type PlanDetailModalProps = {
  planId: string;
  onClose: () => void;
  onSelectPlan: (id: string) => void;
};

const PlanDetailModal: React.FC<PlanDetailModalProps> = ({ planId, onClose, onSelectPlan }) => {
  const { data: plan, isLoading } = usePlanDetail(planId);
  const approvePlan = useApprovePlan();
  const denyPlan = useDenyPlan();
  const expirePlan = useExpirePlan();
  const revisePlan = useRevisePlan();

  const [mode, setMode] = useState<'view' | 'deny' | 'revise' | 'expire'>('view');
  const [message, setMessage] = useState('');
  const [statusMsg, setStatusMsg] = useState<string | null>(null);

  const isBusy = approvePlan.isLoading || denyPlan.isLoading || revisePlan.isLoading || expirePlan.isLoading;
  const isPending = plan?.status === 'pending_approval';
  const isApproved = plan?.status === 'approved';

  const handleApprove = useCallback(async () => {
    try {
      await approvePlan.mutateAsync({ id: planId });
      setStatusMsg(null);
    } catch (err: unknown) {
      setStatusMsg(apiErrorMessage(err, 'Approve failed.'));
    }
  }, [approvePlan, planId]);

  const handleDeny = useCallback(async () => {
    try {
      await denyPlan.mutateAsync({ id: planId, message: message || undefined });
      setMode('view');
      setMessage('');
      setStatusMsg(null);
    } catch (err: unknown) {
      setStatusMsg(apiErrorMessage(err, 'Deny failed.'));
    }
  }, [denyPlan, message, planId]);

  const handleRevise = useCallback(async () => {
    if (!message.trim()) {
      setStatusMsg('Feedback is required to request a revision.');
      return;
    }
    try {
      await revisePlan.mutateAsync({ id: planId, feedback: message });
      setMode('view');
      setMessage('');
      setStatusMsg(null);
    } catch (err: unknown) {
      setStatusMsg(apiErrorMessage(err, 'Revision request failed.'));
    }
  }, [message, planId, revisePlan]);

  const handleExpire = useCallback(async () => {
    try {
      await expirePlan.mutateAsync({ id: planId });
      setMode('view');
      setStatusMsg(null);
    } catch (err: unknown) {
      setStatusMsg(apiErrorMessage(err, 'Expire failed.'));
    }
  }, [expirePlan, planId]);

  return (
    <Modal size="2xl" isCentered closeOnEsc closeOnOverlayClick isOpen onClose={onClose}>
      <ModalOverlay />
      <ModalContent>
        <ModalHeader>
          <HStack gap={3}>
            <Text>Plan Review</Text>
            {plan && <PlanStatusBadge status={plan.status} />}
            {plan && plan.revision > 1 && (
              <Badge colorScheme="blue" fontSize="2xs" variant="subtle">
                Revision {plan.revision}
              </Badge>
            )}
          </HStack>
        </ModalHeader>
        <ModalCloseButton />
        <ModalBody>
          {isLoading || !plan ? (
            <HStack justify="center" py={10}>
              <Spinner size="sm" color="brand.base" />
            </HStack>
          ) : (
            <VStack align="stretch" gap={4}>
              {statusMsg && (
                <Alert status="error" borderRadius="md" py={2}>
                  <AlertIcon />
                  <AlertDescription fontSize="sm">{statusMsg}</AlertDescription>
                </Alert>
              )}

              <Box>
                <Text fontSize="xs" color="text.subtle" mb={1}>Goal</Text>
                <Text fontSize="sm" fontWeight="semibold">{plan.goal}</Text>
              </Box>

              {plan.rationale && (
                <Box>
                  <Text fontSize="xs" color="text.subtle" mb={1}>Rationale</Text>
                  <Text fontSize="sm" whiteSpace="pre-wrap">{plan.rationale}</Text>
                </Box>
              )}

              <Box>
                <Text fontSize="xs" color="text.subtle" mb={1}>
                  Intended actions ({plan.actions.length})
                </Text>
                <VStack align="stretch" gap={2}>
                  {plan.actions.map((action, idx) => (
                    <Box key={idx} borderWidth={1} borderColor="border.base" borderRadius="md" p={2}>
                      <HStack gap={2}>
                        <Text fontSize="xs" color="text.subtle">{idx + 1}.</Text>
                        <Code fontSize="xs">{action.tool}</Code>
                        {action.server && (
                          <Badge colorScheme="gray" fontSize="2xs" variant="subtle">
                            {action.server}
                          </Badge>
                        )}
                      </HStack>
                      {action.description && (
                        <Text fontSize="xs" mt={1}>{action.description}</Text>
                      )}
                      {action.input_summary && (
                        <Text fontSize="xs" fontFamily="mono" color="text.subtle" mt={1}>
                          {action.input_summary}
                        </Text>
                      )}
                    </Box>
                  ))}
                </VStack>
              </Box>

              <HStack gap={6} wrap="wrap">
                <Box>
                  <Text fontSize="xs" color="text.subtle">Agent</Text>
                  <Text fontSize="sm" fontFamily="mono">{plan.agent_id}</Text>
                </Box>
                <Box>
                  <Text fontSize="xs" color="text.subtle">Source</Text>
                  <Text fontSize="sm">{plan.source || '—'}</Text>
                </Box>
                <Box>
                  <Text fontSize="xs" color="text.subtle">Submitted</Text>
                  <Text fontSize="sm">{formatTimestamp(plan.submitted_at)}</Text>
                </Box>
                {plan.expires_at && (
                  <Box>
                    <Text fontSize="xs" color="text.subtle">Pass expires</Text>
                    <Text fontSize="sm">{formatTimestamp(plan.expires_at)}</Text>
                  </Box>
                )}
              </HStack>

              {plan.approval?.reason && (
                <Box>
                  <Text fontSize="xs" color="text.subtle" mb={1}>Decision reason</Text>
                  <Text fontSize="sm">{plan.approval.reason}</Text>
                </Box>
              )}

              {plan.feedback && (
                <Alert status="warning" borderRadius="md" py={2}>
                  <AlertIcon />
                  <AlertDescription fontSize="sm">
                    <Text fontWeight="semibold" as="span">Revision feedback: </Text>
                    {plan.feedback}
                  </AlertDescription>
                </Alert>
              )}

              {(plan.parent_plan_id || plan.revisions.length > 0) && (
                <Box>
                  <Text fontSize="xs" color="text.subtle" mb={1}>Revision chain</Text>
                  <HStack gap={2} wrap="wrap">
                    {plan.parent_plan_id && (
                      <Button
                        size="xs"
                        variant="outline"
                        onClick={() => onSelectPlan(plan.parent_plan_id!)}
                      >
                        ← Previous revision
                      </Button>
                    )}
                    {plan.revisions.map((rev) => (
                      <Button
                        key={rev.plan_id}
                        size="xs"
                        variant="outline"
                        onClick={() => onSelectPlan(rev.plan_id)}
                      >
                        Revision {rev.revision} →
                      </Button>
                    ))}
                  </HStack>
                </Box>
              )}

              {isPending && mode !== 'view' && (
                <FormControl isRequired={mode === 'revise'}>
                  <FormLabel fontSize="sm">
                    {mode === 'revise' ? 'Revision feedback' : 'Denial reason (optional)'}
                  </FormLabel>
                  <Textarea
                    size="sm"
                    rows={3}
                    value={message}
                    placeholder={
                      mode === 'revise'
                        ? 'Tell the agent what to change before resubmitting…'
                        : 'Why is this plan denied?'
                    }
                    onChange={(e) => setMessage(e.target.value)}
                  />
                  {mode === 'revise' && (
                    <FormHelperText fontSize="xs">
                      The agent receives this feedback and can submit a revised plan.
                    </FormHelperText>
                  )}
                </FormControl>
              )}

              {isApproved && mode === 'expire' && (
                <Alert status="warning" borderRadius="md" py={2}>
                  <AlertIcon />
                  <AlertDescription fontSize="sm">
                    Expiring this plan immediately revokes its pass. This cannot be undone.
                  </AlertDescription>
                </Alert>
              )}
            </VStack>
          )}
        </ModalBody>
        <ModalFooter gap={2}>
          {isPending && mode === 'view' && (
            <>
              <Button
                variant="outlineDanger"
                size="sm"
                isDisabled={isBusy}
                onClick={() => setMode('deny')}
                mr="auto"
              >
                Deny
              </Button>
              <Button
                variant="outline"
                size="sm"
                isDisabled={isBusy}
                onClick={() => setMode('revise')}
              >
                Request Revision
              </Button>
              <Button
                variant="primary"
                size="sm"
                isLoading={approvePlan.isLoading}
                isDisabled={isBusy}
                onClick={handleApprove}
              >
                Approve
              </Button>
            </>
          )}
          {isPending && mode === 'deny' && (
            <>
              <Button variant="ghost" size="sm" isDisabled={isBusy} onClick={() => setMode('view')}>
                Back
              </Button>
              <Button
                variant="outlineDanger"
                size="sm"
                isLoading={denyPlan.isLoading}
                isDisabled={isBusy}
                onClick={handleDeny}
              >
                Confirm Deny
              </Button>
            </>
          )}
          {isPending && mode === 'revise' && (
            <>
              <Button variant="ghost" size="sm" isDisabled={isBusy} onClick={() => setMode('view')}>
                Back
              </Button>
              <Button
                variant="primary"
                size="sm"
                isLoading={revisePlan.isLoading}
                isDisabled={isBusy || !message.trim()}
                onClick={handleRevise}
              >
                Send Feedback
              </Button>
            </>
          )}
          {isApproved && mode === 'view' && (
            <>
              <Button
                variant="outlineDanger"
                size="sm"
                isDisabled={isBusy}
                onClick={() => setMode('expire')}
                mr="auto"
              >
                Mark as Expired
              </Button>
              <Button variant="ghost" size="sm" onClick={onClose}>
                Close
              </Button>
            </>
          )}
          {isApproved && mode === 'expire' && (
            <>
              <Button variant="ghost" size="sm" isDisabled={isBusy} onClick={() => setMode('view')}>
                Back
              </Button>
              <Button
                variant="outlineDanger"
                size="sm"
                isLoading={expirePlan.isLoading}
                isDisabled={isBusy}
                onClick={handleExpire}
              >
                Confirm Expire
              </Button>
            </>
          )}
          {!isPending && !isApproved && (
            <Button variant="ghost" size="sm" onClick={onClose}>
              Close
            </Button>
          )}
        </ModalFooter>
      </ModalContent>
    </Modal>
  );
};

const Plans: React.FC = () => {
  const [searchParams, setSearchParams] = useSearchParams();
  const [statusFilter, setStatusFilter] = useState('');
  const { isOpen, onOpen, onClose } = useDisclosure({
    defaultIsOpen: !!searchParams.get('focus'),
  });
  const [selectedId, setSelectedId] = useState<string | null>(
    searchParams.get('focus'),
  );

  const filters = useMemo(
    () => ({ status: statusFilter || undefined }),
    [statusFilter],
  );
  const { data, isLoading } = usePlans(filters);
  usePlanStream(filters, selectedId);
  const plans = useMemo(() => {
    const items = data?.items ?? [];
    // A revision supersedes its parent but both remain persisted for audit and
    // detail navigation. Show only the newest loaded revision in the table so
    // one logical plan occupies one row; the modal still exposes the complete
    // previous/next revision chain.
    const supersededPlanIds = new Set(
      items
        .map((plan) => plan.parent_plan_id)
        .filter((id): id is string => !!id),
    );
    return items.filter((plan) => !supersededPlanIds.has(plan.plan_id));
  }, [data?.items]);

  const openPlan = useCallback(
    (id: string) => {
      setSelectedId(id);
      onOpen();
    },
    [onOpen],
  );

  const handleClose = useCallback(() => {
    onClose();
    setSelectedId(null);
    if (searchParams.get('focus')) {
      searchParams.delete('focus');
      setSearchParams(searchParams, { replace: true });
    }
  }, [onClose, searchParams, setSearchParams]);

  return (
    <Box>
      <Box mb={6}>
        <HStack mb={2}>
          <Flex width="full" justify="space-between">
            <HStack gap={4} pl={2} color="text.heading">
              <Icon as={ClipboardDocumentListIcon} boxSize={10} />
              <ContentPageTitle>Plans</ContentPageTitle>
            </HStack>
          </Flex>
        </HStack>
        <Text pl={2} color="text.subtle">
          Agent-proposed action plans — approve, request revisions, or deny before any tool runs
        </Text>
      </Box>

      <HStack mb={4} maxW="240px">
        <Select
          size="sm"
          options={STATUS_FILTER_OPTIONS}
          value={STATUS_FILTER_OPTIONS.find((o) => o.value === statusFilter) ?? STATUS_FILTER_OPTIONS[0]}
          onChange={(opt) => setStatusFilter(opt?.value ?? '')}
          classNamePrefix="chakra-react-select"
        />
      </HStack>

      <Box borderWidth={1} borderColor="border.base" borderRadius="md" overflow="hidden">
        {isLoading ? (
          <HStack justify="center" py={10}>
            <Spinner size="sm" color="brand.base" />
          </HStack>
        ) : plans.length === 0 ? (
          <Text p={6} color="text.subtle" fontSize="sm">
            No plans submitted yet. Agents can propose plans via POST /api/v1/external/plans.
          </Text>
        ) : (
          <Table size="sm" variant="simple">
            <Thead bg="background.table.header">
              <Tr>
                <Th>Status</Th>
                <Th>Goal</Th>
                <Th>Agent</Th>
                <Th>Actions</Th>
                <Th>Rev</Th>
                <Th>Submitted</Th>
              </Tr>
            </Thead>
            <Tbody>
              {plans.map((plan: Plan) => (
                <Tr
                  key={plan.plan_id}
                  cursor="pointer"
                  sx={{
                    '&:hover, &:nth-of-type(odd):hover, &:nth-of-type(even):hover': {
                      bg: 'background.table.row.hover',
                    },
                  }}
                  onClick={() => openPlan(plan.plan_id)}
                >
                  <Td><PlanStatusBadge status={plan.status} /></Td>
                  <Td maxW="360px">
                    <Text fontSize="sm" noOfLines={1}>{plan.goal}</Text>
                  </Td>
                  <Td>
                    <Text fontSize="xs" fontFamily="mono">{plan.agent_id}</Text>
                  </Td>
                  <Td><Text fontSize="sm">{plan.actions.length}</Text></Td>
                  <Td><Text fontSize="sm">{plan.revision}</Text></Td>
                  <Td>
                    <Text fontSize="xs" whiteSpace="nowrap">
                      {formatTimestamp(plan.submitted_at)}
                    </Text>
                  </Td>
                </Tr>
              ))}
            </Tbody>
          </Table>
        )}
      </Box>

      {isOpen && selectedId && (
        <PlanDetailModal
          planId={selectedId}
          onClose={handleClose}
          onSelectPlan={setSelectedId}
        />
      )}
    </Box>
  );
};

export default Plans;
