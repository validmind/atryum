import { describe, expect, it } from 'vitest';

import { getDisposition } from './invocationDisplay';

describe('getDisposition', () => {
  it('labels a self-protection denial', () => {
    expect(
      getDisposition({
        status: 'denied',
        approval: { status: 'self_protection_denied', reason: 'x' },
        plan_id: null,
      } as Parameters<typeof getDisposition>[0]),
    ).toEqual([{ label: 'Self-protection', color: 'red' }]);
  });

  it('labels a self-protection hold as awaiting a person', () => {
    expect(
      getDisposition({
        status: 'pending_approval',
        approval: { status: 'self_protection_escalated', reason: 'x' },
        plan_id: null,
      } as Parameters<typeof getDisposition>[0]).map((d) => d.label),
    ).toEqual(['Self-protection', 'Awaiting Human']);
  });

  it('keeps the self-protection label after a person decides', () => {
    for (const status of [
      'self_protection_escalated_approved',
      'self_protection_escalated_denied',
    ]) {
      expect(
        getDisposition({
          status: 'approved',
          approval: { status, reason: 'human' },
          plan_id: null,
        } as Parameters<typeof getDisposition>[0]).map((d) => d.label),
      ).toEqual(['Self-protection', 'Human']);
    }
  });
});
