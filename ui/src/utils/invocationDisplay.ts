import type { Invocation, Rule, InvocationEvent } from '../api/AtryumAPI';

export const STATUS_COLOR: Record<string, string> = {
  pending_approval: 'orange',
  approved: 'green',
  denied: 'red',
  running: 'blue',
  executing: 'blue',
  completed: 'green',
  succeeded: 'green',
  failed: 'red',
  received: 'neutral',
  expired: 'neutral',
  cancelled: 'neutral',
};

export const STATUS_LABEL: Record<string, string> = {
  pending_approval: 'Pending Approval',
  approved: 'Approved',
  denied: 'Denied',
  running: 'Running',
  executing: 'Executing',
  completed: 'Completed',
  succeeded: 'Succeeded',
  failed: 'Failed',
  received: 'Received',
  expired: 'Expired',
  cancelled: 'Cancelled',
};

export type DispositionInfo = { label: string; color: string };

export const getDisposition = (
  inv: Pick<Invocation, 'status' | 'approval'>,
): DispositionInfo[] => {
  const approvalStatus = inv.approval?.status;
  const reason = inv.approval?.reason ?? '';
  if (approvalStatus === 'plan_approved')
    return [{ label: 'Plan', color: 'cyan' }];
  if (reason.startsWith('ai_evaluation'))
    return [{ label: 'AI Evaluation', color: 'purple' }];
  if (approvalStatus === 'auto_approved')
    return [{ label: 'Rule', color: 'purple' }];
  if (approvalStatus === 'auto_denied')
    return [{ label: 'Rule', color: 'purple' }];
  if (reason.startsWith('matched approval rule'))
    return [{ label: 'Rule', color: 'purple' }];
  if (approvalStatus === 'ai_escalated')
    return [{ label: 'AI Escalated', color: 'yellow' }];
  if (
    approvalStatus === 'ai_escalated_approved' ||
    approvalStatus === 'ai_escalated_denied'
  )
    return [
      { label: 'AI Escalated', color: 'yellow' },
      { label: 'Human', color: 'blue' },
    ];
  if (approvalStatus === 'approved' || approvalStatus === 'denied')
    return [{ label: 'Human', color: 'blue' }];
  if (inv.status === 'pending_approval')
    return [{ label: 'Awaiting Human', color: 'orange' }];
  if (inv.status === 'received') return [{ label: '—', color: 'gray' }];
  return [{ label: '—', color: 'gray' }];
};

export const isAIEvaluated = (
  inv: Pick<Invocation, 'status' | 'approval'>,
): boolean => {
  const reason = inv.approval?.reason ?? '';
  const status = inv.approval?.status ?? '';
  return (
    reason.startsWith('ai_evaluation') ||
    status === 'ai_escalated' ||
    status === 'ai_escalated_approved' ||
    status === 'ai_escalated_denied'
  );
};

export const getConfidenceColor = (score: number): string => {
  if (score >= 0.8) return 'green';
  if (score >= 0.5) return 'yellow';
  return 'red';
};

export const formatConfidence = (score: number): string =>
  `${Math.round(score * 100)}%`;

export const formatDate = (dateStr: string | null | undefined): string => {
  if (!dateStr) return '—';
  return new Date(dateStr).toLocaleString(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  });
};

// ─── Invocation Audit ─────────────────────────────────────────────────────────

export type AuditStepVariant =
  | 'approve'
  | 'deny'
  | 'defer'
  | 'pending'
  | 'info'
  | 'error';

export type AuditStep = {
  text: string;
  variant: AuditStepVariant;
  /** ISO timestamp for this step, if known */
  timestamp?: string;
  /** Raw actor identifier for human decision steps */
  actor?: string;
};

export type AuditEntry = {
  /** null means no rule matched */
  ruleName: string | null;
  ruleId: string | null;
  isAIEvaluation: boolean;
  confidence?: number;
  steps: AuditStep[];
};

type InvocationAuditInput = {
  status: string;
  completed_at?: string | null;
  approval?: {
    status: string;
    reason?: string | null;
    confidence_score?: number | null;
    actor_id?: string | null;
  } | null;
  matched_rule_id?: string | null;
};

type RuleEvaluatedEventData = {
  rule_id?: string;
  action?: string;
  disposition?: string;
  confidence?: number;
  reason?: string;
  skipped?: boolean;
};

type DecisionEventData = {
  actor_id?: string;
};

/**
 * Derives structured audit entries from an invocation + rules list + events.
 *
 * When `invocation.rule_evaluated` events are present the full evaluation
 * chain — including skipped rules — is reconstructed from those events.
 * Otherwise falls back to deriving a single entry from the invocation's
 * final approval fields.
 */
