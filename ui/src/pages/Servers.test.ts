import { describe, expect, it } from 'vitest';

import type { AuthHeader, Server } from '../api/AtryumAPI';
import {
  clientSecretMoved,
  headersToSave,
  isRenamedWithoutValue,
  storedHeaderKeys,
} from './Servers';

// What the API returns for a server with two stored custom headers: values
// are write-only, so each comes back blank with has_value set.
const server: Server = {
  name: 'github',
  endpoint_slug: 'github',
  mode: 'http',
  enabled: true,
  connection_status: 'ready',
  auth_status: 'ok',
  base_url: 'https://mcp.example.com/mcp',
  auth_headers: [
    { name: 'X-Keep', value: '', has_value: true },
    { name: 'X-Rotate', value: '', has_value: true },
  ],
};

// A form row as loaded from the server: blank value, has_value copied over.
const loaded = (name: string, value = ''): AuthHeader => ({ name, value, has_value: true });

describe('stored header values', () => {
  const stored = storedHeaderKeys(server);

  it('matches stored headers by trimmed, case-insensitive name', () => {
    expect(stored).toEqual(new Set(['x-keep', 'x-rotate']));
    expect(storedHeaderKeys(undefined).size).toBe(0);
    expect(
      storedHeaderKeys({ ...server, auth_headers: [{ name: 'X-New', value: '' }] }).size,
    ).toBe(0);
  });

  // The server matches a stored value to a header by name. Sent blank under
  // a new name, the header and its value would both be dropped.
  it('flags a renamed stored header that has no value', () => {
    expect(isRenamedWithoutValue(loaded('X-Renamed'), stored)).toBe(true);
    expect(isRenamedWithoutValue(loaded('X-Renamed', 'typed'), stored)).toBe(false);
    expect(isRenamedWithoutValue(loaded('X-Keep'), stored)).toBe(false);
    expect(isRenamedWithoutValue(loaded(' x-keep '), stored)).toBe(false);
    // A new row was never stored, so there is nothing to lose.
    expect(isRenamedWithoutValue({ name: 'X-Blank', value: '' }, stored)).toBe(false);
  });

  it('sends a blank value only for a name that has a stored value', () => {
    expect(
      headersToSave(
        [
          loaded('X-Keep'),
          loaded('X-Rotate', 'rotated-secret'),
          { name: 'X-Added', value: 'added-secret' },
          { name: 'X-Blank', value: '' },
          { name: ' ', value: 'no-name' },
        ],
        stored,
      ),
    ).toEqual([
      { name: 'X-Keep', value: '' },
      { name: 'X-Rotate', value: 'rotated-secret' },
      { name: 'X-Added', value: 'added-secret' },
    ]);
  });

  it('does not send a renamed stored header as a blank to keep', () => {
    expect(headersToSave([loaded('X-Renamed'), loaded('x-rotate')], stored)).toEqual([
      { name: 'x-rotate', value: '' },
    ]);
  });
});

describe('clientSecretMoved', () => {
  const preshared: Server = {
    ...server,
    has_oauth_client_secret: true,
    oauth_client_registration: 'preshared',
    oauth_token_url: 'https://auth.example.com/token',
  };

  it('is false while the base URL host and token URL host are unchanged', () => {
    expect(clientSecretMoved(preshared, 'https://auth.example.com/token', false)).toBe(false);
    expect(clientSecretMoved(preshared, 'https://AUTH.example.com:443/other', false)).toBe(false);
  });

  it('is true once the base URL host or the token URL host changes', () => {
    expect(clientSecretMoved(preshared, 'https://auth.example.com/token', true)).toBe(true);
    expect(clientSecretMoved(preshared, 'https://auth.other.example.com/token', false)).toBe(true);
  });

  it('ignores a token URL that is blank on either side', () => {
    expect(clientSecretMoved(preshared, '', false)).toBe(false);
    expect(
      clientSecretMoved({ ...preshared, oauth_token_url: '' }, 'https://auth.example.com/token', false),
    ).toBe(false);
  });

  it('applies only to a stored pre-shared client secret', () => {
    expect(clientSecretMoved(undefined, '', true)).toBe(false);
    expect(clientSecretMoved({ ...preshared, has_oauth_client_secret: false }, '', true)).toBe(false);
    expect(
      clientSecretMoved({ ...preshared, oauth_client_registration: 'dynamic' }, '', true),
    ).toBe(false);
  });
});
