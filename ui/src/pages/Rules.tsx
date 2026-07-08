import React, { useCallback, useState } from 'react';
import {
  Badge,
  Box,
  Button,
  Flex,
  HStack,
  Icon,
  IconButton,
  Spinner,
  Table,
  Tbody,
  Td,
  Text,
  Th,
  Thead,
  Tr,
  useDisclosure,
} from '@chakra-ui/react';
import { ShieldCheckIcon, ChevronUpIcon, ChevronDownIcon } from '@heroicons/react/24/outline';

import { ContentPageTitle } from '../components/Layout';
import { CreateRuleModal } from '../components/CreateRuleModal';
import { useRules, useMoveRule } from '../hooks/useRules';
import { useAgents } from '../hooks/useAgents';
import { type Agent, type Rule, type RuleAction } from '../api/AtryumAPI';

// ─── Constants ────────────────────────────────────────────────────────────────

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

// ─── Helpers ──────────────────────────────────────────────────────────────────

const patternLabel = (patterns: string[]): string =>
  patterns.length === 0 ? 'all' : patterns.join(', ');

const stopPropagation = (e: React.MouseEvent): void => e.stopPropagation();

// ─── RuleRow ──────────────────────────────────────────────────────────────────

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
  const handleMoveDown = useCallback(
    () => onMove(rule.id, 'down'),
    [onMove, rule.id],
  );
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
        <HStack gap={1}>
          <Badge
            colorScheme={ACTION_COLOR[rule.action]}
            fontSize="2xs"
            whiteSpace="nowrap"
          >
            {ACTION_LABEL[rule.action]}
          </Badge>
          {rule.applies_to === 'plan' && (
            <Badge colorScheme="cyan" fontSize="2xs" variant="subtle" whiteSpace="nowrap">
              Plans
            </Badge>
          )}
        </HStack>
      </Td>
      <Td>
        <Text fontSize="sm">{rule.description || '—'}</Text>
      </Td>
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

// ─── Main page ────────────────────────────────────────────────────────────────

const Rules: React.FC = () => {
  const { isOpen, onOpen, onClose } = useDisclosure();
  const [selectedRule, setSelectedRule] = useState<Rule | null>(null);

  const { data: rulesData, isLoading } = useRules();
  const { data: agentsData } = useAgents();
  const moveRule = useMoveRule();

  const rules = rulesData?.items ?? [];
  const isBusy = moveRule.isLoading;

  const openForCreate = useCallback(() => {
    setSelectedRule(null);
    onOpen();
  }, [onOpen]);

  const openForEdit = useCallback(
    (rule: Rule) => {
      setSelectedRule(rule);
      onOpen();
    },
    [onOpen],
  );

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

      <Box
        borderWidth={1}
        borderColor="border.base"
        borderRadius="md"
        overflow="hidden"
      >
        {isLoading ? (
          <HStack justify="center" py={10}>
            <Spinner size="sm" color="brand.base" />
          </HStack>
        ) : rules.length === 0 ? (
          <Text p={6} color="text.subtle" fontSize="sm">
            No rules configured. First match wins — more specific rules should be
            ordered first.
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

      <CreateRuleModal isOpen={isOpen} onClose={onClose} rule={selectedRule} />
    </Box>
  );
};

export default Rules;