export function buildInvocationAudit(
  inv: InvocationAuditInput,
  rules: Rule[],
  events: InvocationEvent[] = [],
): AuditEntry[] {
  const ruleEvalEvents = events.filter(
    (e) => e.type === 'invocation.rule_evaluated',
  );

  const entries =
    ruleEvalEvents.length > 0
      ? buildAuditFromEvents(inv, rules, events, ruleEvalEvents)
      : buildAuditFromApproval(inv, rules);

  // If the invocation failed, append a terminal failure step to the last entry
  // so it appears regardless of whether failure happened during approval wait
  // or after approval during execution.
  if (inv.status === 'failed') {
    const failStep: AuditStep = {
      text: 'Invocation failed',
      variant: 'error',
      timestamp: inv.completed_at ?? undefined,
    };
    if (entries.length > 0) {
      const last = entries[entries.length - 1];
      if (!last.steps.some((s) => s.variant === 'error')) {
        last.steps.push(failStep);
      }
    } else {
      entries.push({
        ruleName: null,
        ruleId: null,
        isAIEvaluation: false,
        steps: [failStep],
      });
    }
  }

  return entries;
}

function buildAuditFromEvents(
  inv: InvocationAuditInput,
  rules: Rule[],
  allEvents: InvocationEvent[],
  ruleEvalEvents: InvocationEvent[],
): AuditEntry[] {
  const decisionEvent = allEvents.find(
    (e) =>
      e.type === 'invocation.approved' ||
      e.type === 'invocation.denied' ||
      e.type === 'invocation.executing',
  );
  const actor = (decisionEvent?.data as DecisionEventData | undefined)
    ?.actor_id;
  const decisionTimestamp = decisionEvent?.timestamp;

  return ruleEvalEvents.map((evt) => {
    const d = (evt.data ?? {}) as RuleEvaluatedEventData;
    const ruleId = d.rule_id ?? null;
    const rule = ruleId ? (rules.find((r) => r.id === ruleId) ?? null) : null;
    const ruleName =
      rule?.description ?? (ruleId ? '*Deleted Rule*' : null);
    const isAIEval = d.action === 'ai_evaluation' && !!ruleId;
    const confidence = d.confidence;
    const aiReason =
      d.action === 'ai_evaluation' && d.reason
        ? extractAIReason(d.reason)
        : undefined;

    const steps: AuditStep[] = [];

    if (d.skipped) {
      steps.push({
        text: aiReason ?? 'Skipped this rule as per charter',
        variant: 'info',
        timestamp: evt.timestamp,
      });
    } else {
      const resolved = resolveDecisionSteps(
        d.disposition ?? '',
        inv,
        actor,
        isAIEval,
        aiReason,
        !ruleId && !isAIEval,
      );
      steps.push(
        ...resolved.map((step) => ({
          ...step,
          timestamp:
            step.variant === 'approve' || step.variant === 'deny'
              ? decisionTimestamp
              : evt.timestamp,
        })),
      );
    }

    return { ruleName, ruleId, isAIEvaluation: isAIEval, confidence, steps };
  });
}

function buildAuditFromApproval(
  inv: InvocationAuditInput,
  rules: Rule[],
): AuditEntry[] {
  const ruleId = inv.matched_rule_id ?? null;
  const rule = ruleId ? (rules.find((r) => r.id === ruleId) ?? null) : null;

  const approvalStatus = inv.approval?.status ?? null;
  const reason = inv.approval?.reason ?? '';
  const confidence = inv.approval?.confidence_score ?? undefined;
  const actor = inv.approval?.actor_id ?? undefined;

  const isAIEval =
    (reason ?? '').startsWith('ai_evaluation') ||
    approvalStatus === 'ai_escalated' ||
    approvalStatus === 'ai_escalated_approved' ||
    approvalStatus === 'ai_escalated_denied';

  // When matched_rule_id is absent but the reason indicates a specific rule was
  // AI-evaluated (not the all-deferred fallback), the rule likely existed and
  // was since deleted. Distinguish that from a genuine no-match fallback.
  const isAIAllDeferred = reason.startsWith(
    'ai_evaluation: all matching rules deferred',
  );
  const ruleName =
    rule?.description ??
    (ruleId
      ? '*Deleted Rule*'
      : isAIEval && !isAIAllDeferred
        ? '*Deleted Rule*'
        : null);

  const steps = resolveDecisionSteps(
    approvalStatus ?? '',
    inv,
    actor ?? undefined,
    isAIEval,
    undefined,
    !ruleId && !isAIEval,
  );

  return [{ ruleName, ruleId, isAIEvaluation: isAIEval, confidence, steps }];
}

