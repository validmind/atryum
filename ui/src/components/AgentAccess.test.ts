import { describe, expect, it } from 'vitest';

import type { AgentAPIKey } from '../api/AtryumAPI';
import { keyStatus } from './AgentAccess';

const base: AgentAPIKey = {
  id: 'k1',
  agent_id: 'a1',
  name: 'laptop',
  key_prefix: 'atr_abcd1234',
  created_at: '2026-09-01T00:00:00Z',
  active: true,
};

describe('keyStatus', () => {
  it('reports active keys', () => {
    expect(keyStatus(base)).toEqual({ label: 'active', color: 'green' });
  });

  it('reports revoked keys regardless of expiry', () => {
    expect(keyStatus({ ...base, active: false, revoked_at: '2026-09-02T00:00:00Z' }).label).toBe('revoked');
  });

  it('reports expired keys', () => {
    expect(keyStatus({ ...base, active: false, expires_at: '2000-01-01T00:00:00Z' }).label).toBe('expired');
  });

  it('treats a future expiry as active', () => {
    expect(keyStatus({ ...base, expires_at: '2999-01-01T00:00:00Z' }).label).toBe('active');
  });
});
