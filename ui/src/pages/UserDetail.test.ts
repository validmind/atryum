import { describe, expect, it } from 'vitest';

import { userDetailTab, userDisplayName } from './UserDetail';

describe('userDetailTab', () => {
  it('opens the keys tab for /users/:id/keys', () => {
    expect(userDetailTab('/users/abc/keys')).toBe('keys');
    expect(userDetailTab('/users/abc/keys/')).toBe('keys');
  });

  it('opens the agents tab for /users/:id/agents', () => {
    expect(userDetailTab('/users/abc/agents')).toBe('agents');
  });

  it('falls back to agents for a bare or unknown path', () => {
    expect(userDetailTab('/users/abc')).toBe('agents');
    expect(userDetailTab('/users/abc/whatever')).toBe('agents');
  });
});

describe('userDisplayName', () => {
  it('prefers email, then name, then subject', () => {
    expect(userDisplayName({ email: 'a@b.c', name: 'A', subject: 's' })).toBe('a@b.c');
    expect(userDisplayName({ email: '', name: 'A', subject: 's' })).toBe('A');
    expect(userDisplayName({ email: '', name: '', subject: 'google-oauth2|1' })).toBe('google-oauth2|1');
  });
});
