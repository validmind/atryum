import { describe, expect, it } from 'vitest';

import { visibleNavItems } from './Layout';

describe('visibleNavItems', () => {
  it('shows the full menu until the principal is known', () => {
    const labels = visibleNavItems(undefined).map((i) => i.label);
    expect(labels).toEqual(['Invocations', 'Plans', 'Agents', 'Servers', 'Rules', 'Users', 'Settings']);
  });

  it('shows everything to admins, including Users', () => {
    expect(visibleNavItems('admin').map((i) => i.label)).toContain('Users');
  });

  it('hides admin-only pages from members', () => {
    expect(visibleNavItems('member').map((i) => i.label)).toEqual(['Agents']);
  });
});
