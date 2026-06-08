import type { Invocation } from '../api/AtryumAPI';

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

export const formatDate = (dateStr: string | null | undefined): string => {
  if (!dateStr) return '—';
  return new Date(dateStr).toLocaleString(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  });
};
