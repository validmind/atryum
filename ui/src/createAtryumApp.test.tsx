import React from 'react';
import { describe, expect, it } from 'vitest';

import { createAtryumApp } from './createAtryumApp';

describe('createAtryumApp', () => {
  it('constructs the stock application shell', () => {
    expect(React.isValidElement(createAtryumApp())).toBe(true);
  });
});