function extractAIReason(raw: string): string {
  const exact: Record<string, string> = {
    'ai_evaluation: all matching rules deferred; falling back to human_approval':
      'All matching rules deferred — falling back to human approval',
  };
  if (exact[raw]) return exact[raw];

  const prefixes = [
    'ai_evaluation requires human approval: ',
    'ai_evaluation approved: ',
    'ai_evaluation denied: ',
    'ai_evaluation deferred to next rule: ',
  ];
  for (const prefix of prefixes) {
    if (raw.startsWith(prefix)) return raw.slice(prefix.length);
  }
  return raw;
}

function resolveDecisionSteps(
  disposition: string,
  inv: InvocationAuditInput,
  actor?: string,
  isAIEval?: boolean,
  aiReason?: string,
  isServerDefault?: boolean,
): AuditStep[] {
  const approvalStatus = inv.approval?.status ?? null;
  const reason = inv.approval?.reason ?? '';
  const deferReason =
    aiReason ??
    (isAIEval && reason ? extractAIReason(reason) : undefined);

  if (disposition === 'auto' || disposition === 'auto_approved') {
    const byAI = isAIEval ?? reason.startsWith('ai_evaluation');
    return [
      {
        text: byAI
          ? 'Invocation approved by AI'
          : isServerDefault
            ? 'Invocation approved by server default'
            : 'Invocation approved by rule',
        variant: 'approve',
      },
    ];
  }
  if (disposition === 'never' || disposition === 'auto_denied') {
    const byAI = isAIEval ?? reason.startsWith('ai_evaluation');
    return [
      {
        text: byAI
          ? 'Invocation denied by AI'
          : isServerDefault
            ? 'Invocation denied by server default'
            : 'Invocation denied by rule',
        variant: 'deny',
      },
    ];
  }
  if (
    disposition === 'ai_escalated' ||
    disposition === 'ai_escalated_approved' ||
    disposition === 'ai_escalated_denied'
  ) {
    const escalatedDeferText =
      deferReason ?? 'Deferring to human approval as per charter';
    if (
      approvalStatus === 'ai_escalated_approved' ||
      approvalStatus === 'approved'
    ) {
      return [
        { text: escalatedDeferText, variant: 'defer' },
        { text: 'Invocation approved by human', variant: 'approve', actor },
      ];
    }
    if (
      approvalStatus === 'ai_escalated_denied' ||
      approvalStatus === 'denied'
    ) {
      return [
        { text: escalatedDeferText, variant: 'defer' },
        { text: 'Invocation denied by human', variant: 'deny', actor },
      ];
    }
    return [
      { text: escalatedDeferText, variant: 'defer' },
      ...undecidedSteps(inv),
    ];
  }
  if (disposition === 'human' || disposition === 'workflow') {
    const deferText =
      disposition === 'workflow'
        ? 'Deferring to approval workflow'
        : deferReason ?? 'Human approval required as per rule';
    if (
      approvalStatus === 'approved' ||
      approvalStatus === 'ai_escalated_approved'
    ) {
      return [
        { text: deferText, variant: 'defer' },
        { text: 'Approved by human', variant: 'approve', actor },
      ];
    }
    if (
      approvalStatus === 'denied' ||
      approvalStatus === 'ai_escalated_denied'
    ) {
      return [
        { text: deferText, variant: 'defer' },
        { text: 'Denied by human', variant: 'deny', actor },
      ];
    }
    return [
      { text: deferText, variant: 'defer' },
      ...undecidedSteps(inv),
    ];
  }
  if (disposition === 'approved') {
    return [{ text: 'Approved by human', variant: 'approve', actor }];
  }
  if (disposition === 'denied') {
    return [{ text: 'Denied by human', variant: 'deny', actor }];
  }
  if (inv.status === 'pending_approval') {
    return [{ text: 'Awaiting human approval', variant: 'pending' }];
  }
  if (inv.status === 'expired') {
    return [{ text: 'Expired without decision', variant: 'info' }];
  }
  if (inv.status === 'cancelled') {
    return [{ text: 'Cancelled', variant: 'info' }];
  }
  // Unknown disposition (e.g. a value added on the backend before this UI
  // learns about it): render it raw rather than an empty step list.
  if (disposition !== '') {
    return [{ text: `Decision: ${disposition}`, variant: 'info' }];
  }
  return [];
}

/**
 * Steps for an invocation that reached a human/workflow gate but has no
 * recorded decision. Only render "Awaiting human" while a decision is still
 * possible; on a terminal status show what actually ended the wait ('failed'
 * returns nothing — buildInvocationAudit appends the failure step).
 */
function undecidedSteps(inv: InvocationAuditInput): AuditStep[] {
  if (inv.status === 'failed') {
    return [];
  }
  if (inv.status === 'expired') {
    return [{ text: 'Expired without decision', variant: 'info' }];
  }
  if (inv.status === 'cancelled') {
    return [{ text: 'Cancelled', variant: 'info' }];
  }
  return [{ text: 'Awaiting human', variant: 'pending' }];
}
